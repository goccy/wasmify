package wasmbuild

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/wasmify/internal/buildjson"
)

// InjectBridgeSteps adds a compile step for api_bridge.cc and links it into all link steps.
// If api_bridge.cc does not exist in bridgeDir, the steps are returned unchanged.
//
// shimSrcs are absolute paths to wasmify's generic host-capability shim
// translation units (see DeployHostShims). They are deployed only when the
// corresponding host capability is opted in, so each one present here is
// compiled with the same flags as the bridge (which carry the matching
// -DWASMIFY_HOST_* define) and linked into every link step. With host
// capabilities off, shimSrcs is empty and no extra object or import is added.
func InjectBridgeSteps(steps []WasmBuildStep, cfg WasmConfig, bridgeDir string, shimSrcs []string) []WasmBuildStep {
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

	// Project root on the include path so the generated api_bridge.cc's
	// project-relative includes (e.g. `#include "py.h"`) resolve. The bridge
	// compiles run with the upstream build's workdir (not the repo root), so
	// this is what lets a hand-written embedding header be found without the
	// caller passing extra -I flags.
	if cfg.ProjectRoot != "" {
		includeFlags = append(includeFlags, "-I", cfg.ProjectRoot)
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

	// Compile and link the project's declared custom bridge implementation
	// sources (wasmify.json bridge.CustomBridgeSources), each from its committed
	// location — no copying into the generated bridge dir. -I the source's own
	// directory so its sibling-relative includes (e.g. py.c's `#include "py.h"`)
	// resolve.
	for _, src := range cfg.CustomBridgeSources {
		base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
		// Prefix the object so a custom bridge source (e.g. foo.cc) can never
		// collide with a PROJECT source of the same basename (foo.c) — both
		// would otherwise derive obj/foo.o, and the later compile clobbers the
		// earlier on disk. That corrupts the archive when order shifts and,
		// worse, masks incremental recompiles: the bridge's obj/foo.o makes the
		// build cache treat the project's own foo.o as already up to date.
		out := filepath.Join(cfg.BuildDir, "obj", "wasmify_bridge_"+base+".o")
		// Force C++ regardless of the source extension: a custom bridge is the
		// C++ counterpart of api_bridge.cc (it bridges the same C++ API), and a
		// project may name it `.c` while it uses C++ constructs.
		srcFlags := append(append([]string(nil), includeFlags...), "-x", "c++", "-I", filepath.Dir(src))
		steps = append(steps, WasmBuildStep{
			Type:       buildjson.StepCompile,
			Executable: filepath.Join(cfg.WasiSDKPath, "bin", "clang++"),
			Args:       buildBridgeCompileArgs(src, out, cfg, srcFlags),
			WorkDir:    workDir,
			OutputFile: out,
			InputFiles: []string{src},
		})
		steps = addBridgeToLink(steps, out)
	}

	// Compile and link wasmify's generic host-capability shims. These are
	// deployed by DeployHostShims only when the matching capability is opted
	// in, so every entry here belongs in the build. Each shim TU is compiled
	// with the same flags as the bridge (carrying the -DWASMIFY_HOST_* define
	// that the shim body is gated on) and its object linked into every link
	// step, so the host-backed socket()/posix_spawn()/... definitions resolve
	// in the final wasm.
	for _, shimSrc := range shimSrcs {
		base := strings.TrimSuffix(filepath.Base(shimSrc), filepath.Ext(shimSrc))
		// Prefix as for custom bridge sources: a project source named e.g.
		// host_sockets.c must not collide with the shim's obj/host_sockets.o.
		shimOutput := filepath.Join(cfg.BuildDir, "obj", "wasmify_shim_"+base+".o")
		shimStep := WasmBuildStep{
			Type:       buildjson.StepCompile,
			Executable: filepath.Join(cfg.WasiSDKPath, "bin", "clang++"),
			Args:       buildBridgeCompileArgs(shimSrc, shimOutput, cfg, includeFlags),
			WorkDir:    workDir,
			OutputFile: shimOutput,
			InputFiles: []string{shimSrc},
		}
		steps = addBridgeToLink(steps, shimOutput)
		steps = append(steps, shimStep)
	}

	// Append any extra linker flags (e.g. -Wl,--wrap=connect for host-provided
	// socket shims) from cfg.ExtraLDFlags to every link step.
	steps = appendExtraLDFlags(steps, cfg)

	for i := range steps {
		steps[i].ID = i + 1
	}

	return steps
}

// appendExtraLDFlags appends cfg.ExtraLDFlags to every (non-skipped) link step.
// Used to pass --wrap and similar linker options the captured upstream build
// doesn't include — e.g. routing libc socket()/connect() to host-provided shims
// in the bridge sources.
func appendExtraLDFlags(steps []WasmBuildStep, cfg WasmConfig) []WasmBuildStep {
	if len(cfg.ExtraLDFlags) == 0 {
		return steps
	}
	for i := range steps {
		if steps[i].Type != buildjson.StepLink || steps[i].Skipped {
			continue
		}
		steps[i].Args = append(steps[i].Args, cfg.ExtraLDFlags...)
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
		// __EMSCRIPTEN__ (to bypass sizeof(void*)==8 static_asserts in project
		// headers) is added by wasmCompileFlags below, unless the build opted
		// out via the NoEmscriptenDefine option (wasi-native projects whose
		// headers have real __EMSCRIPTEN__ branches). A stub
		// <emscripten/version.h> satisfies include checks when it IS set.
	}
	// Add the same wasm compile flags used for all other source files
	args = append(args, wasmCompileFlags(cfg)...)
	args = append(args, includeFlags...)
	// Extra include dirs for hand-written bridge / wrapper sources that live
	// OUTSIDE the captured upstream build (e.g. a project's own embedding
	// header that the generated api_bridge.cc includes). Without this the bridge
	// cannot resolve a wrapper header that is not on any captured -I path.
	for _, dir := range cfg.BridgeExtraIncludes {
		args = append(args, "-I", dir)
	}
	// The host-capability -D macros (WASMIFY_HOST_SOCKETS / _SUBPROCESS) and the
	// -I for the host-include stubs are applied by wasmCompileFlags above, which
	// runs for every wasm-build compile (bridge, shims, and upstream sources
	// alike), so they are not repeated here.
	//
	// Project-declared flags come last (wasmify.json wasm_build.extra_cxxflags).
	// clang resolves a repeated option to its last occurrence, so this is also
	// how a project overrides a wasmify default — notably the -std=c++20 above,
	// which a library whose headers need a newer standard must raise. A bridge
	// including a prebuilt library's headers additionally has to match that
	// library's ABI-affecting flags (-fno-rtti, -fno-exceptions,
	// -fno-sized-deallocation, -fno-aligned-new).
	args = append(args, cfg.ExtraCXXFlags...)
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
