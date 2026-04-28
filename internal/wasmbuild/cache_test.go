package wasmbuild

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/wasmify/internal/buildjson"
)

func TestBuildCache_IsCached(t *testing.T) {
	tmpDir := t.TempDir()

	// Create an output file
	outputFile := filepath.Join(tmpDir, "test.o")
	_ = os.WriteFile(outputFile, []byte("object"), 0o644)

	step := WasmBuildStep{
		Type:       buildjson.StepCompile,
		Args:       []string{"-c", "-o", outputFile, "test.cc"},
		OutputFile: outputFile,
	}

	bc := LoadBuildCache(tmpDir)

	// Not cached initially
	if bc.IsCached(step) {
		t.Fatal("expected not cached initially")
	}

	// Update cache
	bc.Update(step)

	// Now cached
	if !bc.IsCached(step) {
		t.Fatal("expected cached after update")
	}

	// Change args → not cached
	step2 := step
	step2.Args = []string{"-c", "-O2", "-o", outputFile, "test.cc"}
	if bc.IsCached(step2) {
		t.Fatal("expected not cached with different args")
	}
}

func TestBuildCache_SaveLoad(t *testing.T) {
	tmpDir := t.TempDir()

	outputFile := filepath.Join(tmpDir, "test.o")
	_ = os.WriteFile(outputFile, []byte("object"), 0o644)

	step := WasmBuildStep{
		Type:       buildjson.StepCompile,
		Args:       []string{"-c", "test.cc"},
		OutputFile: outputFile,
	}

	// Save
	bc := LoadBuildCache(tmpDir)
	bc.Update(step)
	if err := bc.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Load in new instance
	bc2 := LoadBuildCache(tmpDir)
	if !bc2.IsCached(step) {
		t.Fatal("expected cached after load")
	}
}

func TestBuildCache_MissingOutput(t *testing.T) {
	tmpDir := t.TempDir()

	outputFile := filepath.Join(tmpDir, "test.o")
	_ = os.WriteFile(outputFile, []byte("object"), 0o644)

	step := WasmBuildStep{
		Type:       buildjson.StepCompile,
		Args:       []string{"-c", "test.cc"},
		OutputFile: outputFile,
	}

	bc := LoadBuildCache(tmpDir)
	bc.Update(step)

	// Remove output → not cached
	_ = os.Remove(outputFile)
	if bc.IsCached(step) {
		t.Fatal("expected not cached when output missing")
	}
}
