package main

import (
	"bytes"
	"fmt"

	"github.com/goccy/wasm2go/transpile"
)

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
	// In single-file mode the main Go source is written to mainBuf
	// (everything else — asm bundle, pure-Go fallback, alias.go —
	// arrives in res.Files alongside it). In multi-package mode
	// mainBuf is left empty and the entire output (main file
	// included) is in res.Files. So the writer-vs-Files split, not
	// the size of res.Files, is the reliable signal for which
	// layout wasm2go picked.
	var mainBuf bytes.Buffer
	res, err := transpile.Transpile(bytes.NewReader(wasmBin), &mainBuf, transpile.Options{
		Package:          pkg,
		OutputImportPath: baseImport,
		BulkExportPrefix: "w_",
	})
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
