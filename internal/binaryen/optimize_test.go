package binaryen

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// fakeBinaryenStub writes a tiny shell script that mimics
// `wasm-opt -Oz ... -o <out> <in>` by copying <in> to <out> and
// optionally trimming or padding the output to control size. The
// returned directory satisfies WasmOptPath(dir).
//
// Behaviour selectors:
//
//	"shrink"  — write input size - 1 bytes to output (success path).
//	"grow"    — write input size + 1 bytes (regression path).
//	"fail"    — exit non-zero without writing output.
func fakeBinaryenStub(t *testing.T, behaviour string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var script string
	switch behaviour {
	case "shrink":
		script = `#!/bin/sh
# POSIX-only: dash on Ubuntu does not support bash's ${@: -1} so we
# walk the args manually and remember the trailing positional.
out=""
in=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-o" ]; then out="$arg"; fi
  in="$arg"
  prev="$arg"
done
size=$(wc -c < "$in" | tr -d ' ')
new=$((size - 1))
head -c "$new" "$in" > "$out"
`
	case "grow":
		script = `#!/bin/sh
out=""
in=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-o" ]; then out="$arg"; fi
  in="$arg"
  prev="$arg"
done
cat "$in" "$in" > "$out"
`
	case "fail":
		script = `#!/bin/sh
exit 7
`
	default:
		t.Fatalf("unknown behaviour %q", behaviour)
	}
	stub := filepath.Join(binDir, "wasm-opt")
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".wasmify-version"), []byte(Version), 0o644); err != nil {
		t.Fatalf("stamp version: %v", err)
	}
	// Sanity: ensure stub is executable.
	if _, err := exec.LookPath(stub); err != nil {
		// LookPath only checks PATH; a direct stat suffices.
		if _, statErr := os.Stat(stub); statErr != nil {
			t.Fatalf("stub not visible: %v", statErr)
		}
	}
	return dir
}

func TestOptimizeShrinks(t *testing.T) {
	dir := fakeBinaryenStub(t, "shrink")
	in := filepath.Join(t.TempDir(), "in.wasm")
	out := filepath.Join(t.TempDir(), "out.wasm")
	if err := os.WriteFile(in, []byte("\x00\x61\x73\x6dpadding-bytes-here"), 0o644); err != nil {
		t.Fatalf("seed input: %v", err)
	}
	res, err := Optimize(in, out, OptimizeOptions{InstallDir: dir})
	if err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	if res.AfterSize >= res.BeforeSize {
		t.Errorf("expected shrink: before=%d after=%d", res.BeforeSize, res.AfterSize)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("output wasm missing: %v", err)
	}
}

// TestOptimizeFailsClosedOnRegression pins the contract that wasmify
// never silently ships a larger wasm. If the post-opt binary grew,
// Optimize must error and leave the original file untouched.
func TestOptimizeFailsClosedOnRegression(t *testing.T) {
	dir := fakeBinaryenStub(t, "grow")
	tmp := t.TempDir()
	in := filepath.Join(tmp, "in.wasm")
	original := []byte("\x00\x61\x73\x6dseed-bytes")
	if err := os.WriteFile(in, original, 0o644); err != nil {
		t.Fatalf("seed input: %v", err)
	}
	_, err := Optimize(in, in, OptimizeOptions{InstallDir: dir})
	if err == nil {
		t.Fatal("Optimize succeeded on a size regression; want failure")
	}
	got, _ := os.ReadFile(in)
	if string(got) != string(original) {
		t.Errorf("input was mutated despite failure: got %q want %q", got, original)
	}
}

// TestOptimizeFailsOnToolError makes sure a wasm-opt non-zero exit
// surfaces as an error rather than silently leaving the input as the
// "result".
func TestOptimizeFailsOnToolError(t *testing.T) {
	dir := fakeBinaryenStub(t, "fail")
	tmp := t.TempDir()
	in := filepath.Join(tmp, "in.wasm")
	if err := os.WriteFile(in, []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed input: %v", err)
	}
	_, err := Optimize(in, filepath.Join(tmp, "out.wasm"), OptimizeOptions{InstallDir: dir})
	if err == nil {
		t.Fatal("expected error from failing wasm-opt; got nil")
	}
}
