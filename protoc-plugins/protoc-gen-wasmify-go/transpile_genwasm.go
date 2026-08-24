package main

import (
	"bytes"
	"fmt"
	"os"
	"strconv"

	"github.com/goccy/wasm2go/transpile"
)

// transpilerOptions converts the plugin's external configuration into
// wasm2go's typed Options. wasm2go itself branches on options only;
// this entry point is where the WASM2GO_* environment (the only
// channel a buf-spawned plugin inherits from a Make/CI pipeline) is
// translated, exactly once.
//
// WASM2GO_FAST_MATH accepts explicit values: "", "0" and "false" mean
// off. The numeric knobs parse as integers; malformed values are
// reported rather than silently ignored, since a typo here silently
// changes the generated code's performance envelope.
func transpilerOptions(opts *transpile.Options) error {
	num := func(name string, dst *int) error {
		v := os.Getenv(name)
		if v == "" {
			return nil
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid %s=%q: %w", name, v, err)
		}
		*dst = n
		return nil
	}
	if err := num("WASM2GO_OUTLINE", &opts.OutlineMinValues); err != nil {
		return err
	}
	if err := num("WASM2GO_UNROLL", &opts.SIMDUnroll); err != nil {
		return err
	}
	if err := num("WASM2GO_FUSE_LOOP_UNROLL", &opts.FuseLoopUnroll); err != nil {
		return err
	}
	if err := num("WASM2GO_VEC_DOT_PAIR_ENTRY", &opts.VecDotPairEntry); err != nil {
		return err
	}
	// Same off-values as WASM2GO_FAST_MATH: "0" and "false" read as
	// off so a Make-level `VAR ?= 1` default can be overridden on the
	// command line without unsetting the variable.
	switch os.Getenv("WASM2GO_VEC_DOT_ROWS") {
	case "", "0", "false":
	default:
		opts.VecDotRows = true
	}
	opts.FuseLoops = os.Getenv("WASM2GO_FUSE_LOOP") != ""
	// The f16 table address is auto-detected by wasm2go v0.5.5+ (no
	// address input exists anymore); the only knob left is the
	// opt-out that disables the table-keyed rewrites outright.
	switch os.Getenv("WASM2GO_NO_F16_TABLE") {
	case "", "0", "false":
	default:
		opts.DisableF16Table = true
	}
	switch os.Getenv("WASM2GO_FAST_MATH") {
	case "", "0", "false":
	default:
		opts.FastMath = true
	}
	opts.PureOnly = os.Getenv("WASM2GO_PURE") != ""
	if v := os.Getenv("WASM2GO_MULTIPACKAGE_THRESHOLD"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return fmt.Errorf("invalid WASM2GO_MULTIPACKAGE_THRESHOLD=%q: %w", v, err)
		}
		// Process-wide by design: the plugin is one transpile per
		// process. The restore closure is discarded on purpose.
		transpile.SetMultiPackageThreshold(n)
	}
	return nil
}

// transpileGenwasm converts a wasm binary into the multi-file Go
// "wasm2go" package that the runtime=wasm2go bridge drives. It returns
// the relative-path → contents map and a flag indicating whether
// wasm2go emitted the single-package layout (true) or the multi-package
// layout (false).
//
// baseImport is the Go import path of the directory the wasm2go
// packages will live under; for multi-package output the chunk
// packages land at <baseImport>/base, <baseImport>/p0, ...
func transpileGenwasm(wasmBin []byte, pkg, baseImport string) (files map[string][]byte, singlePkg bool, err error) {
	m, err := transpile.Parse(bytes.NewReader(wasmBin))
	if err != nil {
		return nil, false, fmt.Errorf("parse wasm: %w", err)
	}
	// The bridge runtime and the transpiled package must agree on guest
	// pointer width, so record whether this wasm declares a memory64
	// linear memory before generating either.
	wasm2goMem64 = m.Memory64()
	// In single-file mode the main Go source is written to mainBuf
	// (everything else — asm bundle, pure-Go fallback, alias.go —
	// arrives in res.Files alongside it). In multi-package mode
	// mainBuf is left empty and the entire output (main file
	// included) is in res.Files. So the writer-vs-Files split, not
	// the size of res.Files, is the reliable signal for which
	// layout wasm2go picked.
	var mainBuf bytes.Buffer
	opts := transpile.Options{
		Package:          pkg,
		OutputImportPath: baseImport,
		BulkExportPrefix: "w_",
	}
	if err := transpilerOptions(&opts); err != nil {
		return nil, false, err
	}
	res, err := transpile.Translate(&mainBuf, m, opts)
	if err != nil {
		return nil, false, fmt.Errorf("transpile wasm: %w", err)
	}
	singlePkg = mainBuf.Len() > 0
	files = make(map[string][]byte, len(res.Files)+len(res.Sidecars)+len(res.AuxFiles)+1)
	for k, v := range res.Files {
		files[k] = v
	}
	if mainBuf.Len() > 0 {
		files[pkg+".go"] = mainBuf.Bytes()
	}
	for k, v := range res.Sidecars {
		files[k] = v
	}
	for k, v := range res.AuxFiles {
		files[k] = v
	}
	if len(files) == 0 {
		return nil, false, fmt.Errorf("transpile produced no files")
	}
	return files, singlePkg, nil
}
