package buildjson

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ExecuteResult holds the result of executing build.json steps.
type ExecuteResult struct {
	Succeeded int
	Cached    int
	Failed    int
	Total     int
}

// Execute runs all build steps in order: compile → archive → link.
// Each step is executed using its recorded executable, args, and work_dir.
// Returns an error if any step fails.
func Execute(b *BuildJSON) (*ExecuteResult, error) {
	result := &ExecuteResult{Total: len(b.Steps)}

	phases := []StepType{
		StepCompile,
		StepArchive,
		StepLink,
	}

	for _, phase := range phases {
		for _, step := range b.Steps {
			if step.Type != phase {
				continue
			}
			cached, err := executeStep(&step)
			if err != nil {
				result.Failed++
				return result, fmt.Errorf("step %d (%s) failed: %w", step.ID, step.Type, err)
			}
			if cached {
				result.Cached++
			} else {
				result.Succeeded++
			}
		}
	}

	return result, nil
}

// findOutputFiles extracts all output file paths from the step.
// Checks OutputFile, -o flag, -MF flag, and @params files.
func findOutputFiles(step *BuildStep) []string {
	var outputs []string
	if step.OutputFile != "" {
		outputs = append(outputs, step.OutputFile)
	}
	for i, arg := range step.Args {
		if (arg == "-o" || arg == "-MF") && i+1 < len(step.Args) {
			outputs = append(outputs, step.Args[i+1])
		}
		// @params file: parse it for -o flags
		if strings.HasPrefix(arg, "@") {
			paramsFile := arg[1:]
			if !filepath.IsAbs(paramsFile) {
				paramsFile = filepath.Join(step.WorkDir, paramsFile)
			}
			outputs = append(outputs, parseParamsFileOutputs(paramsFile)...)
		}
	}
	return outputs
}

// parseParamsFileOutputs reads a params file (one arg per line) and
// extracts output file paths following -o flags.
func parseParamsFileOutputs(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	var outputs []string
	scanner := bufio.NewScanner(f)
	nextIsOutput := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if nextIsOutput {
			outputs = append(outputs, line)
			nextIsOutput = false
			continue
		}
		if line == "-o" {
			nextIsOutput = true
		}
	}
	return outputs
}

// makeWritable ensures a file is writable.
// Build systems like Bazel mark outputs as read-only, which prevents
// re-execution of build steps.
func makeWritable(path string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	if info.Mode().Perm()&0o200 == 0 {
		_ = os.Chmod(path, info.Mode()|0o200)
	}
}

// filterBuildArgs removes flags that only work inside a build system sandbox.
// Clang modules flags (-fmodules-strict-decluse, -fmodule-map-file, -fmodule-name)
// depend on Bazel's module cache and fail when replayed outside the sandbox.
func filterBuildArgs(args []string) []string {
	var filtered []string
	skip := false
	for i, arg := range args {
		if skip {
			skip = false
			continue
		}
		// -Xclang <next> pairs: check if next arg is a module flag
		if arg == "-Xclang" && i+1 < len(args) {
			next := args[i+1]
			if strings.HasPrefix(next, "-fmodule-map-file=") ||
				strings.HasPrefix(next, "-fmodule-name=") ||
				next == "-fno-cxx-modules" {
				skip = true // skip the -Xclang and the next arg
				continue
			}
		}
		// Direct flags to remove
		if arg == "-fmodules-strict-decluse" ||
			arg == "-Wprivate-header" {
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered
}

// outputsExist checks if all output files already exist.
// Used as a build cache: if outputs are present, skip the step.
func outputsExist(outputs []string, workDir string) bool {
	if len(outputs) == 0 {
		return false
	}
	for _, f := range outputs {
		abs := f
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(workDir, abs)
		}
		if _, err := os.Stat(abs); err != nil {
			return false
		}
	}
	return true
}

func executeStep(step *BuildStep) (cached bool, err error) {
	outputFiles := findOutputFiles(step)

	// Build cache: skip if all outputs already exist
	if outputsExist(outputFiles, step.WorkDir) {
		return true, nil
	}

	// Ensure output directories exist and output files are writable
	for _, outputFile := range outputFiles {
		absOutput := outputFile
		if !filepath.IsAbs(absOutput) {
			absOutput = filepath.Join(step.WorkDir, absOutput)
		}
		dir := filepath.Dir(absOutput)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return false, fmt.Errorf("failed to create output dir: %w", err)
		}
		makeWritable(absOutput)
	}

	executable := step.Executable
	if !filepath.IsAbs(executable) {
		executable = filepath.Join(step.WorkDir, executable)
	}

	args := filterBuildArgs(step.Args)
	cmd := exec.Command(executable, args...)
	cmd.Dir = step.WorkDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	desc := ""
	if len(outputFiles) > 0 {
		desc = outputFiles[0]
	} else if len(step.InputFiles) > 0 {
		desc = step.InputFiles[0]
	}
	fmt.Fprintf(os.Stderr, "[validate-build] %s: %s\n", step.Type, desc)

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "[validate-build] FAILED: %s %s\n",
			step.Executable, strings.Join(step.Args, " "))
		return false, err
	}
	return false, nil
}
