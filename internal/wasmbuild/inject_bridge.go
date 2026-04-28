package wasmbuild

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/wasmify/internal/buildjson"
)

// InjectBridgeSteps adds a compile step for api_bridge.cc and links it into all link steps.
// If api_bridge.cc does not exist in bridgeDir, the steps are returned unchanged.
func InjectBridgeSteps(steps []WasmBuildStep, cfg WasmConfig, bridgeDir string) []WasmBuildStep {
	bridgeSrc := filepath.Join(bridgeDir, "api_bridge.cc")
	if _, err := os.Stat(bridgeSrc); err != nil {
		return steps
	}

	includeFlags := extractIncludeFlags(steps)
	workDir := extractWorkDir(steps)

	// Ensure the work directory itself is on the include path.
	// `-iquote .` lets `#include "foo.h"` find files relative to workDir,
	// which is needed when the project's compile flags don't explicitly
	// add the source root (common in simple Makefile builds).
	hasWorkDirInclude := false
	for i := 0; i < len(includeFlags); i++ {
		if includeFlags[i] == "-iquote" && i+1 < len(includeFlags) && includeFlags[i+1] == "." {
			hasWorkDirInclude = true
			break
		}
	}
	if !hasWorkDirInclude {
		includeFlags = append([]string{"-iquote", "."}, includeFlags...)
	}

	bridgeOutput := filepath.Join(cfg.BuildDir, "obj", "api_bridge.o")
	compileStep := WasmBuildStep{
		Type:       buildjson.StepCompile,
		Executable: filepath.Join(cfg.WasiSDKPath, "bin", "clang++"),
		Args:       buildBridgeCompileArgs(bridgeSrc, bridgeOutput, cfg, includeFlags),
		WorkDir:    workDir,
		OutputFile: bridgeOutput,
		InputFiles: []string{bridgeSrc},
	}

	// Add bridge object to all link steps
	steps = addBridgeToLink(steps, bridgeOutput)

	// Append compile step and reindex
	steps = append(steps, compileStep)

	// Also compile and link custom_bridge.cc if it exists alongside api_bridge.cc.
	// custom_bridge.cc provides hand-written bridge functions for cases the
	// auto-generator can't handle (e.g., virtual methods on abstract classes,
	// functions not in the api-spec).
	customSrc := filepath.Join(bridgeDir, "custom_bridge.cc")
	if _, err := os.Stat(customSrc); err == nil {
		customOutput := filepath.Join(cfg.BuildDir, "obj", "custom_bridge.o")
		customStep := WasmBuildStep{
			Type:       buildjson.StepCompile,
			Executable: filepath.Join(cfg.WasiSDKPath, "bin", "clang++"),
			Args:       buildBridgeCompileArgs(customSrc, customOutput, cfg, includeFlags),
			WorkDir:    workDir,
			OutputFile: customOutput,
			InputFiles: []string{customSrc},
		}
		steps = addBridgeToLink(steps, customOutput)
		steps = append(steps, customStep)
	}

	for i := range steps {
		steps[i].ID = i + 1
	}

	return steps
}

// extractIncludeFlags collects unique -I and -isystem flags from existing compile steps.
func extractIncludeFlags(steps []WasmBuildStep) []string {
	seen := make(map[string]bool)
	var flags []string

	for _, step := range steps {
		if step.Type != buildjson.StepCompile || step.Skipped {
			continue
		}
		for i, arg := range step.Args {
			switch {
			case arg == "-I" || arg == "-isystem" || arg == "-iquote":
				if i+1 < len(step.Args) {
					pair := arg + "\x00" + step.Args[i+1]
					if !seen[pair] {
						seen[pair] = true
						flags = append(flags, arg, step.Args[i+1])
					}
				}
			case strings.HasPrefix(arg, "-I"):
				if !seen[arg] {
					seen[arg] = true
					flags = append(flags, arg)
				}
			case strings.HasPrefix(arg, "-isystem") || strings.HasPrefix(arg, "-iquote"):
				if !seen[arg] {
					seen[arg] = true
					flags = append(flags, arg)
				}
			}
		}
	}

	return flags
}

func buildBridgeCompileArgs(srcFile, outputFile string, cfg WasmConfig, includeFlags []string) []string {
	args := []string{
		"-c",
		"-std=c++20",
		// -DNDEBUG MUST match the library's setting. Many C++ classes — e.g.
		// googlesql::IdString — add debug-only member fields and methods
		// guarded by `#ifndef NDEBUG`. If the bridge sees the debug layout
		// while the library archive was compiled in release, sizeof()
		// differs and the wasm ABI for every function that takes/returns
		// such a class diverges, triggering wasm-ld "signature mismatch"
		// stubs that trap with `unreachable` during _initialize's static
		// constructor chain. Projects building with `bazel -c opt` (the
		// common case for wasm targets) get -DNDEBUG, so the bridge does
		// the same. If your build truly wants debug mode, set
		// WASMIFY_BRIDGE_DEBUG=1 in the env.
		"-DNDEBUG",
		// Define __EMSCRIPTEN__ to bypass sizeof(void*)==8 static_asserts in
		// project headers that guard their 8-byte-pointer assumptions behind
		// this macro. A stub <emscripten/version.h> is deployed to satisfy
		// include checks when this macro is set.
		"-D__EMSCRIPTEN__",
	}
	// Add the same wasm compile flags used for all other source files
	args = append(args, wasmCompileFlags(cfg)...)
	args = append(args, includeFlags...)
	args = append(args, "-o", outputFile, srcFile)
	return args
}

// extractWorkDir returns the work directory from the first non-skipped compile step.
func extractWorkDir(steps []WasmBuildStep) string {
	for _, step := range steps {
		if step.Type == buildjson.StepCompile && !step.Skipped && step.WorkDir != "" {
			return step.WorkDir
		}
	}
	return "."
}

func addBridgeToLink(steps []WasmBuildStep, bridgeObj string) []WasmBuildStep {
	for i := range steps {
		if steps[i].Type != buildjson.StepLink || steps[i].Skipped {
			continue
		}
		steps[i].Args = append(steps[i].Args, bridgeObj)
		steps[i].InputFiles = append(steps[i].InputFiles, bridgeObj)
	}
	return steps
}
