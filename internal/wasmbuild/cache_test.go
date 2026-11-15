package wasmbuild

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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

	bc := LoadBuildCache(tmpDir, "wasm32-wasip1")

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
	bc := LoadBuildCache(tmpDir, "wasm32-wasip1")
	bc.Update(step)
	if err := bc.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Load in new instance
	bc2 := LoadBuildCache(tmpDir, "wasm32-wasip1")
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

	bc := LoadBuildCache(tmpDir, "wasm32-wasip1")
	bc.Update(step)

	// Remove output → not cached
	_ = os.Remove(outputFile)
	if bc.IsCached(step) {
		t.Fatal("expected not cached when output missing")
	}
}

// An archive is up to date only when every member the step names is there:
// an archive left by an earlier build, whose members this build did not
// produce under the names the step has, is made again (and the archiver
// then says which member it lacks).
func TestBuildCache_ArchiveWithMissingMembers(t *testing.T) {
	tmpDir := t.TempDir()
	archive := filepath.Join(tmpDir, "lib", "Util.a")
	if err := os.MkdirAll(filepath.Dir(archive), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, []byte("!<arch>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, second := filepath.Join(tmpDir, "obj", "First.o"), filepath.Join(tmpDir, "obj", "Second.o")
	step := WasmBuildStep{
		Type:       buildjson.StepArchive,
		OriginalID: 7,
		Args:       []string{"rc", archive, first, second},
		WorkDir:    tmpDir,
		OutputFile: archive,
		InputFiles: collectInputFiles([]string{"rc", archive, first, second}),
	}

	// Without an entry (the make rules) and with one, alike.
	for _, recorded := range []bool{false, true} {
		bc := LoadBuildCache(tmpDir, "wasm32-wasip1")
		if recorded {
			bc.Update(step)
		}
		if bc.IsCached(step) {
			t.Errorf("recorded=%v: an archive none of whose members exists is up to date", recorded)
		}
		if got := missingArchiveMember(step); got != first {
			t.Errorf("missing member = %q, want %q", got, first)
		}
	}

	// The members are made after the archive: it is older than they are.
	if err := os.MkdirAll(filepath.Dir(first), 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(archive, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first, []byte("o"), 0o644); err != nil {
		t.Fatal(err)
	}
	bc := LoadBuildCache(tmpDir, "wasm32-wasip1")
	bc.Update(step)
	if bc.IsCached(step) {
		t.Error("an archive one of whose members is missing is up to date")
	}
	if err := os.WriteFile(second, []byte("o"), 0o644); err != nil {
		t.Fatal(err)
	}
	if bc.IsCached(step) {
		t.Error("an archive older than its members is up to date")
	}

	// The archive made from them is up to date, by a member's relative
	// name too (the archiver runs in the step's working directory).
	now := time.Now().Add(time.Minute)
	if err := os.Chtimes(archive, now, now); err != nil {
		t.Fatal(err)
	}
	if !bc.IsCached(step) {
		t.Error("an archive newer than all its members is not up to date")
	}
	relative := step
	relative.Args = []string{"rc", archive, "obj/First.o", "obj/Second.o"}
	relative.InputFiles = collectInputFiles(relative.Args)
	bc.Update(relative)
	if !bc.IsCached(relative) {
		t.Error("members named relative to the working directory were not found")
	}
}

// A compile step stays up to date when an input cannot be found where the
// step names it: a header given to -include is found by the compiler's
// search path, not by this name.
func TestBuildCache_CompileWithHeaderFoundBySearchPath(t *testing.T) {
	tmpDir := t.TempDir()
	source, object := filepath.Join(tmpDir, "a.c"), filepath.Join(tmpDir, "a.o")
	for _, f := range []string{source, object} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().Add(time.Minute)
	if err := os.Chtimes(object, now, now); err != nil {
		t.Fatal(err)
	}
	args := []string{"-c", "-include", "config.h", "-Iinclude", "-o", object, source}
	step := WasmBuildStep{
		Type:       buildjson.StepCompile,
		OriginalID: 3,
		Args:       args,
		WorkDir:    tmpDir,
		OutputFile: object,
		InputFiles: collectInputFiles(args),
	}
	if len(step.InputFiles) != 2 {
		t.Fatalf("inputs = %q, want the header and the source", step.InputFiles)
	}
	bc := LoadBuildCache(tmpDir, "wasm32-wasip1")
	if !bc.IsCached(step) {
		t.Error("without an entry: not up to date")
	}
	bc.Update(step)
	if !bc.IsCached(step) {
		t.Error("with an entry: not up to date")
	}

	// A header that is found by its name is an input like the source.
	header := filepath.Join(tmpDir, "found.h")
	if err := os.WriteFile(header, []byte("h"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Minute)
	if err := os.Chtimes(header, later, later); err != nil {
		t.Fatal(err)
	}
	step.Args = []string{"-c", "-include", header, "-o", object, source}
	step.InputFiles = collectInputFiles(step.Args)
	bc.Update(step)
	if bc.IsCached(step) {
		t.Error("up to date although the -include header is newer than the object")
	}
}

// A compile step of a source tree names its source relative to the step's
// working directory ("op.c", run in perl5/): an edit of that source makes
// the object out of date, whatever wasmify's own working directory is.
func TestBuildCache_CompileWithSourceRelativeToWorkDir(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "perl5")
	object := filepath.Join(tmpDir, "obj", "perl5", "op.o")
	for _, d := range []string{workDir, filepath.Dir(object)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(workDir, "op.c")
	for _, f := range []string{source, object} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"-c", "-o", object, "op.c"}
	step := WasmBuildStep{
		Type:       buildjson.StepCompile,
		OriginalID: 5,
		Args:       args,
		WorkDir:    workDir,
		OutputFile: object,
		InputFiles: collectInputFiles(args),
	}
	if len(step.InputFiles) != 1 || step.InputFiles[0] != "op.c" {
		t.Fatalf("inputs = %q, want the relative source", step.InputFiles)
	}

	// The source is edited after the object was made.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(object, old, old); err != nil {
		t.Fatal(err)
	}
	for _, recorded := range []bool{false, true} {
		bc := LoadBuildCache(tmpDir, "wasm32-wasip1")
		if recorded {
			bc.Update(step)
		}
		if bc.IsCached(step) {
			t.Errorf("recorded=%v: up to date although op.c is newer than op.o", recorded)
		}
	}

	// The object made from it afterwards is up to date.
	now := time.Now().Add(time.Minute)
	if err := os.Chtimes(object, now, now); err != nil {
		t.Fatal(err)
	}
	bc := LoadBuildCache(tmpDir, "wasm32-wasip1")
	if !bc.IsCached(step) {
		t.Error("without an entry: an object newer than its source is not up to date")
	}
	bc.Update(step)
	if !bc.IsCached(step) {
		t.Error("with an entry: an object newer than its source is not up to date")
	}
}
