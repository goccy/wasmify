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

// wasmHeader is the fixed 8-byte module preamble: \0asm magic + version 1.
var wasmHeader = []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}

// memSection builds a memory section (id 5) with one memory of the
// given limits flag and a min of 1 page.
func memSection(flags byte) []byte {
	payload := []byte{0x01, flags, 0x01}
	if flags&0x01 != 0 {
		payload = append(payload, 0x02) // max, when the flag declares one
	}
	return append([]byte{0x05, byte(len(payload))}, payload...)
}

func TestWasmDeclaresMemory64(t *testing.T) {
	cases := []struct {
		name string
		bin  []byte
		want bool
	}{
		{"wasm32 min-only", append(append([]byte{}, wasmHeader...), memSection(0x00)...), false},
		{"wasm32 min+max", append(append([]byte{}, wasmHeader...), memSection(0x01)...), false},
		{"wasm64 min-only", append(append([]byte{}, wasmHeader...), memSection(0x04)...), true},
		{"wasm64 min+max", append(append([]byte{}, wasmHeader...), memSection(0x05)...), true},
		{"no memory section", wasmHeader, false},
		{"truncated header", wasmHeader[:4], false},
		{"empty", nil, false},
		{
			// A preceding section (empty type section, id 1) must be
			// skipped, not misread as the memory section.
			"memory after another section",
			append(append(append([]byte{}, wasmHeader...), 0x01, 0x01, 0x00), memSection(0x04)...),
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wasmDeclaresMemory64(tc.bin); got != tc.want {
				t.Errorf("wasmDeclaresMemory64 = %v, want %v", got, tc.want)
			}
		})
	}
}
