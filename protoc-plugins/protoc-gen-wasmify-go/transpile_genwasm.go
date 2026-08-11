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
	opts.FuseLoops = os.Getenv("WASM2GO_FUSE_LOOP") != ""
	if v := os.Getenv("WASM2GO_F16_TABLE"); v != "" {
		n, err := strconv.ParseUint(v, 0, 32)
		if err != nil {
			return fmt.Errorf("invalid WASM2GO_F16_TABLE=%q: %w", v, err)
		}
		opts.F16TableAddr = uint32(n)
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
// wasmDeclaresMemory64 reports whether the binary's memory section
// declares a 64-bit (memory64) linear memory, by walking the section
// framing of the wasm spec's binary format: 8-byte magic+version, then
// (id byte, LEB128 size, payload) sections; in the memory section (id
// 5) each entry starts with a limits flag whose 0x04 bit is the
// memory64 marker. Only defined memories are inspected — every wasm
// the wasmify pipeline builds defines its own linear memory rather
// than importing one.
//
// This duplicates a check the wasm2go parser makes so the plugin can
// choose pointer widths without requiring a wasm2go release that
// exports it; the format is the WebAssembly spec's, not an internal
// protocol.
func wasmDeclaresMemory64(bin []byte) bool {
	const headerLen = 8 // \0asm + version
	if len(bin) < headerLen {
		return false
	}
	readU32 := func(p []byte) (uint64, int) { // LEB128, bounded at u32 range
		var v uint64
		for i := 0; i < 5 && i < len(p); i++ {
			v |= uint64(p[i]&0x7f) << (7 * i)
			if p[i]&0x80 == 0 {
				return v, i + 1
			}
		}
		return 0, 0
	}
	for off := headerLen; off < len(bin); {
		id := bin[off]
		size, n := readU32(bin[off+1:])
		if n == 0 {
			return false
		}
		body := off + 1 + n
		if body+int(size) > len(bin) {
			return false
		}
		if id == 5 { // memory section
			p := bin[body : body+int(size)]
			count, cn := readU32(p)
			if cn == 0 {
				return false
			}
			// The first defined memory decides the module's address
			// width (multi-memory is not in play in this pipeline).
			if count > 0 && cn < len(p) {
				return p[cn]&0x04 != 0
			}
			return false
		}
		off = body + int(size)
	}
	return false
}

func transpileGenwasm(wasmBin []byte, pkg, baseImport string) (files map[string][]byte, singlePkg bool, err error) {
	// The bridge runtime and the transpiled package must agree on guest
	// pointer width, so record whether this wasm declares a memory64
	// linear memory before generating either.
	wasm2goMem64 = wasmDeclaresMemory64(wasmBin)
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
	res, err := transpile.Transpile(bytes.NewReader(wasmBin), &mainBuf, opts)
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
