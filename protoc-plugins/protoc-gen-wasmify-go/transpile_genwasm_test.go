package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTranspileGenwasm drives the wasm2go transpiler through the plugin
// helper using a 55-byte synthetic module — no real wasmify project. It
// verifies the helper returns at least one valid Go source (whose layout
// wasm2go picks automatically based on input size) and a data sidecar.
func TestTranspileGenwasm(t *testing.T) {
	wasmBin, err := os.ReadFile(filepath.Join("testdata", "tiny.wasm"))
	if err != nil {
		t.Skipf("read fixture: %v", err)
	}

	files, singlePkg, err := transpileGenwasm(wasmBin, "genwasm", "example.com/x/internal/genwasm")
	if err != nil {
		t.Fatalf("transpileGenwasm: %v", err)
	}
	// A 55-byte fixture is far below the multi-package threshold, so
	// wasm2go must report single-package.
	if !singlePkg {
		t.Errorf("singlePkg = false, want true for the tiny fixture")
	}

	goFiles := 0
	fset := token.NewFileSet()
	for path, content := range files {
		if !strings.HasSuffix(path, ".go") {
			continue
		}
		goFiles++
		if _, err := parser.ParseFile(fset, path, content, parser.PackageClauseOnly); err != nil {
			t.Errorf("%s does not parse: %v", path, err)
		}
	}
	if goFiles == 0 {
		t.Error("no Go files produced")
	}
}
