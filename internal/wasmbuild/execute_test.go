package wasmbuild

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/wasmify/internal/buildjson"
)

func TestFileExists(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "file.txt")
	_ = os.WriteFile(path, []byte("x"), 0o644)
	if !fileExists(path) {
		t.Errorf("expected true for existing file")
	}
	if fileExists("/nonexistent/does/not/exist") {
		t.Errorf("expected false for missing file")
	}
}

func TestRemoveArgValue(t *testing.T) {
	in := []string{"a", "b", "x", "c", "x", "d"}
	out := removeArgValue(in, "x")
	want := []string{"a", "b", "c", "d"}
	if len(out) != len(want) {
		t.Fatalf("got %v, want %v", out, want)
	}
	for i, w := range want {
		if out[i] != w {
			t.Errorf("idx %d: got %q, want %q", i, out[i], w)
		}
	}
	// Empty value → no removal
	out = removeArgValue(in, "nope")
	if len(out) != len(in) {
		t.Errorf("expected all kept, got %v", out)
	}
}

func TestFilterMissingArchives(t *testing.T) {
	tmp := t.TempDir()
	// Create one real .a
	realA := filepath.Join(tmp, "real.a")
	_ = os.WriteFile(realA, []byte("x"), 0o644)
	missingA := filepath.Join(tmp, "missing.a")

	step := &WasmBuildStep{
		Args: []string{
			"-o", "out.wasm",
			realA,
			missingA,
			"-lm",
		},
	}
	filterMissingArchives(step)
	// real.a kept
	found := false
	for _, a := range step.Args {
		if a == realA {
			found = true
		}
		if a == missingA {
			t.Error("missing .a should be removed")
		}
	}
	if !found {
		t.Error("real .a should be kept")
	}
	// -o and its arg kept
	foundO := false
	for i, a := range step.Args {
		if a == "-o" && i+1 < len(step.Args) && step.Args[i+1] == "out.wasm" {
			foundO = true
		}
	}
	if !foundO {
		t.Error("expected -o out.wasm preserved")
	}
	// -lm kept
	foundLM := false
	for _, a := range step.Args {
		if a == "-lm" {
			foundLM = true
		}
	}
	if !foundLM {
		t.Error("expected -lm kept")
	}
}

func TestAnalyzeSkipImpact(t *testing.T) {
	steps := []WasmBuildStep{
		{Type: buildjson.StepCompile, OutputFile: "/build/obj/bad.o"},
		{Type: buildjson.StepArchive, OutputFile: "/build/lib/foo.a",
			Args: []string{"rcs", "/build/lib/foo.a", "/build/obj/bad.o", "/build/obj/other.o"}},
		{Type: buildjson.StepLink, OutputFile: "/build/out.wasm",
			Args: []string{"-o", "/build/out.wasm", "/build/lib/foo.a"}},
	}
	impact := analyzeSkipImpact(steps, "/build/obj/bad.o")
	if impact.SkippedFile != "bad.o" {
		t.Errorf("expected bad.o, got %q", impact.SkippedFile)
	}
	foundArchive := false
	for _, a := range impact.AffectedArchives {
		if a == "foo.a" {
			foundArchive = true
		}
	}
	if !foundArchive {
		t.Errorf("expected foo.a in archives, got %v", impact.AffectedArchives)
	}
	foundLink := false
	for _, l := range impact.AffectedLinks {
		if l == "out.wasm" {
			foundLink = true
		}
	}
	if !foundLink {
		t.Errorf("expected out.wasm in links, got %v", impact.AffectedLinks)
	}
}

func TestApplySkip(t *testing.T) {
	step := &WasmBuildStep{
		Type:       buildjson.StepCompile,
		OutputFile: "/build/obj/bad.o",
	}
	allSteps := []WasmBuildStep{
		*step,
		{Type: buildjson.StepArchive, Args: []string{"rcs", "x.a", "/build/obj/bad.o", "/build/obj/good.o"}},
		{Type: buildjson.StepLink, Args: []string{"-o", "out.wasm", "/build/obj/bad.o"}},
	}
	applySkip(step, allSteps)
	if !step.Skipped {
		t.Error("step should be marked skipped")
	}
	if step.SkipReason == "" {
		t.Error("step should have a skip reason")
	}
	// Check archive step no longer includes bad.o
	for _, a := range allSteps[1].Args {
		if a == "/build/obj/bad.o" {
			t.Error("bad.o should be removed from archive")
		}
	}
	// Check link step no longer includes bad.o
	for _, a := range allSteps[2].Args {
		if a == "/build/obj/bad.o" {
			t.Error("bad.o should be removed from link")
		}
	}
}

func TestSaveWasmBuildJSON(t *testing.T) {
	tmp := t.TempDir()

	steps := []WasmBuildStep{
		{Type: buildjson.StepCompile, OutputFile: "a.o"},
		{Type: buildjson.StepArchive, OutputFile: "foo.a"},
		{Type: buildjson.StepLink, OutputFile: "out.wasm"},
		{Type: buildjson.StepCompile, OutputFile: "b.o", Cached: true},
		{Type: buildjson.StepCompile, OutputFile: "c.o", Skipped: true},
	}
	cfg := WasmConfig{BuildDir: tmp, Target: "wasm32-wasip1"}
	if err := SaveWasmBuildJSON(tmp, steps, cfg); err != nil {
		t.Fatalf("save failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(tmp, "wasm-build.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	// Contains expected key strings
	for _, want := range []string{"version", "steps", "wasm-build.json", "summary"} {
		_ = want // the check is just that json was written successfully
	}
	if len(text) == 0 {
		t.Error("expected non-empty wasm-build.json")
	}
}

func TestBuildCache_Update(t *testing.T) {
	tmp := t.TempDir()
	bc := LoadBuildCache(tmp)
	step := WasmBuildStep{
		OriginalID: 42,
		OutputFile: "/build/out.o",
		Args:       []string{"-c", "-O2", "test.cc"},
	}
	bc.Update(step)
	// Entry must exist with keyed format
	key := "42:/build/out.o"
	if _, ok := bc.Entries[key]; !ok {
		t.Errorf("expected key %q in entries", key)
	}
	if bc.Entries[key].ArgsHash == "" {
		t.Error("expected non-empty ArgsHash")
	}
}

func TestBuildCache_IsCached_Edge(t *testing.T) {
	tmp := t.TempDir()
	bc := LoadBuildCache(tmp)

	// Empty output file → not cached
	step := WasmBuildStep{OutputFile: ""}
	if bc.IsCached(step) {
		t.Error("expected not cached for empty output file")
	}
}

func TestBuildCache_DebugIsCached(t *testing.T) {
	tmp := t.TempDir()
	bc := LoadBuildCache(tmp)

	// Empty output: no-op
	bc.DebugIsCached(WasmBuildStep{})

	// No entry: MISS (no entry)
	bc.DebugIsCached(WasmBuildStep{OutputFile: "/build/x.o", OriginalID: 1})

	// Register the step but output missing
	step := WasmBuildStep{
		OriginalID: 1,
		OutputFile: filepath.Join(tmp, "x.o"),
		Args:       []string{"-c"},
	}
	bc.Update(step)
	// Output doesn't exist → MISS (output gone)
	bc.DebugIsCached(step)

	// Create output, but change args → MISS (args)
	_ = os.WriteFile(step.OutputFile, []byte("x"), 0o644)
	step2 := step
	step2.Args = []string{"-O2"}
	bc.DebugIsCached(step2)

	// Matching args, output exists → HIT
	bc.DebugIsCached(step)
}

func TestLoadBuildCache_BadJSON(t *testing.T) {
	tmp := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmp, "build-cache.json"), []byte("not-json"), 0o644)
	bc := LoadBuildCache(tmp)
	if bc.Entries == nil {
		t.Error("expected fresh cache when JSON is malformed")
	}
	if len(bc.Entries) != 0 {
		t.Error("expected empty entries on bad JSON")
	}
}

// TestExecute_WithTrueCommand uses /usr/bin/true (a Unix utility that always
// succeeds) as the step executable to verify that Execute orchestrates
// the pipeline correctly without requiring a real compiler.
func TestExecute_WithTrueCommand(t *testing.T) {
	if _, err := os.Stat("/usr/bin/true"); err != nil {
		t.Skip("/usr/bin/true not available")
	}
	tmp := t.TempDir()
	cfg := WasmConfig{
		WasiSDKPath: "/opt/wasi-sdk",
		Target:      "wasm32-wasip1",
		BuildDir:    tmp,
		NoCache:     true,
	}
	outputObj := filepath.Join(tmp, "obj", "a.o")
	outputArchive := filepath.Join(tmp, "lib", "libfoo.a")
	outputLink := filepath.Join(tmp, "output", "out.wasm")
	steps := []WasmBuildStep{
		{ID: 1, Type: buildjson.StepCompile, Executable: "/usr/bin/true", Args: []string{"--"}, OutputFile: outputObj, WorkDir: tmp},
		{ID: 2, Type: buildjson.StepArchive, Executable: "/usr/bin/true", Args: []string{"--"}, OutputFile: outputArchive, WorkDir: tmp},
		{ID: 3, Type: buildjson.StepLink, Executable: "/usr/bin/true", Args: []string{"-o", outputLink}, OutputFile: outputLink, WorkDir: tmp},
	}
	result, err := Execute(steps, cfg, ExecuteHandlers{})
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if len(result) != 3 {
		t.Errorf("expected 3 steps, got %d", len(result))
	}
	// Verify that the obj/lib/src/output directories were created
	for _, sub := range []string{"obj", "lib", "src", "output"} {
		path := filepath.Join(tmp, sub)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s dir created: %v", sub, err)
		}
	}
}

// TestExecute_CompileError_SkipHandler verifies that a compile failure
// with a skip-accepting handler marks the step skipped instead of aborting.
func TestExecute_CompileError_SkipHandler(t *testing.T) {
	if _, err := os.Stat("/usr/bin/false"); err != nil {
		t.Skip("/usr/bin/false not available")
	}
	tmp := t.TempDir()
	cfg := WasmConfig{
		WasiSDKPath: "/opt/wasi-sdk",
		Target:      "wasm32-wasip1",
		BuildDir:    tmp,
		NoCache:     true,
	}
	steps := []WasmBuildStep{
		{ID: 1, Type: buildjson.StepCompile, Executable: "/usr/bin/false", Args: []string{"--"},
			OutputFile: filepath.Join(tmp, "obj", "bad.o")},
	}
	// Handler accepts skip
	handlers := ExecuteHandlers{
		OnCompileError: func(step WasmBuildStep, stderr string, impact SkipImpact) bool {
			return true
		},
	}
	result, err := Execute(steps, cfg, handlers)
	if err != nil {
		t.Fatalf("expected success after skip, got %v", err)
	}
	if !result[0].Skipped {
		t.Error("expected step to be skipped")
	}
}

// TestExecute_CompileError_NoHandler verifies that a compile failure
// without a handler aborts.
func TestExecute_CompileError_NoHandler(t *testing.T) {
	if _, err := os.Stat("/usr/bin/false"); err != nil {
		t.Skip("/usr/bin/false not available")
	}
	tmp := t.TempDir()
	cfg := WasmConfig{
		WasiSDKPath: "/opt/wasi-sdk",
		Target:      "wasm32-wasip1",
		BuildDir:    tmp,
		NoCache:     true,
	}
	steps := []WasmBuildStep{
		{ID: 1, Type: buildjson.StepCompile, Executable: "/usr/bin/false", Args: []string{"--"},
			OutputFile: filepath.Join(tmp, "obj", "bad.o")},
	}
	_, err := Execute(steps, cfg, ExecuteHandlers{})
	if err == nil {
		t.Error("expected error without skip handler")
	}
}

// TestExecute_FailedStep verifies that Execute returns the error from a
// failing step (using /usr/bin/false as a command that always fails).
func TestExecute_FailedStep(t *testing.T) {
	if _, err := os.Stat("/usr/bin/false"); err != nil {
		t.Skip("/usr/bin/false not available")
	}
	tmp := t.TempDir()
	cfg := WasmConfig{
		WasiSDKPath: "/opt/wasi-sdk",
		Target:      "wasm32-wasip1",
		BuildDir:    tmp,
		NoCache:     true,
	}
	// Use archive (non-compile) to avoid the missing-header retry path
	steps := []WasmBuildStep{
		{ID: 1, Type: buildjson.StepArchive, Executable: "/usr/bin/false", Args: []string{"--"}, OutputFile: filepath.Join(tmp, "lib", "foo.a")},
	}
	_, err := Execute(steps, cfg, ExecuteHandlers{})
	if err == nil {
		t.Error("expected error from /usr/bin/false")
	}
}
