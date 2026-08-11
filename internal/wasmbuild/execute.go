package wasmbuild

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/goccy/wasmify/internal/buildjson"
)

// MissingHeaderHandler is called when a missing header is detected during compilation.
// It receives the header path and a human-readable description of why the header is needed.
// It should return true if the stub should be deployed, false to abort.
type MissingHeaderHandler func(headerPath string, description string) bool

// CompileErrorHandler is called when a compile step fails with a non-header error.
// It receives the stderr output and the impact analysis of skipping the file.
// It should return true to skip the step and continue, false to abort.
type CompileErrorHandler func(step WasmBuildStep, stderrOutput string, impact SkipImpact) bool

// SkipImpact describes the downstream effects of skipping a compile step.
type SkipImpact struct {
	SkippedFile      string   // Base name of the .o file that won't be built
	AffectedArchives []string // .a files that would include this .o
	AffectedLinks    []string // Final outputs that link these .a files
}

// SkipPersister is called when the user accepts a skip decision so the caller
// can record it durably (typically: set WasmSkip=true on the corresponding
// step in build.json and save the file). OriginalID matches the step's ID in
// build.json, which is more stable across transforms than OutputFile.
type SkipPersister func(originalID int, reason string) error

// ExecuteHandlers groups the interactive handlers for Execute.
type ExecuteHandlers struct {
	OnMissingHeader MissingHeaderHandler
	OnCompileError  CompileErrorHandler
	OnSkipPersist   SkipPersister
}

// Execute runs all wasm build steps sequentially with build cache support.
// Handlers enable interactive error recovery (missing header stubs, skip on error).
func Execute(steps []WasmBuildStep, cfg WasmConfig, handlers ExecuteHandlers) ([]WasmBuildStep, error) {
	logEffectiveOptLevel(steps, cfg)

	// Create output directories
	for _, dir := range []string{"obj", "lib", "src", "output"} {
		if err := os.MkdirAll(filepath.Join(cfg.BuildDir, dir), 0o755); err != nil {
			return steps, fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	bc := LoadBuildCache(cfg.BuildDir)

	// Execute in order: compile, archive, link
	phases := []buildjson.StepType{
		buildjson.StepCompile,
		buildjson.StepArchive,
		buildjson.StepLink,
	}

	for _, phase := range phases {
		for i := range steps {
			if steps[i].Skipped || steps[i].Type != phase {
				continue
			}
			if !cfg.NoCache && bc.IsCached(steps[i]) {
				steps[i].Cached = true
				fmt.Fprintf(os.Stderr, "[wasm-build] cached %s\n", filepath.Base(steps[i].OutputFile))
				continue
			} else if !cfg.NoCache && os.Getenv("WASMIFY_CACHE_DEBUG") != "" {
				bc.DebugIsCached(steps[i])
			}

			// For link steps, remove references to .a files that don't exist
			// (e.g., external pre-built native libraries that weren't compiled for wasm)
			if steps[i].Type == buildjson.StepLink {
				filterMissingArchives(&steps[i])
			}

			err := executeStepWithRetry(&steps[i], steps, cfg, handlers)
			if err != nil {
				_ = bc.Save()
				return steps, fmt.Errorf("step %d (%s) failed: %w", steps[i].ID, steps[i].Type, err)
			}
			if !steps[i].Skipped {
				bc.Update(steps[i])
			}
		}
	}

	_ = bc.Save()
	return steps, nil
}

const maxMissingHeaderRetries = 20

// executeStepWithRetry runs a build step. For compile steps, it detects missing
// header errors and offers to deploy stubs via the handler, then retries.
// If the error is not a missing header, it offers to skip the step.
func executeStepWithRetry(step *WasmBuildStep, allSteps []WasmBuildStep, cfg WasmConfig, handlers ExecuteHandlers) error {
	// Non-compile steps: execute directly
	if step.Type != buildjson.StepCompile {
		return executeStep(*step)
	}

	for retry := 0; retry < maxMissingHeaderRetries; retry++ {
		stderrOutput, err := executeStepCapture(*step)
		if err == nil {
			return nil
		}

		// Try missing header recovery first
		missingHeaders := DetectMissingHeaders(stderrOutput)
		if len(missingHeaders) > 0 && handlers.OnMissingHeader != nil {
			deployed := false
			for _, header := range missingHeaders {
				stub, ok := LookupHeaderStub(header)
				if !ok {
					break // unknown header, fall through to compile error handler
				}
				if handlers.OnMissingHeader(header, stub.Description) {
					if deployErr := DeployStubHeader(cfg.PosixCompatDir, header, stub.Content); deployErr != nil {
						return fmt.Errorf("failed to deploy stub for <%s>: %w", header, deployErr)
					}
					fmt.Fprintf(os.Stderr, "[wasm-build] Deployed stub: %s\n", header)
					deployed = true
				}
			}
			if deployed {
				fmt.Fprintf(os.Stderr, "[wasm-build] Retrying: %s %s\n",
					filepath.Base(step.Executable), filepath.Base(step.OutputFile))
				continue
			}
		}

		// Not a missing header (or unknown header / rejected) — offer to skip
		if handlers.OnCompileError != nil {
			impact := analyzeSkipImpact(allSteps, step.OutputFile)
			if handlers.OnCompileError(*step, stderrOutput, impact) {
				applySkip(step, allSteps)
				if handlers.OnSkipPersist != nil {
					if perr := handlers.OnSkipPersist(step.OriginalID, step.SkipReason); perr != nil {
						fmt.Fprintf(os.Stderr, "[wasm-build] warning: could not persist skip decision: %v\n", perr)
					}
				}
				return nil
			}
		}

		// No handler or user rejected skip
		fmt.Fprint(os.Stderr, stderrOutput)
		fmt.Fprintf(os.Stderr, "[wasm-build] FAILED: %s %s\n", step.Executable, strings.Join(step.Args, " "))
		return err
	}

	return fmt.Errorf("too many missing header retries (max %d)", maxMissingHeaderRetries)
}

// analyzeSkipImpact determines which archive and link steps would be affected
// if the given .o file is not built.
func analyzeSkipImpact(steps []WasmBuildStep, skippedOutput string) SkipImpact {
	impact := SkipImpact{
		SkippedFile: filepath.Base(skippedOutput),
	}

	// Find archive steps that reference this .o
	affectedArchivePaths := map[string]bool{}
	for _, s := range steps {
		if s.Type != buildjson.StepArchive || s.Skipped {
			continue
		}
		for _, arg := range s.Args {
			if arg == skippedOutput {
				base := filepath.Base(s.OutputFile)
				impact.AffectedArchives = append(impact.AffectedArchives, base)
				affectedArchivePaths[s.OutputFile] = true
				break
			}
		}
	}

	// Find link steps that reference affected archives
	for _, s := range steps {
		if s.Type != buildjson.StepLink || s.Skipped {
			continue
		}
		for _, arg := range s.Args {
			if affectedArchivePaths[arg] {
				impact.AffectedLinks = append(impact.AffectedLinks, filepath.Base(s.OutputFile))
				break
			}
		}
	}

	return impact
}

// applySkip marks a compile step as skipped and removes its output from
// downstream archive/link step arguments so they don't fail on missing files.
func applySkip(step *WasmBuildStep, allSteps []WasmBuildStep) {
	step.Skipped = true
	step.SkipReason = "compile error (skipped by user)"

	// Remove the .o reference from archive and link steps
	for i := range allSteps {
		s := &allSteps[i]
		if s.Type != buildjson.StepArchive && s.Type != buildjson.StepLink {
			continue
		}
		s.Args = removeArgValue(s.Args, step.OutputFile)
		s.InputFiles = removeArgValue(s.InputFiles, step.OutputFile)
	}
}

// filterMissingArchives removes .a file references from link step args
// when the archive file doesn't exist (e.g., external pre-built native
// libraries that weren't compiled for wasm).
func filterMissingArchives(step *WasmBuildStep) {
	var filtered []string
	skipNext := false
	for i, arg := range step.Args {
		if skipNext {
			skipNext = false
			filtered = append(filtered, arg)
			continue
		}
		// Keep flags and their arguments
		if strings.HasPrefix(arg, "-") {
			filtered = append(filtered, arg)
			// -o takes a next argument
			if arg == "-o" && i+1 < len(step.Args) {
				skipNext = true
			}
			continue
		}
		// Check if .a or .o file exists
		ext := strings.ToLower(filepath.Ext(arg))
		if (ext == ".a" || ext == ".o") && !fileExists(arg) {
			fmt.Fprintf(os.Stderr, "[wasm-build] skipping missing: %s\n", filepath.Base(arg))
			continue
		}
		filtered = append(filtered, arg)
	}
	step.Args = filtered
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// removeArgValue removes all occurrences of value from args.
func removeArgValue(args []string, value string) []string {
	var result []string
	for _, a := range args {
		if a != value {
			result = append(result, a)
		}
	}
	return result
}

// executeStepCapture runs a build step and captures stderr output
// instead of printing it directly. Stdout is still forwarded.
func executeStepCapture(step WasmBuildStep) (string, error) {
	if step.OutputFile != "" {
		dir := filepath.Dir(step.OutputFile)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("failed to create output dir: %w", err)
		}
	}

	cmd := exec.Command(step.Executable, step.Args...)
	cmd.Dir = step.WorkDir
	cmd.Stdout = os.Stdout

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	fmt.Fprintf(os.Stderr, "[wasm-build] %s %s\n", filepath.Base(step.Executable), step.OutputFile)

	err := cmd.Run()
	return stderrBuf.String(), err
}

func executeStep(step WasmBuildStep) error {
	// Ensure output directory exists
	if step.OutputFile != "" {
		dir := filepath.Dir(step.OutputFile)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("failed to create output dir: %w", err)
		}
	}

	// An archive replay must start from a clean output: build systems
	// pair `ar q` (append) with an rm of the archive that the capture
	// never records, so replaying the ar alone onto a leftover archive
	// from an earlier run APPENDS — the stale members survive and the
	// link picks whichever copy it finds first (visible as "wasm32
	// object file can't be linked in wasm64 mode" after a target
	// switch).
	// ranlib is also classified StepArchive but only (re)indexes the
	// archive the preceding ar step wrote — removing its "output" would
	// delete that fresh archive.
	if step.Type == buildjson.StepArchive && step.OutputFile != "" &&
		!strings.Contains(filepath.Base(step.Executable), "ranlib") {
		if err := os.Remove(step.OutputFile); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove stale archive: %w", err)
		}
	}

	cmd := exec.Command(step.Executable, step.Args...)
	cmd.Dir = step.WorkDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Fprintf(os.Stderr, "[wasm-build] %s %s\n", filepath.Base(step.Executable), step.OutputFile)

	if err := cmd.Run(); err != nil {
		// Show full command on error
		fmt.Fprintf(os.Stderr, "[wasm-build] FAILED: %s %s\n", step.Executable, strings.Join(step.Args, " "))
		return err
	}
	return nil
}

// SaveWasmBuildJSON writes the wasm-build.json file.
func SaveWasmBuildJSON(dataDir string, steps []WasmBuildStep, cfg WasmConfig) error {
	summary := BuildSummary{}
	for _, s := range steps {
		summary.TotalSteps++
		if s.Skipped {
			summary.SkippedSteps++
			continue
		}
		if s.Cached {
			summary.CachedSteps++
		}
		switch s.Type {
		case buildjson.StepCompile:
			summary.CompileSteps++
		case buildjson.StepArchive:
			summary.ArchiveSteps++
		case buildjson.StepLink:
			summary.LinkSteps++
		}
	}

	wb := WasmBuildJSON{
		Version:   "1.0",
		Config:    cfg,
		Steps:     steps,
		Summary:   summary,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	data, err := json.MarshalIndent(wb, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal wasm-build.json: %w", err)
	}

	path := filepath.Join(dataDir, "wasm-build.json")
	return os.WriteFile(path, data, 0o644)
}

// logEffectiveOptLevel makes the build's optimization level visible up
// front. The wasm compile/link flags append cfg.optLevel() AFTER each
// replayed command's own flags, and clang's last-`-O*`-wins rule makes
// the appended level the effective one — so a project whose captured
// commands say -O3 still compiles at the -Oz default unless
// wasm_build.opt_level pins a speed level. That silent demotion cost a
// measured ~5x throughput on an inference-engine build; this log line
// (plus an explicit override note) is the tripwire against repeating it.
func logEffectiveOptLevel(steps []WasmBuildStep, cfg WasmConfig) {
	eff := cfg.optLevel()
	src := "wasm_build.opt_level"
	if cfg.OptLevel == "" {
		src = "default"
	}
	overridden := map[string]int{}
	for _, s := range steps {
		if s.Skipped || s.Type != buildjson.StepCompile {
			continue
		}
		for _, a := range s.Args {
			if len(a) == 3 && strings.HasPrefix(a, "-O") && a != eff {
				overridden[a]++
			}
		}
	}
	fmt.Fprintf(os.Stderr, "[wasm-build] Optimization level: %s (%s)\n", eff, src)
	for _, lvl := range []string{"-O0", "-O1", "-O2", "-O3", "-Os", "-Oz"} {
		if n := overridden[lvl]; n > 0 {
			fmt.Fprintf(os.Stderr, "[wasm-build] NOTE: overriding %s from %d compile command(s) with %s; set wasm_build.opt_level to control this\n", lvl, n, eff)
		}
	}
}
