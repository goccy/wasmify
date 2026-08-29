package buildjson

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// MarkTransientSteps flags captured compile/link/archive steps whose output
// file no longer exists on disk and is not consumed by any surviving later
// step. The captured build itself deleted the file, so the step was a
// probe/scratch artifact — the classic case is a configure-style feature
// test (autoconf, or a Perl Makefile.PL compiling tmp$$.c) whose temp source
// and output are unlinked as soon as the probe answers. Such a step can
// never be replayed (its inputs are gone with it) and contributes nothing to
// the final artifacts, so it is marked wasm_skip and `wasmify wasm-build`
// ignores it deterministically instead of failing on the vanished file.
//
// Steps are scanned in reverse so that a chain of transient steps (compile
// tmp.o, then link tmp.o into a probe executable) unwinds: once the probe
// link is marked, nothing consumes tmp.o any more and its compile step is
// marked too. Steps already carrying wasm_skip are left untouched. Marked
// steps are reported to progress (one line each) so a capture that silently
// drops a step the developer expected to survive is visible in the log.
//
// Call this after the capture finished, while the build tree still reflects
// the capture's end state; statting the tree at any later time would judge
// files the developer cleaned up manually.
func MarkTransientSteps(steps []BuildStep, progress io.Writer) {
	// consumed holds the resolved output paths that some surviving later
	// step reads. Built incrementally during the reverse scan: when we visit
	// step i, consumed already reflects exactly the steps after i.
	consumed := make(map[string]bool)

	addInputs := func(s BuildStep) {
		for _, in := range s.InputFiles {
			consumed[resolveStepPath(s.WorkDir, in)] = true
		}
	}

	for i := len(steps) - 1; i >= 0; i-- {
		s := &steps[i]
		// Only compile/link/archive steps replay in wasm-build; anything
		// else (and anything already skipped) neither survives nor keeps
		// another step's output alive.
		if s.Type != StepCompile && s.Type != StepLink && s.Type != StepArchive {
			continue
		}
		if s.WasmSkip {
			continue
		}
		if s.OutputFile != "" {
			out := resolveStepPath(s.WorkDir, s.OutputFile)
			if _, err := os.Stat(out); os.IsNotExist(err) && !consumed[out] {
				s.WasmSkip = true
				s.WasmSkipReason = "transient probe artifact: output deleted by the captured build and consumed by no other step"
				if progress != nil {
					fmt.Fprintf(progress, "[generate-build] step %d (%s %s): output %s was deleted by the build; marking wasm_skip (probe/scratch artifact)\n",
						s.ID, s.Type, filepath.Base(s.Executable), s.OutputFile)
				}
				continue
			}
		}
		addInputs(*s)
	}
}

// resolveStepPath resolves a path recorded in a build step against the
// step's working directory, cleaning it for map-key comparison.
func resolveStepPath(workDir, p string) string {
	if !filepath.IsAbs(p) && workDir != "" {
		p = filepath.Join(workDir, p)
	}
	return filepath.Clean(p)
}
