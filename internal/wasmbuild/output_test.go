package wasmbuild

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteOutput covers the output.* artifact copy — relative paths
// resolve against the wasmify.json directory, absolute paths are kept,
// and missing intermediate directories are created.
func TestWriteOutput(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.wasm")
	content := []byte("\x00asm\x01\x00\x00\x00payload")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}

	base := filepath.Join(dir, "proj")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("relative dst resolves against baseDir and creates dirs", func(t *testing.T) {
		got, err := WriteOutput(src, filepath.Join("gen", "app.wasm"), base)
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(base, "gen", "app.wasm")
		if got != want {
			t.Errorf("resolved dst = %q, want %q", got, want)
		}
		data, err := os.ReadFile(want)
		if err != nil {
			t.Fatalf("output file unreadable: %v", err)
		}
		if string(data) != string(content) {
			t.Errorf("output content mismatch")
		}
	})

	t.Run("absolute dst is used unchanged", func(t *testing.T) {
		abs := filepath.Join(dir, "out", "abs.wasm")
		got, err := WriteOutput(src, abs, base)
		if err != nil {
			t.Fatal(err)
		}
		if got != abs {
			t.Errorf("resolved dst = %q, want %q (absolute, unchanged)", got, abs)
		}
		if _, err := os.Stat(abs); err != nil {
			t.Errorf("absolute dst not written: %v", err)
		}
	})

	t.Run("missing source is reported", func(t *testing.T) {
		if _, err := WriteOutput(filepath.Join(dir, "nope.wasm"), "x.wasm", base); err == nil {
			t.Error("expected error for missing source artifact")
		}
	})
}
