package buildjson

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The scenario mirrors a Perl Makefile.PL feature probe captured mid-build:
// tmp.c is compiled and linked, the probe answers, and the temp files are
// unlinked — while the real objects and archives survive on disk.
func TestMarkTransientSteps(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("real.c")
	write("real.o")
	write("libreal.a")

	steps := []BuildStep{
		{ID: 1, Type: StepCompile, Executable: "clang", WorkDir: dir,
			InputFiles: []string{"real.c"}, OutputFile: "real.o"},
		{ID: 2, Type: StepArchive, Executable: "ar", WorkDir: dir,
			InputFiles: []string{"real.o"}, OutputFile: "libreal.a"},
		// Probe: compile tmp.o then link the probe executable; both files
		// were deleted by the build before the capture ended.
		{ID: 3, Type: StepCompile, Executable: "clang", WorkDir: dir,
			InputFiles: []string{"tmp123.c"}, OutputFile: "tmp123.o"},
		{ID: 4, Type: StepLink, Executable: "clang", WorkDir: dir,
			InputFiles: []string{"tmp123.o"}, OutputFile: "tmp123"},
	}

	var log bytes.Buffer
	MarkTransientSteps(steps, &log)

	if steps[0].WasmSkip || steps[1].WasmSkip {
		t.Fatalf("surviving steps were marked: compile=%v archive=%v",
			steps[0].WasmSkip, steps[1].WasmSkip)
	}
	if !steps[3].WasmSkip {
		t.Fatalf("probe link (deleted output) was not marked")
	}
	// The probe link no longer consumes tmp123.o, so its compile step must
	// unwind too.
	if !steps[2].WasmSkip {
		t.Fatalf("probe compile (deleted output, only consumed by a transient link) was not marked")
	}
	if got := strings.Count(log.String(), "wasm_skip"); got != 2 {
		t.Fatalf("progress lines = %d, want 2:\n%s", got, log.String())
	}
}

// A deleted output that a surviving later step still consumes must NOT be
// marked: replay order rebuilds it, and skipping it would corrupt the chain.
func TestMarkTransientStepsKeepsConsumedIntermediates(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "final"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	steps := []BuildStep{
		// mid.o was deleted after linking (a build that cleans objects),
		// but the surviving link consumes it.
		{ID: 1, Type: StepCompile, Executable: "clang", WorkDir: dir,
			InputFiles: []string{"mid.c"}, OutputFile: "mid.o"},
		{ID: 2, Type: StepLink, Executable: "clang", WorkDir: dir,
			InputFiles: []string{"mid.o"}, OutputFile: "final"},
	}

	MarkTransientSteps(steps, nil)

	if steps[0].WasmSkip {
		t.Fatalf("intermediate consumed by a surviving link was marked transient")
	}
	if steps[1].WasmSkip {
		t.Fatalf("surviving final link was marked transient")
	}
}

// Steps already skipped by the developer must stay skipped and must not keep
// other deleted outputs alive.
func TestMarkTransientStepsIgnoresPreskipped(t *testing.T) {
	dir := t.TempDir()

	steps := []BuildStep{
		{ID: 1, Type: StepCompile, Executable: "clang", WorkDir: dir,
			InputFiles: []string{"gone.c"}, OutputFile: "gone.o"},
		{ID: 2, Type: StepLink, Executable: "clang", WorkDir: dir,
			InputFiles: []string{"gone.o"}, OutputFile: "gone",
			WasmSkip: true, WasmSkipReason: "developer decision"},
	}

	MarkTransientSteps(steps, nil)

	if steps[1].WasmSkipReason != "developer decision" {
		t.Fatalf("pre-existing skip reason was overwritten: %q", steps[1].WasmSkipReason)
	}
	// The pre-skipped link consumes nothing, so gone.o is transient.
	if !steps[0].WasmSkip {
		t.Fatalf("deleted output consumed only by a pre-skipped step was not marked")
	}
}
