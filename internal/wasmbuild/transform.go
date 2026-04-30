package wasmbuild

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/wasmify/internal/buildjson"
)

// TransformSteps converts all native build steps to wasm32-wasi equivalents.
// After the initial transform, it resolves output filename collisions
// (e.g., multiple bazel targets producing different arena.o files) by
// appending a short hash suffix to colliding names.
func TransformSteps(steps []buildjson.BuildStep, cfg WasmConfig) []WasmBuildStep {
	steps = dropPICDuplicateSteps(steps)
	var result []WasmBuildStep
	for i, step := range steps {
		ws := transformStep(step, cfg)
		ws.ID = i + 1
		ws.OriginalID = step.ID
		// Persist wasm_skip from build.json onto the transformed step so
		// Execute honors the prior decision without re-prompting.
		if step.WasmSkip {
			ws.Skipped = true
			if ws.SkipReason == "" {
				ws.SkipReason = step.WasmSkipReason
			}
		}
		result = append(result, ws)
	}
	resolveOutputCollisions(result)
	scrubPersistedSkips(result)
	return result
}

// dropPICDuplicateSteps removes bazel-on-Linux's redundant PIC variants
// of compile/archive/link steps. Bazel produces both static (`foo.o`,
// `libfoo.a`) and PIC (`foo.pic.o`, `libfoo.pic.a`, `libfoo.so`)
// outputs from each cc_library on Linux; on macOS only the static set
// is recorded in aquery. For wasm32-wasip1 there is no shared-library
// concept, so the PIC variants are always dead duplicates of the
// static ones (same source, same flags after stripping `-fPIC`,
// archived twice). Linking both sets later in LinkLibrary doubles the
// symbols and forces wasm-ld into `--allow-multiple-definition`
// territory — at -Oz the resulting wasm starts behaving differently
// from the darwin-host equivalent (visible in go-googlesql's
// TestSQLFormatter / TestAnalyzeMultiStatements regressions).
//
// Filtering at the build.json level here makes the captured Linux
// build set exactly match what darwin records, so the rest of the
// pipeline runs identically regardless of host.
func dropPICDuplicateSteps(steps []buildjson.BuildStep) []buildjson.BuildStep {
	// Bazel-on-Linux records two variants of every cc_library compile/
	// archive: `foo.o` + `foo.pic.o`, and `libbar.a` (or `libbar.lo`)
	// + `libbar.pic.a` (or `libbar.pic.lo`). The PIC counterparts only
	// matter for the cc_library shared-library link, which has no
	// meaning under wasm32-wasip1. Drop the PIC variant whenever the
	// non-PIC variant is also present so the wasm-build set matches
	// what bazel-on-darwin records (one compile + one archive per
	// source).
	picSuffix := func(s string) (key string, ok bool) {
		const tag = ".pic."
		i := strings.LastIndex(s, tag)
		if i < 0 {
			return "", false
		}
		// Reject if the `.pic.` is not part of the trailing extension
		// (e.g. paths with `.pic.` mid-name and a different ext).
		ext := s[i+len(tag)-1:] // includes leading dot of ext
		// Accept .o / .a / .lo / .so suffixes.
		switch ext {
		case ".o", ".a", ".lo", ".so":
			return s[:i] + ext, true
		}
		return "", false
	}

	hasNonPIC := map[string]bool{}
	for _, s := range steps {
		if s.OutputFile == "" {
			continue
		}
		if _, isPIC := picSuffix(s.OutputFile); isPIC {
			continue
		}
		hasNonPIC[s.OutputFile] = true
	}

	out := steps[:0:0]
	for _, s := range steps {
		switch s.Type {
		case buildjson.StepCompile, buildjson.StepArchive:
			if base, isPIC := picSuffix(s.OutputFile); isPIC && hasNonPIC[base] {
				continue
			}
		case buildjson.StepLink:
			// cc_library shared variant. There is no analog in the
			// final wasm (no dlopen on wasm32-wasip1) and the same
			// code is already in the static archive, so replaying
			// these is wasted disk + can confuse the eventual
			// LinkLibrary merge. Skip lib*.so / lib*.so.<n> outputs.
			b := filepath.Base(s.OutputFile)
			if isCcLibrarySharedOutput(b) {
				continue
			}
		}
		out = append(out, s)
	}
	return out
}

// isCcLibrarySharedOutput reports whether a link step's output is a
// cc_library shared-library artefact (`lib<name>.so` or
// `lib<name>.so.<n>`). cc_binary executables don't carry the `lib`
// prefix, so they fall through and get processed normally.
func isCcLibrarySharedOutput(base string) bool {
	if !strings.HasPrefix(base, "lib") {
		return false
	}
	idx := strings.Index(base, ".so")
	if idx < 0 {
		return false
	}
	rest := base[idx+len(".so"):]
	if rest == "" {
		return true
	}
	if rest[0] != '.' {
		return false
	}
	for _, c := range rest[1:] {
		if (c < '0' || c > '9') && c != '.' {
			return false
		}
	}
	return true
}

// scrubPersistedSkips removes the output files of pre-skipped compile steps
// from downstream archive/link argument lists. applySkip (in execute.go)
// performs the same scrub at runtime when the user accepts an interactive
// "skip?" prompt, but wasm_skip decisions loaded from build.json bypass
// that path — Execute sees Skipped=true and moves on, never touching the
// archive args. Without scrubbing, the archive phase later tries to `ar`
// together `.o` files that were never produced and fails.
func scrubPersistedSkips(steps []WasmBuildStep) {
	for i := range steps {
		if steps[i].Type != buildjson.StepCompile || !steps[i].Skipped {
			continue
		}
		out := steps[i].OutputFile
		if out == "" {
			continue
		}
		for j := range steps {
			s := &steps[j]
			if s.Type != buildjson.StepArchive && s.Type != buildjson.StepLink {
				continue
			}
			s.Args = removeArgValue(s.Args, out)
			s.InputFiles = removeArgValue(s.InputFiles, out)
		}
	}
}

// resolveOutputCollisions detects when multiple compile steps produce the
// same output basename (e.g., obj/arena.o) from different source files,
// and renames the colliding outputs to obj/arena_<hash>.o. Archive steps
// that reference the old name are updated to use the new name.
func resolveOutputCollisions(steps []WasmBuildStep) {
	// First pass: find collisions among compile step outputs
	seen := make(map[string]int) // output path → first step index
	renames := make(map[int]string) // step index → new output path

	for i := range steps {
		s := &steps[i]
		if s.Type != "compile" || s.OutputFile == "" {
			continue
		}
		if prevIdx, exists := seen[s.OutputFile]; exists {
			// Collision! Rename the current step's output.
			// Use the original bazel output path to generate a unique suffix.
			origPath := ""
			for _, arg := range s.Args {
				if strings.HasSuffix(arg, ".cc") || strings.HasSuffix(arg, ".c") {
					origPath = arg
					break
				}
			}
			if origPath == "" {
				origPath = s.OutputFile + "_" + strings.Repeat("x", i)
			}

			// Also rename the first occurrence if not already renamed
			if _, firstRenamed := renames[prevIdx]; !firstRenamed {
				prevOrig := ""
				for _, arg := range steps[prevIdx].Args {
					if strings.HasSuffix(arg, ".cc") || strings.HasSuffix(arg, ".c") {
						prevOrig = arg
						break
					}
				}
				if prevOrig != "" {
					newName := uniqueOutputName(s.OutputFile, prevOrig)
					renames[prevIdx] = newName
				}
			}

			newName := uniqueOutputName(s.OutputFile, origPath)
			renames[i] = newName
		} else {
			seen[s.OutputFile] = i
		}
	}

	if len(renames) == 0 {
		return
	}

	// Build old→new mapping for archive step fixup
	oldToNew := make(map[string]string)
	for idx, newPath := range renames {
		oldPath := steps[idx].OutputFile
		oldToNew[oldPath] = newPath
		steps[idx].OutputFile = newPath
		steps[idx].Args = replaceOutputArg(steps[idx].Args, newPath)
	}

	// Fix up archive steps that reference renamed .o files
	for i := range steps {
		if steps[i].Type != "archive" {
			continue
		}
		for j, arg := range steps[i].Args {
			if newPath, ok := oldToNew[arg]; ok {
				steps[i].Args[j] = newPath
			}
		}
	}
}

// uniqueOutputName generates a collision-free output name by inserting a
// short hash of the source path before the extension.
// E.g., obj/arena.o + "mylib/base/arena.cc" → obj/arena_a3f2.o
func uniqueOutputName(outputPath, sourcePath string) string {
	ext := filepath.Ext(outputPath)
	base := strings.TrimSuffix(outputPath, ext)
	// Use first 8 chars of hex-encoded hash of source path
	h := uint32(0)
	for _, b := range []byte(sourcePath) {
		h = h*31 + uint32(b)
	}
	suffix := strings.ToLower(strings.ReplaceAll(
		strings.ReplaceAll(filepath.Base(filepath.Dir(sourcePath)), "/", "_"),
		"~", ""))
	if len(suffix) > 12 {
		suffix = suffix[:12]
	}
	return base + "_" + suffix + ext
}

func transformStep(step buildjson.BuildStep, cfg WasmConfig) WasmBuildStep {
	switch step.Type {
	case buildjson.StepCompile:
		return transformCompileStep(step, cfg)
	case buildjson.StepLink:
		return transformLinkStep(step, cfg)
	case buildjson.StepArchive:
		return transformArchiveStep(step, cfg)
	default:
		return WasmBuildStep{
			Type:       step.Type,
			Executable: step.Executable,
			Args:       step.Args,
			WorkDir:    step.WorkDir,
			OutputFile: step.OutputFile,
			Skipped:    true,
			SkipReason: "unsupported step type for wasm build",
		}
	}
}

func transformCompileStep(step buildjson.BuildStep, cfg WasmConfig) WasmBuildStep {
	compiler := resolveCompiler(step.Compiler, step.Language, cfg)
	args := filterCompileFlags(step.Args)
	args = append(args, wasmCompileFlags(cfg)...)

	// Rewrite output path
	outputFile := rewriteOutputPath(step.OutputFile, cfg.BuildDir, "obj")

	// Replace -o argument
	args = replaceOutputArg(args, outputFile)

	return WasmBuildStep{
		Type:       buildjson.StepCompile,
		Executable: compiler,
		Args:       args,
		WorkDir:    step.WorkDir,
		OutputFile: outputFile,
		InputFiles: collectInputFiles(args),
	}
}

func transformLinkStep(step buildjson.BuildStep, cfg WasmConfig) WasmBuildStep {
	compiler := resolveCompiler(step.Compiler, step.Language, cfg)
	args := expandResponseFiles(step.Args, step.WorkDir)
	args = filterLinkFlags(args)
	args = filterNativeLibraries(args)
	args = append(args, wasmLinkFlags(cfg)...)

	// Extract output from args if step didn't have an explicit OutputFile
	origOutput := step.OutputFile
	if origOutput == "" {
		origOutput = extractOutputFromArgs(args)
	}

	// Rewrite output path
	outputFile := rewriteOutputPath(origOutput, cfg.BuildDir, "output")
	if outputFile != "" && !strings.HasSuffix(outputFile, ".wasm") {
		outputFile += ".wasm"
	}
	args = replaceOutputArg(args, outputFile)

	// Rewrite input .o and .a paths
	args = rewriteInputPaths(args, cfg.BuildDir)

	return WasmBuildStep{
		Type:       buildjson.StepLink,
		Executable: compiler,
		Args:       args,
		WorkDir:    step.WorkDir,
		OutputFile: outputFile,
		InputFiles: collectInputFiles(args),
	}
}

// filterNativeLibraries removes native toolchain library files (.a) that are
// not from the project build output (e.g., toolchain libc++.a, libunwind.a).
// Only keeps .a files from bazel-out build directories.
func filterNativeLibraries(args []string) []string {
	var result []string
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			result = append(result, arg)
			continue
		}
		ext := strings.ToLower(filepath.Ext(arg))
		if ext == ".a" && isNativeToolchainLib(arg) {
			continue
		}
		result = append(result, arg)
	}
	return result
}

// isNativeToolchainLib returns true if the .a path looks like a native
// toolchain library rather than a project build output.
func isNativeToolchainLib(path string) bool {
	// Native toolchain paths typically contain "toolchain" or "llvm" dirs
	// and NOT "bazel-out" build output paths
	if strings.Contains(path, "toolchains_llvm") || strings.Contains(path, "llvm_toolchain") {
		return true
	}
	// System library paths
	if strings.HasPrefix(path, "/usr/") || strings.HasPrefix(path, "/Library/") {
		return true
	}
	return false
}

// extractOutputFromArgs finds the -o argument value in args.
func extractOutputFromArgs(args []string) string {
	for i, arg := range args {
		if arg == "-o" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func transformArchiveStep(step buildjson.BuildStep, cfg WasmConfig) WasmBuildStep {
	archiver := filepath.Join(cfg.WasiSDKPath, "bin", "llvm-ar")
	args := expandResponseFiles(step.Args, step.WorkDir)
	args, extractedOutput := convertLibtoolToAr(args)

	// Use extractedOutput if the step didn't have an explicit OutputFile
	origOutput := step.OutputFile
	if origOutput == "" {
		origOutput = extractedOutput
	}

	outputFile := rewriteOutputPath(origOutput, cfg.BuildDir, "lib")
	// For llvm-ar, the archive name is positional (args[1] after "rcs").
	// Replace it directly instead of using replaceOutputArg which looks for -o.
	if len(args) >= 2 && args[0] == "rcs" {
		args[1] = outputFile
	}
	args = rewriteInputPaths(args, cfg.BuildDir)

	return WasmBuildStep{
		Type:       buildjson.StepArchive,
		Executable: archiver,
		Args:       args,
		WorkDir:    step.WorkDir,
		OutputFile: outputFile,
		InputFiles: collectInputFiles(args),
	}
}

// expandResponseFiles expands @file arguments by reading the file contents
// and replacing the @file arg with the file's lines.
func expandResponseFiles(args []string, workDir string) []string {
	var result []string
	for _, arg := range args {
		if strings.HasPrefix(arg, "@") {
			path := arg[1:]
			if !filepath.IsAbs(path) {
				path = filepath.Join(workDir, path)
			}
			data, err := os.ReadFile(path)
			if err == nil {
				for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
					line = strings.TrimSpace(line)
					if line != "" {
						result = append(result, line)
					}
				}
				continue
			}
			// If we can't read the file, keep the @file arg as-is
		}
		result = append(result, arg)
	}
	return result
}

// convertLibtoolToAr converts libtool-style args (-static -o <output> <inputs>)
// to llvm-ar style args (rcs -o <output> <inputs>).
// Returns the converted args and the extracted output file path.
func convertLibtoolToAr(args []string) ([]string, string) {
	// Detect libtool format: starts with -static or contains -o flag
	hasStaticOrO := false
	for _, arg := range args {
		if arg == "-static" || arg == "-o" {
			hasStaticOrO = true
			break
		}
	}
	if !hasStaticOrO {
		return args, ""
	}

	// Extract output and input files from libtool args
	var outputFile string
	var inputs []string
	skipNext := false
	for i, arg := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if arg == "-o" && i+1 < len(args) {
			outputFile = args[i+1]
			skipNext = true
			continue
		}
		if arg == "-static" || arg == "-no_warning_for_no_symbols" {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		inputs = append(inputs, arg)
	}

	if outputFile == "" {
		return args, ""
	}

	// Build llvm-ar style args: rcs <output> <inputs...>
	// (llvm-ar uses positional archive name, not -o flag)
	result := []string{"rcs", outputFile}
	result = append(result, inputs...)
	return result, outputFile
}

// resolveCompiler returns the wasi-sdk compiler path for the given language.
func resolveCompiler(compiler, language string, cfg WasmConfig) string {
	if language == "c++" || isCPPCompiler(compiler) {
		return filepath.Join(cfg.WasiSDKPath, "bin", "clang++")
	}
	return filepath.Join(cfg.WasiSDKPath, "bin", "clang")
}

func isCPPCompiler(name string) bool {
	switch name {
	case "c++", "g++", "clang++":
		return true
	}
	return false
}

// filterCompileFlags removes native-specific flags from compile arguments.
func filterCompileFlags(args []string) []string {
	var result []string
	skipNext := false
	for i, arg := range args {
		if skipNext {
			skipNext = false
			continue
		}

		if shouldRemoveCompileFlag(arg) {
			if flagTakesArg(arg) && i+1 < len(args) {
				skipNext = true
			}
			continue
		}

		// Remove flags with values after =
		if shouldRemoveCompileFlagPrefix(arg) {
			continue
		}

		result = append(result, arg)
	}
	return result
}

func shouldRemoveCompileFlag(flag string) bool {
	switch flag {
	case "-pthread",
		"-fcolor-diagnostics",
		"-fPIC", "-fPIE", "-fpic", "-fpie",
		"-MD", "-MP",
		"-fstack-protector", "-fstack-protector-strong", "-fstack-protector-all",
		"-no-canonical-prefixes",
		"-Wprivate-header",
		"-fno-cxx-modules":
		return true
	}
	// Flags that take a separate argument. Host-SDK paths
	// (-isysroot, -cxx-isystem) are macOS-specific when captured
	// from an Apple clang invocation and would point wasi-sdk clang
	// at the wrong sysroot if left in.
	switch flag {
	case "-MF", "-MT", "-MQ", "-Xclang",
		"-isysroot", "-cxx-isystem":
		return true
	}
	return false
}

func shouldRemoveCompileFlagPrefix(flag string) bool {
	prefixes := []string{
		"--target=",
		"--sysroot=",
		"-mmacosx-version-min=",
		"-miphoneos-version-min=",
		"-mios-simulator-version-min=",
		"-fmodule-map-file=",
		"-fmodule-name=",
		"-fmodules-strict-decluse",
		"-fstack-protector",
		"-frandom-seed=",
		"-stdlib=",
		"-MF",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(flag, p) {
			return true
		}
	}
	if strings.HasPrefix(flag, "-arch ") || flag == "-arch" {
		return true
	}
	// Remove x86/ARM-specific machine flags (not supported by wasm)
	if strings.HasPrefix(flag, "-m") && !strings.HasPrefix(flag, "-mmacosx") && !strings.HasPrefix(flag, "-mios") {
		// Keep -mno-* warning flags but remove ISA-specific flags like -maes, -msse4.1, -mavx2, -mfpu=neon
		if len(flag) > 2 && flag[2] != 'n' { // not -mno-*
			// Check if this looks like an architecture-specific flag
			archPrefixes := []string{
				"-maes", "-msse", "-mavx", "-mbmi", "-mfma",
				"-mfpu", "-mneon", "-marm", "-mthumb",
				"-march=", "-mcpu=", "-mtune=",
				"-mpclmul", "-mpopcnt", "-mrdrnd", "-mrdseed",
				"-msha", "-mvaes",
			}
			for _, p := range archPrefixes {
				if strings.HasPrefix(flag, p) {
					return true
				}
			}
		}
	}
	return false
}

func flagTakesArg(flag string) bool {
	switch flag {
	case "-MF", "-MT", "-MQ", "-arch", "-Xclang",
		"-isysroot", "-cxx-isystem":
		return true
	}
	return false
}

// filterLinkFlags removes native-specific flags from link arguments.
func filterLinkFlags(args []string) []string {
	var result []string
	skipNext := false
	for i, arg := range args {
		if skipNext {
			skipNext = false
			continue
		}

		if shouldRemoveLinkFlag(arg) {
			if linkFlagTakesArg(arg) && i+1 < len(args) {
				skipNext = true
			}
			continue
		}

		if shouldRemoveLinkFlagPrefix(arg) {
			continue
		}

		result = append(result, arg)
	}
	return result
}

func shouldRemoveLinkFlag(flag string) bool {
	switch flag {
	case "-pthread",
		"-fPIC", "-fPIE", "-fpic", "-fpie",
		"-pie", "-shared",
		"-rdynamic",
		"-Bstatic", "-Bdynamic",
		"-headerpad_max_install_names",
		"-force_load", "-all_load", "-noall_load":
		return true
	case "-framework", "-rpath":
		return true // these take a next argument
	}
	// Remove native library flags that conflict with wasm
	if flag == "-lunwind" || flag == "-lpthread" || flag == "-lm" || flag == "-ldl" || flag == "-lrt" {
		return true
	}
	return false
}

func linkFlagTakesArg(flag string) bool {
	switch flag {
	case "-framework", "-rpath", "-arch", "-force_load":
		return true
	}
	return false
}

func shouldRemoveLinkFlagPrefix(flag string) bool {
	prefixes := []string{
		"--target=",
		"--sysroot=",
		"-mmacosx-version-min=",
		"-Wl,-rpath",
		"-Wl,-install_name",
		"-Wl,-headerpad",
		"-Wl,-framework",
		"-Wl,-force_load",
		"-Wl,-all_load",
		// Linker flags that wasm-ld rejects with a hard error. Bazel
		// emits these on Linux hosts because its toolchain assumes
		// the system ELF linker; wasm-ld ignores semantically
		// equivalent options or has no use for them. Stripping them
		// is purely a syntactic accommodation and does not affect
		// the produced wasm bytecode.
		"-Wl,--hash-style=",
		"-Wl,--build-id=",
		"-Wl,--build-id",
		"-Wl,--dynamic-linker",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(flag, p) {
			return true
		}
	}
	// Remove native toolchain library paths (not our wasm-build dir)
	if strings.HasPrefix(flag, "-L") {
		return true
	}
	return false
}

// wasmCompileFlags returns the flags to add for wasm compilation.
func wasmCompileFlags(cfg WasmConfig) []string {
	var flags []string
	if cfg.PosixCompatDir != "" {
		// Must come before --sysroot so our headers override wasi-sdk's
		flags = append(flags, "-isystem", cfg.PosixCompatDir)
	}
	flags = append(flags,
		"--target="+cfg.Target,
		"--sysroot="+Sysroot(cfg.WasiSDKPath),
		// __EMSCRIPTEN__ bypasses sizeof(void*)==8 static_asserts, POSIX
		// socket checks, and other host-only code paths in C/C++ projects
		// that guard wasm compatibility behind this macro.
		"-D__EMSCRIPTEN__",
		"-D_WASI_EMULATED_SIGNAL",
		"-D_WASI_EMULATED_PROCESS_CLOCKS",
		"-D_WASI_EMULATED_MMAN",
		"-mllvm", "-wasm-enable-sjlj",
		// Force size-optimal codegen on every per-file compile. The
		// native build (Bazel etc.) usually compiles with -O2;
		// transformCompileStep concatenates wasmCompileFlags AFTER
		// filterCompileFlags(step.Args) so clang's last-`-O*`-wins
		// rule promotes every translation unit to -Oz.
		"-Oz",
	)
	return flags
}

// wasmLinkFlags returns the flags to add for wasm linking.
func wasmLinkFlags(cfg WasmConfig) []string {
	flags := []string{
		"--target=" + cfg.Target,
		"--sysroot=" + Sysroot(cfg.WasiSDKPath),
	}
	if cfg.NoEntry {
		flags = append(flags, "-mexec-model=reactor")
	}
	// Force-export only the runtime utility entry points. The
	// per-method `w_<svc>_<mid>` and `wasmify_get_type_name` exports
	// are emitted by the bridge itself via
	// `__attribute__((export_name(...)))`, so they reach the wasm
	// table without any further --export flag. Together with
	// --gc-sections + --strip-all this lets wasm-ld drop everything
	// no host caller can reach.
	flags = append(flags,
		"-Oz",
		"-Wl,--export=wasm_alloc",
		"-Wl,--export=wasm_free",
		"-Wl,--export=wasm_init",
		"-Wl,--export=_initialize",
		"-Wl,--gc-sections",
		"-Wl,--strip-all",
	)
	if cfg.AllowUndefined {
		flags = append(flags, "-Wl,--allow-undefined")
	}
	// Large wasm projects may need a bigger stack than the default 64KB
	// to hold deeply-nested templated calls and avoid "out of bounds
	// memory access" from stack overflow during bridge dispatch.
	stackSize := cfg.StackSize
	if stackSize <= 0 {
		stackSize = DefaultStackSize
	}
	flags = append(flags, fmt.Sprintf("-Wl,-z,stack-size=%d", stackSize))
	// WASI emulation libraries
	flags = append(flags,
		"-lwasi-emulated-signal",
		"-lwasi-emulated-process-clocks",
		"-lwasi-emulated-mman",
	)
	return flags
}

// rewriteOutputPath maps a native output path to the wasm build directory.
func rewriteOutputPath(originalPath, buildDir, subdir string) string {
	if originalPath == "" {
		return ""
	}
	base := filepath.Base(originalPath)
	ext := filepath.Ext(base)
	// For .o files, derive a unique prefix from the parent directory to
	// avoid collisions when multiple bazel targets produce the same
	// basename (e.g., parser.o from project/parser vs protobuf/compiler).
	// The prefix is the last meaningful directory segment of the original
	// bazel output path (e.g., "parser" from "_objs/parser/parser.o",
	// "importer" from "_objs/importer/parser.o").
	if ext == ".o" && subdir == "obj" {
		stem := strings.TrimSuffix(base, ext)
		// Walk up the directory tree to find a disambiguating segment.
		// Bazel outputs are like: bazel-out/.../project/parser/_objs/parser/parser.o
		// The _objs/<target>/ directory is often the same as the filename,
		// so we go one more level up to get the library name (e.g., "parser"
		// from "project/parser").
		dir := filepath.Dir(originalPath)
		parentDir := filepath.Base(dir)
		if parentDir == stem || parentDir == "_objs" {
			// Go one more level up
			grandParent := filepath.Base(filepath.Dir(dir))
			if grandParent == "_objs" {
				grandParent = filepath.Base(filepath.Dir(filepath.Dir(dir)))
			}
			if grandParent != "" && grandParent != "." && grandParent != stem {
				parentDir = grandParent
			}
		}
		if parentDir != "" && parentDir != "." && parentDir != stem {
			return filepath.Join(buildDir, subdir, stem+"_"+sanitizeDirName(parentDir)+ext)
		}
	}
	// For .a / .lo archives, bazel emits one archive per cc_library
	// target named lib<target>.a in the package's output directory.
	// Sibling cc_libraries in different packages can have the same
	// target name (e.g. `googlesql/public/libtype.a` from cc_library
	// "type" alongside `googlesql/public/types/libtype.a` from a
	// different cc_library also named "type"). Collapsing to the
	// basename here would silently overwrite one with the other in
	// `<buildDir>/lib/`, dropping every .o the loser archived. Encode
	// the package directory into the filename so co-located archives
	// from distinct bazel packages survive side by side.
	if (ext == ".a" || ext == ".lo") && subdir == "lib" {
		stem := strings.TrimSuffix(base, ext)
		parentDir := filepath.Base(filepath.Dir(originalPath))
		if parentDir != "" && parentDir != "." && parentDir != stem {
			return filepath.Join(buildDir, subdir, stem+"__"+sanitizeDirName(parentDir)+ext)
		}
	}
	return filepath.Join(buildDir, subdir, base)
}

func sanitizeDirName(name string) string {
	// Replace non-alphanumeric chars with underscore, keep short
	var buf []byte
	for _, c := range []byte(name) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			buf = append(buf, c)
		} else {
			buf = append(buf, '_')
		}
	}
	s := string(buf)
	if len(s) > 20 {
		s = s[:20]
	}
	return s
}

// replaceOutputArg replaces the -o argument value in args.
func replaceOutputArg(args []string, newOutput string) []string {
	result := make([]string, len(args))
	copy(result, args)
	for i, arg := range result {
		if arg == "-o" && i+1 < len(result) {
			result[i+1] = newOutput
			return result
		}
	}
	return result
}

// collectInputFiles extracts source and object file paths from args for cache tracking.
func collectInputFiles(args []string) []string {
	var inputs []string
	skipNext := false
	for i, arg := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if arg == "-o" && i+1 < len(args) {
			skipNext = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(arg))
		switch ext {
		case ".c", ".cc", ".cpp", ".cxx", ".o", ".a", ".s", ".h", ".hpp":
			inputs = append(inputs, arg)
		}
	}
	return inputs
}

// rewriteInputPaths rewrites .o and .a file references in args to point to the wasm build dir.
func rewriteInputPaths(args []string, buildDir string) []string {
	result := make([]string, len(args))
	for i, arg := range args {
		if strings.HasPrefix(arg, "-") {
			result[i] = arg
			continue
		}
		ext := strings.ToLower(filepath.Ext(arg))
		switch ext {
		case ".o":
			// Use the same collision-safe naming as rewriteOutputPath
			result[i] = rewriteOutputPath(arg, buildDir, "obj")
		case ".a", ".lo":
			// Same collision-safe naming as rewriteOutputPath so that
			// archive references in link/archive command lines resolve
			// to the disambiguated paths produced for archive outputs.
			result[i] = rewriteOutputPath(arg, buildDir, "lib")
		default:
			result[i] = arg
		}
	}
	return result
}
