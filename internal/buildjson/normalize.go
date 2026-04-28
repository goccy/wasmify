package buildjson

import (
	"path/filepath"
	"strings"

	"github.com/goccy/wasmify/internal/wrapper"
)

// compilerNames maps executable base names to whether they are compilers.
var compilerNames = map[string]bool{
	"cc": true, "c++": true,
	"gcc": true, "g++": true,
	"clang": true, "clang++": true,
}

var cppCompilers = map[string]bool{
	"c++": true, "g++": true, "clang++": true,
}

// Normalize converts raw log entries into structured BuildSteps.
func Normalize(entries []wrapper.LogEntry) []BuildStep {
	var steps []BuildStep
	for i, entry := range entries {
		step := BuildStep{
			ID:         i + 1,
			Executable: entry.Executable,
			Args:       entry.Args,
			WorkDir:    entry.WorkDir,
		}

		baseName := filepath.Base(entry.Tool)
		step.Type = classifyStep(baseName, entry.Args)
		step.Compiler = baseName

		if compilerNames[baseName] {
			step.Language = detectLanguage(baseName, entry.Args)
		}

		step.Flags = parseFlags(entry.Args)
		step.InputFiles = extractInputFiles(entry.Args)
		step.OutputFile = extractOutputFile(entry.Args)

		if step.Type == StepArchive {
			// `ar <mode> <archive> <members...>` — the archive name is
			// positional (first non-flag arg after the mode string) and
			// is NOT listed with -o. Rebuild input_files/output_file
			// around that convention.
			if out := extractArArchive(entry.Args); out != "" {
				step.OutputFile = out
				// extractInputFiles includes the archive itself as an
				// input because it looks like a .a file. Remove it.
				filtered := step.InputFiles[:0]
				for _, f := range step.InputFiles {
					if f == out {
						continue
					}
					filtered = append(filtered, f)
				}
				step.InputFiles = filtered
			}
		}

		steps = append(steps, step)
	}
	return steps
}

// extractArArchive returns the archive name from `ar` style args.
// `ar` uses positional args: [mode, archive, members...]. The first arg
// is a mode string like "rcs" / "rv" / "cr" composed of mode letters.
// Mode letters are a-z / A-Z only; no path separators or dots.
func extractArArchive(args []string) string {
	if len(args) == 0 {
		return ""
	}
	start := 0
	if isArMode(args[0]) {
		start = 1
	}
	for i := start; i < len(args); i++ {
		a := args[i]
		if a == "" || strings.HasPrefix(a, "-") {
			continue
		}
		return a
	}
	return ""
}

func isArMode(s string) bool {
	if s == "" || len(s) > 6 {
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

func classifyStep(tool string, args []string) StepType {
	switch tool {
	case "ar", "ranlib":
		return StepArchive
	case "ld":
		return StepLink
	case "strip":
		return StepOther
	}

	if compilerNames[tool] {
		for _, arg := range args {
			if arg == "-c" {
				return StepCompile
			}
		}
		// No -c flag means linking
		return StepLink
	}

	return StepOther
}

func detectLanguage(tool string, args []string) string {
	// Check -x flag first
	for i, arg := range args {
		if arg == "-x" && i+1 < len(args) {
			switch args[i+1] {
			case "c":
				return "c"
			case "c++":
				return "c++"
			case "assembler", "assembler-with-cpp":
				return "asm"
			}
		}
	}

	// Check file extensions
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		switch strings.ToLower(filepath.Ext(arg)) {
		case ".c":
			return "c"
		case ".cpp", ".cc", ".cxx", ".c++":
			return "c++"
		case ".s", ".asm":
			return "asm"
		}
	}

	// Fall back to tool name
	if cppCompilers[tool] {
		return "c++"
	}
	return "c"
}

func parseFlags(args []string) BuildFlags {
	var flags BuildFlags
	for i := 0; i < len(args); i++ {
		arg := args[i]

		switch {
		case strings.HasPrefix(arg, "-I"):
			path := strings.TrimPrefix(arg, "-I")
			if path == "" && i+1 < len(args) {
				i++
				path = args[i]
			}
			flags.IncludePaths = append(flags.IncludePaths, path)

		case strings.HasPrefix(arg, "-D"):
			def := strings.TrimPrefix(arg, "-D")
			if def == "" && i+1 < len(args) {
				i++
				def = args[i]
			}
			flags.Defines = append(flags.Defines, def)

		case strings.HasPrefix(arg, "-l"):
			lib := strings.TrimPrefix(arg, "-l")
			if lib == "" && i+1 < len(args) {
				i++
				lib = args[i]
			}
			flags.LinkLibs = append(flags.LinkLibs, lib)

		case strings.HasPrefix(arg, "-L"):
			path := strings.TrimPrefix(arg, "-L")
			if path == "" && i+1 < len(args) {
				i++
				path = args[i]
			}
			flags.LibPaths = append(flags.LibPaths, path)

		case strings.HasPrefix(arg, "-std="):
			flags.Standard = strings.TrimPrefix(arg, "-std=")

		case strings.HasPrefix(arg, "-O"):
			flags.Optimization = strings.TrimPrefix(arg, "-O")

		case strings.HasPrefix(arg, "-W"):
			flags.Warnings = append(flags.Warnings, strings.TrimPrefix(arg, "-W"))

		case arg == "-o":
			i++ // Skip output file (handled by extractOutputFile)

		case arg == "-c":
			// Skip, handled by classifyStep

		default:
			if strings.HasPrefix(arg, "-") && !isSourceFile(arg) {
				flags.OtherFlags = append(flags.OtherFlags, arg)
			}
		}
	}
	return flags
}

func extractOutputFile(args []string) string {
	for i, arg := range args {
		if arg == "-o" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func extractInputFiles(args []string) []string {
	var files []string
	skipNext := false
	for _, arg := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if arg == "-o" || arg == "-I" || arg == "-D" || arg == "-L" ||
			arg == "-l" || arg == "-x" || arg == "-isystem" ||
			arg == "-MF" || arg == "-MQ" || arg == "-MT" {
			skipNext = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if isSourceFile(arg) || isObjectFile(arg) {
			files = append(files, arg)
		}
	}
	return files
}

func isSourceFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".c", ".cpp", ".cc", ".cxx", ".c++", ".s", ".asm", ".m", ".mm":
		return true
	}
	return false
}

func isObjectFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".o", ".obj", ".a", ".so", ".dylib":
		return true
	}
	return false
}
