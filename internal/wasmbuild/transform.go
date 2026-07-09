package wasmbuild

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/wasmify/internal/buildjson"
)

// TransformSteps converts all native build steps to wasm32-wasi equivalents.
// Outputs are laid out under obj//lib/ mirroring each step's source directory
// (see rewriteOutputPath), so same-basename objects/archives from different
// source trees never collide. resolveObjectRefs then re-points every object/
// archive reference in archive and link steps to the actual (nested) output
// that produced it.
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
	resolveObjectRefs(result)
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
// cc_library shared-library artifact (`lib<name>.so` or
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

// resolveObjectRefs re-points every .o / .a / .lo reference in archive and link
// steps to the ACTUAL output that produced it. Outputs are laid out under
// obj//lib/ mirroring their source directory (rewriteOutputPath), but a step
// may reference an object/archive built in a DIFFERENT work dir — e.g. a
// top-level libcore.a (archived in the project root) pulling in a plugin.o
// compiled under a plugins/ subdir — so a reference cannot be nested from the
// REFERENCING step's work dir alone. Resolve each through a basename →
// produced-output registry; when a basename was produced by more than one work
// dir (two compress.o), pick the output whose producing work dir matches the
// referencing step's.
func resolveObjectRefs(steps []WasmBuildStep) {
	type produced struct{ workDir, path string }
	reg := map[string][]produced{}
	compileOutByWD := map[string][]string{} // work dir → compile output paths
	referenced := map[string]bool{}         // basenames referenced by archive/link steps
	for i := range steps {
		s := &steps[i]
		if (s.Type == buildjson.StepCompile || s.Type == buildjson.StepArchive) && s.OutputFile != "" {
			b := filepath.Base(s.OutputFile)
			reg[b] = append(reg[b], produced{s.WorkDir, s.OutputFile})
		}
		if s.Type == buildjson.StepCompile && s.OutputFile != "" {
			compileOutByWD[s.WorkDir] = append(compileOutByWD[s.WorkDir], s.OutputFile)
		}
		if s.Type == buildjson.StepArchive || s.Type == buildjson.StepLink {
			for _, a := range s.Args {
				if !strings.HasPrefix(a, "-") && strings.ToLower(filepath.Ext(a)) == ".o" {
					referenced[filepath.Base(a)] = true
				}
			}
		}
	}
	resolve := func(arg, workDir string) string {
		cands := reg[filepath.Base(arg)]
		switch len(cands) {
		case 1:
			return cands[0].path
		default:
			if len(cands) > 1 {
				for _, c := range cands {
					if c.workDir == workDir {
						return c.path
					}
				}
				return cands[0].path // best effort when no same-work-dir producer
			}
		}
		// No producer for this basename. Some build systems rename an object
		// after compiling it via a shell step wasmify does not wrap — e.g.
		// MakeMaker compiles FooBar.c to FooBar.o and then `mv FooBar.o Bar.o`,
		// so the archive references Bar.o but only FooBar.o was recorded. If the
		// referencing work dir has exactly ONE compile output that no archive/
		// link references, it is that renamed-away object; resolve to it.
		if strings.ToLower(filepath.Ext(arg)) == ".o" {
			var orphans []string
			for _, out := range compileOutByWD[workDir] {
				if !referenced[filepath.Base(out)] {
					orphans = append(orphans, out)
				}
			}
			if len(orphans) == 1 {
				return orphans[0]
			}
		}
		return arg // external/system library, or ambiguous — leave as-is
	}
	for i := range steps {
		s := &steps[i]
		if s.Type != buildjson.StepArchive && s.Type != buildjson.StepLink {
			continue
		}
		for j, arg := range s.Args {
			if strings.HasPrefix(arg, "-") {
				continue
			}
			switch strings.ToLower(filepath.Ext(arg)) {
			case ".o", ".a", ".lo":
				s.Args[j] = resolve(arg, s.WorkDir)
			}
		}
	}
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

	// Rewrite output path. Some build systems compile with an IMPLICIT object
	// output — e.g. a hand-written Makefile runs `cc -c foo.c`, which defaults
	// the object to foo.o in the work dir — so step.OutputFile is empty. Derive
	// the object name from the source in that case; otherwise the object lands
	// in the work dir while the ARCHIVE step's inputs ARE rewritten into obj/,
	// and the archive fails with "foo.o: No such file".
	origOutput := step.OutputFile
	if origOutput == "" {
		origOutput = deriveObjectOutput(step.Args)
	}
	outputFile := rewriteOutputPath(origOutput, cfg.BuildDir, "obj", step.WorkDir, cfg.ProjectRoot)

	// Set -o to the obj/ path, ADDING it when the original compile had none.
	args = ensureOutputArg(args, outputFile)

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
	outputFile := rewriteOutputPath(origOutput, cfg.BuildDir, "output", step.WorkDir, cfg.ProjectRoot)
	if outputFile != "" && !strings.HasSuffix(outputFile, ".wasm") {
		outputFile += ".wasm"
	}
	args = replaceOutputArg(args, outputFile)

	// Rewrite input .o and .a paths
	args = rewriteInputPaths(args, cfg.BuildDir, step.WorkDir, cfg.ProjectRoot)

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
	// ranlib (index-only) is a distinct tool from ar. Some Makefiles run
	// `ar rc libfoo.a *.o` and then `ranlib libfoo.a` as SEPARATE steps, and
	// normalize.go classifies both as archive. Route ranlib to llvm-ranlib —
	// feeding its lone archive operand to llvm-ar just prints ar's usage and
	// fails.
	if filepath.Base(step.Compiler) == "ranlib" {
		return transformRanlibStep(step, cfg)
	}

	archiver := filepath.Join(cfg.WasiSDKPath, "bin", "llvm-ar")
	args := expandResponseFiles(step.Args, step.WorkDir)
	args, extractedOutput := convertLibtoolToAr(args)

	// Use extractedOutput if the step didn't have an explicit OutputFile
	origOutput := step.OutputFile
	if origOutput == "" {
		origOutput = extractedOutput
	}

	outputFile := rewriteOutputPath(origOutput, cfg.BuildDir, "lib", step.WorkDir, cfg.ProjectRoot)
	// For llvm-ar, the archive name is positional, right after the operation
	// word (rc / rcs / cr / crus / ...). Some projects use `ar rc`, not the
	// previously hard-coded "rcs", so match any ar operation word.
	if len(args) >= 2 && isArOperation(args[0]) {
		args[1] = outputFile
	}
	args = rewriteInputPaths(args, cfg.BuildDir, step.WorkDir, cfg.ProjectRoot)

	return WasmBuildStep{
		Type:       buildjson.StepArchive,
		Executable: archiver,
		Args:       args,
		WorkDir:    step.WorkDir,
		OutputFile: outputFile,
		InputFiles: collectInputFiles(args),
	}
}

// transformRanlibStep maps a `ranlib <archive>` step onto llvm-ranlib, pointing
// it at the archive where the ar step wrote it (lib/). An empty index pass is
// harmless, so this need not be skipped even when llvm-ar already wrote a
// symbol table.
func transformRanlibStep(step buildjson.BuildStep, cfg WasmConfig) WasmBuildStep {
	ranlib := filepath.Join(cfg.WasiSDKPath, "bin", "llvm-ranlib")
	args := make([]string, len(step.Args))
	copy(args, step.Args)
	outputFile := ""
	for i, a := range args {
		if strings.HasSuffix(strings.ToLower(a), ".a") {
			args[i] = rewriteOutputPath(a, cfg.BuildDir, "lib", step.WorkDir, cfg.ProjectRoot)
			outputFile = args[i]
		}
	}
	return WasmBuildStep{
		Type:       buildjson.StepArchive,
		Executable: ranlib,
		Args:       args,
		WorkDir:    step.WorkDir,
		OutputFile: outputFile,
	}
}

// isArOperation reports whether an argument is an ar operation word — the
// leading positional key like "rc", "rcs", "cr", "crus", "q", "t". It is a
// non-flag token (no leading '-') composed solely of ar operation/modifier
// letters, with at least one of the operations r/c/q/d/m/p/s/t/x present.
func isArOperation(arg string) bool {
	if arg == "" || strings.HasPrefix(arg, "-") {
		return false
	}
	hasOp := false
	for _, c := range arg {
		switch c {
		case 'r', 'c', 'q', 'd', 'm', 'p', 's', 't', 'x':
			hasOp = true
		case 'u', 'v', 'a', 'b', 'i', 'o', 'l', 'N', 'P', 'T', 'D', 'U', 'S':
			// modifiers — allowed, but not sufficient on their own
		default:
			return false
		}
	}
	return hasOp
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
	// Host-capability opt-ins applied to EVERY wasm-build compile (not just the
	// bridge), so the upstream's own sources enable the matching feature at
	// wasm-build only. The upstream's separate (host-arch-equivalent) make phase
	// never sees these, so it stays plain and needs none of the extra headers.
	if cfg.HostSockets {
		flags = append(flags, "-DWASMIFY_HOST_SOCKETS")
	}
	if cfg.HostSubprocess {
		flags = append(flags, "-DWASMIFY_HOST_SUBPROCESS")
		if cfg.HostIncludeDir != "" {
			// Carries spawn.h/sys/wait.h that wasi-libc omits; gated behind the
			// macro above so its presence is inert when the feature is off.
			flags = append(flags, "-I", cfg.HostIncludeDir)
		}
	}
	flags = append(flags,
		"--target="+cfg.Target,
		"--sysroot="+Sysroot(cfg.WasiSDKPath),
	)
	// __EMSCRIPTEN__ bypasses sizeof(void*)==8 static_asserts, POSIX
	// socket checks, and other host-only code paths in C/C++ projects
	// that guard wasm compatibility behind this macro. It is HARMFUL for
	// projects that natively support wasm32-wasi and have real
	// `#ifdef __EMSCRIPTEN__` branches (e.g. a source file includes
	// <emscripten/stack.h> under it). Such wasi-native projects set the
	// NoEmscriptenDefine option (also settable via WASMIFY_NO_EMSCRIPTEN_DEFINE)
	// to keep their wasi code paths.
	if !cfg.NoEmscriptenDefine {
		flags = append(flags, "-D__EMSCRIPTEN__")
	}
	flags = append(flags,
		"-D_WASI_EMULATED_SIGNAL",
		"-D_WASI_EMULATED_PROCESS_CLOCKS",
		"-D_WASI_EMULATED_MMAN",
		"-mllvm", "-wasm-enable-sjlj",
		// Disable type-based-alias-analysis (TBAA) optimizations. This is a
		// conservative correctness flag: it can only PREVENT miscompiles, never
		// introduce them, at the cost of a little optimization on
		// aliasing-heavy code. Many mature C/C++ codebases type-pun pointers in
		// ways that technically violate C's strict-aliasing rule — treating a
		// family of distinct struct pointer types as interchangeable, e.g.
		// `p = (A**)&b; *p` where the slot holds a `B*` — and such projects MUST
		// be built with this flag; their own build systems routinely add it for
		// gcc/clang. Without it, at -O2+/-Oz TBAA lets Dead-Store-Elimination
		// delete a store it wrongly proves non-aliasing (e.g. the write that
		// initialises a stack slot whose address is later read back through a
		// differently-typed pointer), producing wrong code. Applying it to every
		// wasm compile keeps the output faithful to what these projects expect.
		"-fno-strict-aliasing",
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
	)
	// Strip the name section for a smaller shipping wasm, unless the project
	// opted to keep symbols (wasmify.json wasm_build.keep_symbols) for
	// debugging / symbolication.
	if !cfg.KeepSymbols {
		flags = append(flags, "-Wl,--strip-all")
	}
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
//
// The function is idempotent: if `originalPath` already lives directly
// under `<buildDir>/<subdir>/` it is returned as-is. This matters for
// archive steps, where the output path is set on `args[1]` and then the
// whole arg list is fed through `rewriteInputPaths`. Without the
// idempotency guard the second pass would re-disambiguate the already-
// disambiguated archive name and append a spurious `__lib` suffix
// (e.g. `libtype__public.a` -> `libtype__public__lib.a`).
func rewriteOutputPath(originalPath, buildDir, subdir, workDir, projectRoot string) string {
	if originalPath == "" {
		return ""
	}
	prefix := filepath.Join(buildDir, subdir) + string(filepath.Separator)
	if strings.HasPrefix(originalPath, prefix) {
		return originalPath
	}
	base := filepath.Base(originalPath)
	ext := filepath.Ext(base)
	// Target isolation: when the step ran in a work dir that lives under the
	// project root, mirror that directory under obj//lib/. Two source trees
	// that produce the same object/archive basename (e.g. a compress.c in each
	// of two codec dirs, or a Util.a from each of two modules) then land in
	// separate subdirectories and cannot clobber each other — no name hashing
	// needed. rewriteOutputPath is a pure function of (originalPath, workDir),
	// and the archive that consumes an object runs in the SAME work dir as the
	// compile that produced it, so the object's create-output and its
	// archive-input reference map to the same nested path.
	if subdir == "obj" || subdir == "lib" {
		if ns := workDirNamespace(workDir, projectRoot); ns != "" {
			return filepath.Join(buildDir, subdir, ns, base)
		}
	}
	// For .o files, derive a unique prefix from the parent directory to
	// avoid collisions when multiple bazel targets produce the same
	// basename (e.g., parser.o from project/parser vs protobuf/compiler).
	// The prefix is the last meaningful directory segment of the original
	// bazel output path (e.g., "parser" from "_objs/parser/parser.o",
	// "importer" from "_objs/importer/parser.o").
	if ext == ".o" && subdir == "obj" {
		stem := strings.TrimSuffix(base, ext)
		// Bazel writes per-target object files at
		//   bazel-out/.../<package>/_objs/<target>/<source>.o
		// Two distinct cc_library targets can share the same target
		// subdirectory name (e.g. `googlesql/public/_objs/type_proto/`
		// and `external/protobuf~/src/google/protobuf/_objs/type_proto/`
		// both contain a `type.pb.o`). Compose the suffix from the
		// target subdir AND its containing package so siblings in
		// different bazel packages survive side by side.
		dir := filepath.Dir(originalPath)
		parentDir := filepath.Base(dir)                 // typically the cc_library target name
		grandParent := filepath.Base(filepath.Dir(dir)) // either "_objs" or, when no _objs, the package
		if grandParent == "_objs" {
			grandParent = filepath.Base(filepath.Dir(filepath.Dir(dir)))
		}
		// Build the suffix in increasing specificity. parentDir alone is
		// the most common, but if it equals the stem (a redundant repeat
		// like "_objs/parser/parser.o") we lean on the grandparent
		// instead. When both are meaningful and distinct, join them so
		// the suffix is unique even for two targets named alike under
		// different packages.
		var suffix string
		switch {
		case parentDir == stem || parentDir == "_objs":
			if grandParent != "" && grandParent != "." && grandParent != stem {
				suffix = grandParent
			}
		case grandParent != "" && grandParent != "." && grandParent != stem && grandParent != parentDir:
			suffix = parentDir + "_" + grandParent
		default:
			if parentDir != "" && parentDir != "." && parentDir != stem {
				suffix = parentDir
			}
		}
		if suffix != "" {
			return filepath.Join(buildDir, subdir, stem+"_"+sanitizeDirName(suffix)+ext)
		}
	}
	// For .a / .lo archives whose work dir was NOT under the project root (so
	// the work-dir nesting above did not apply — e.g. a build sandbox whose
	// exec root sits elsewhere), fall back to mirroring the archive's OWN
	// source directory structure under lib/. Two archives with the same
	// basename from different source trees (googlesql/public/libtype.a vs
	// googlesql/public/types/libtype.a) then keep distinct nested paths and
	// cannot clobber each other, still without name hashing. LinkLibrary globs
	// lib/ recursively, so the exact names never matter to linking — only their
	// uniqueness does.
	if (ext == ".a" || ext == ".lo") && subdir == "lib" {
		if ns := structuralNamespace(filepath.Dir(originalPath)); ns != "" {
			return filepath.Join(buildDir, subdir, ns, base)
		}
	}
	return filepath.Join(buildDir, subdir, base)
}

// workDirNamespace returns the step's work dir relative to the project root
// when it is a clean descendant of it (e.g. "src/mylib"), or ""
// when the work dir is the root itself, escapes the root (starts with ".."),
// or either path is unset. It is the per-target output namespace: outputs from
// distinct source directories mirror that structure under obj//lib/ and so
// never collide by basename.
func workDirNamespace(workDir, projectRoot string) string {
	if workDir == "" || projectRoot == "" {
		return ""
	}
	rel, err := filepath.Rel(projectRoot, workDir)
	if err != nil || rel == "." || rel == "" || strings.HasPrefix(rel, "..") {
		return ""
	}
	return rel
}

// structuralNamespace turns a source directory into a clean, hash-free nested
// namespace by dropping empty / "." / ".." and volume-root segments and
// sanitizing each remaining component. It mirrors the original tree under
// obj//lib/ so same-basename outputs from different directories stay distinct.
func structuralNamespace(dir string) string {
	var out []string
	for _, p := range strings.Split(filepath.ToSlash(filepath.Clean(dir)), "/") {
		if p == "" || p == "." || p == ".." {
			continue
		}
		out = append(out, p)
	}
	return filepath.Join(out...)
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

// ensureOutputArg sets the -o value, APPENDING `-o <newOutput>` when the args
// carry no -o at all. Unlike replaceOutputArg (used where an explicit -o always
// exists), this handles compiles that relied on the compiler's implicit object
// name, e.g. `cc -c op.c` → op.o. A no-op when newOutput is empty.
func ensureOutputArg(args []string, newOutput string) []string {
	if newOutput == "" {
		return args
	}
	for i, arg := range args {
		if arg == "-o" && i+1 < len(args) {
			result := make([]string, len(args))
			copy(result, args)
			result[i+1] = newOutput
			return result
		}
	}
	return append(append([]string{}, args...), "-o", newOutput)
}

// deriveObjectOutput computes the object name for a compile that omitted -o:
// the first source operand's basename with a .o extension (op.c → op.o). Returns
// "" when no source operand is present.
func deriveObjectOutput(args []string) string {
	skipNext := false
	for _, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if strings.HasPrefix(a, "-") {
			// Options that take a separate value; skip the value so it is not
			// mistaken for a source operand (e.g. `-include foo.h`).
			switch a {
			case "-o", "-I", "-D", "-U", "-L", "-l", "-include", "-isystem", "-x", "-MT", "-MF":
				skipNext = true
			}
			continue
		}
		switch strings.ToLower(filepath.Ext(a)) {
		case ".c", ".cc", ".cpp", ".cxx", ".c++", ".s", ".m", ".mm":
			base := filepath.Base(a)
			return strings.TrimSuffix(base, filepath.Ext(base)) + ".o"
		}
	}
	return ""
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
// rewriteInputPaths maps .o / .a / .lo references onto the flat build dir as a
// first approximation; resolveObjectRefs later re-points each to the ACTUAL
// (possibly nested) output that produced it, keyed by basename + work dir. It
// can't nest correctly on its own because an archive/link may reference an
// object built in a different work dir (e.g. a top-level libcore.a pulling in
// a plugin.o compiled under a plugins/ subdir).
// rewriteInputPaths re-points the .o / .a / .lo operands of an archive or link
// command onto the wasm build tree. It must apply the SAME collision-safe
// naming that rewriteOutputPath gave the corresponding compile/archive OUTPUT —
// otherwise an input that was disambiguated on output (target-isolation
// namespace subdir, or the `<stem>_<suffix>.o` collision suffix) is referenced
// here under a bare `obj/<base>` path that nothing ever wrote, and the archive
// fails with "<obj>: No such file". Passing the step's own workDir/projectRoot
// makes the reference reproduce the producer's path, because the archive that
// consumes an object runs in the same work dir as the compile that produced it.
// resolveObjectRefs is the later safety net for genuine cross-work-dir refs.
func rewriteInputPaths(args []string, buildDir, workDir, projectRoot string) []string {
	result := make([]string, len(args))
	for i, arg := range args {
		if strings.HasPrefix(arg, "-") {
			result[i] = arg
			continue
		}
		switch strings.ToLower(filepath.Ext(arg)) {
		case ".o":
			result[i] = rewriteOutputPath(arg, buildDir, "obj", workDir, projectRoot)
		case ".a", ".lo":
			result[i] = rewriteOutputPath(arg, buildDir, "lib", workDir, projectRoot)
		default:
			result[i] = arg
		}
	}
	return result
}
