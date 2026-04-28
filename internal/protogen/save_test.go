package protogen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveProto(t *testing.T) {
	tmp := t.TempDir()
	content := "syntax = \"proto3\";\npackage test;\n"
	if err := SaveProto(tmp, content, "test.proto"); err != nil {
		t.Fatalf("SaveProto failed: %v", err)
	}
	path := filepath.Join(tmp, "proto", "test.proto")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Errorf("content mismatch")
	}
}

func TestSaveOptionsProto(t *testing.T) {
	tmp := t.TempDir()
	if err := SaveOptionsProto(tmp); err != nil {
		t.Fatalf("SaveOptionsProto failed: %v", err)
	}
	path := filepath.Join(tmp, "proto", "wasmify", "options.proto")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	// Check for expected options
	for _, want := range []string{"wasm_handle", "wasm_parent", "wasm_abstract", "wasm_method_type"} {
		if !contains(text, want) {
			t.Errorf("expected %q in options.proto", want)
		}
	}
}

func TestSaveBridge(t *testing.T) {
	tmp := t.TempDir()
	if err := SaveBridge(tmp, "// cc content", "// h content"); err != nil {
		t.Fatalf("SaveBridge failed: %v", err)
	}
	// Both files created
	for _, name := range []string{"api_bridge.cc", "api_bridge.h"} {
		path := filepath.Join(tmp, "wasm-build", "src", name)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s: %v", name, err)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(s) > 0 && indexOf(s, sub) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
