package wasmbuild

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/wasmify/internal/buildjson"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Target != "wasm32-wasip1" {
		t.Errorf("expected default target wasm32-wasip1, got %s", cfg.Target)
	}
	if !cfg.NoEntry {
		t.Error("expected NoEntry=true by default")
	}
	if !cfg.AllowUndefined {
		t.Error("expected AllowUndefined=true by default")
	}
	if cfg.StackSize != DefaultStackSize {
		t.Errorf("expected StackSize=DefaultStackSize, got %d", cfg.StackSize)
	}
}

func TestSysroot(t *testing.T) {
	got := Sysroot("/opt/wasi-sdk")
	want := filepath.Join("/opt/wasi-sdk", "share", "wasi-sysroot")
	if got != want {
		t.Errorf("Sysroot(%q) = %q, want %q", "/opt/wasi-sdk", got, want)
	}
}

func TestShouldRemoveCompileFlag(t *testing.T) {
	tests := []struct {
		flag string
		want bool
	}{
		{"-pthread", true},
		{"-fcolor-diagnostics", true},
		{"-fPIC", true},
		{"-fpic", true},
		{"-MD", true},
		{"-MP", true},
		{"-fstack-protector", true},
		{"-fstack-protector-strong", true},
		{"-no-canonical-prefixes", true},
		{"-MF", true},
		{"-MT", true},
		{"-MQ", true},
		{"-Xclang", true},
		// keep
		{"-O2", false},
		{"-I/usr/include", false},
		{"-c", false},
		{"-std=c++17", false},
	}
	for _, tt := range tests {
		t.Run(tt.flag, func(t *testing.T) {
			if got := shouldRemoveCompileFlag(tt.flag); got != tt.want {
				t.Errorf("shouldRemoveCompileFlag(%q) = %v, want %v", tt.flag, got, tt.want)
			}
		})
	}
}

func TestShouldRemoveCompileFlagPrefix(t *testing.T) {
	tests := []struct {
		flag string
		want bool
	}{
		{"--target=x86_64-apple-darwin", true},
		{"--sysroot=/foo", true},
		{"-mmacosx-version-min=10.15", true},
		{"-miphoneos-version-min=11.0", true},
		{"-fmodule-map-file=foo", true},
		{"-fmodule-name=bar", true},
		{"-fstack-protector-all", true},
		{"-frandom-seed=abc", true},
		{"-stdlib=libc++", true},
		{"-MF", true},
		{"-arch", true},
		{"-maes", true},
		{"-msse4.1", true},
		{"-mavx2", true},
		{"-mfpu=neon", true},
		{"-march=x86-64", true},
		// keep
		{"-I/usr/include", false},
		{"-O2", false},
		{"-mno-avx", false},
	}
	for _, tt := range tests {
		t.Run(tt.flag, func(t *testing.T) {
			if got := shouldRemoveCompileFlagPrefix(tt.flag); got != tt.want {
				t.Errorf("shouldRemoveCompileFlagPrefix(%q) = %v, want %v", tt.flag, got, tt.want)
			}
		})
	}
}

func TestFilterCompileFlags(t *testing.T) {
	args := []string{
		"-c",
		"-O2",
		"-pthread",
		"-fPIC",
		"-MF", "deps.d",
		"-I/usr/include",
		"-stdlib=libc++",
		"--target=x86_64-apple-darwin",
		"-Xclang", "-disable-O0",
		"test.cc",
	}
	result := filterCompileFlags(args)
	// kept: -c, -O2, -I/usr/include, test.cc
	for _, want := range []string{"-c", "-O2", "-I/usr/include", "test.cc"} {
		found := false
		for _, a := range result {
			if a == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected %q in result, got %v", want, result)
		}
	}
	// removed: -pthread, -fPIC, -MF, deps.d, -stdlib=libc++, --target=..., -Xclang
	for _, bad := range []string{"-pthread", "-fPIC", "-MF", "deps.d", "-stdlib=libc++", "-Xclang"} {
		for _, a := range result {
			if a == bad {
				t.Errorf("unexpected %q in filtered result: %v", bad, result)
			}
		}
	}
	// --target= prefix removed
	for _, a := range result {
		if strings.HasPrefix(a, "--target=") {
			t.Errorf("unexpected --target=... in filtered: %v", result)
		}
	}
}

func TestShouldRemoveLinkFlag(t *testing.T) {
	tests := []struct {
		flag string
		want bool
	}{
		{"-pthread", true},
		{"-fPIC", true},
		{"-pie", true},
		{"-shared", true},
		{"-rdynamic", true},
		{"-Bstatic", true},
		{"-Bdynamic", true},
		{"-framework", true},
		{"-rpath", true},
		{"-lunwind", true},
		{"-lpthread", true},
		{"-lm", true},
		{"-ldl", true},
		{"-lrt", true},
		// keep
		{"-O2", false},
		{"-lfoo", false},
		{"-o", false},
	}
	for _, tt := range tests {
		t.Run(tt.flag, func(t *testing.T) {
			if got := shouldRemoveLinkFlag(tt.flag); got != tt.want {
				t.Errorf("shouldRemoveLinkFlag(%q) = %v, want %v", tt.flag, got, tt.want)
			}
		})
	}
}

func TestShouldRemoveLinkFlagPrefix(t *testing.T) {
	tests := []struct {
		flag string
		want bool
	}{
		{"--target=wasm32", true},
		{"--sysroot=/foo", true},
		{"-Wl,-rpath,/lib", true},
		{"-Wl,-install_name,foo", true},
		{"-Wl,-headerpad,abc", true},
		{"-L/usr/lib", true},
		// keep
		{"-Wl,--gc-sections", false},
		{"-lwasi-emulated-signal", false},
	}
	for _, tt := range tests {
		t.Run(tt.flag, func(t *testing.T) {
			if got := shouldRemoveLinkFlagPrefix(tt.flag); got != tt.want {
				t.Errorf("shouldRemoveLinkFlagPrefix(%q) = %v, want %v", tt.flag, got, tt.want)
			}
		})
	}
}

func TestFilterLinkFlags(t *testing.T) {
	args := []string{
		"-o", "out",
		"-pthread",
		"-lm",
		"-framework", "CoreFoundation",
		"-L/usr/lib",
		"-lfoo",
		"-Wl,-rpath,/abc",
		"-Wl,--gc-sections",
	}
	result := filterLinkFlags(args)
	// Kept: -o, out, -lfoo, -Wl,--gc-sections
	want := []string{"-o", "out", "-lfoo", "-Wl,--gc-sections"}
	for _, w := range want {
		found := false
		for _, a := range result {
			if a == w {
				found = true
			}
		}
		if !found {
			t.Errorf("expected %q in result, got %v", w, result)
		}
	}
	// Removed: -pthread, -lm, -framework, CoreFoundation (consumed), -L/usr/lib, -Wl,-rpath...
	for _, bad := range []string{"-pthread", "-lm", "-framework", "CoreFoundation", "-L/usr/lib"} {
		for _, a := range result {
			if a == bad {
				t.Errorf("unexpected %q in filtered result: %v", bad, result)
			}
		}
	}
}

func TestFlagTakesArg(t *testing.T) {
	for _, f := range []string{"-MF", "-MT", "-MQ", "-arch", "-Xclang"} {
		if !flagTakesArg(f) {
			t.Errorf("flagTakesArg(%q) should be true", f)
		}
	}
	for _, f := range []string{"-c", "-o", "-O2", "-pthread"} {
		if flagTakesArg(f) {
			t.Errorf("flagTakesArg(%q) should be false", f)
		}
	}
}

func TestLinkFlagTakesArg(t *testing.T) {
	for _, f := range []string{"-framework", "-rpath", "-arch", "-force_load"} {
		if !linkFlagTakesArg(f) {
			t.Errorf("linkFlagTakesArg(%q) should be true", f)
		}
	}
	for _, f := range []string{"-o", "-O2", "-lm"} {
		if linkFlagTakesArg(f) {
			t.Errorf("linkFlagTakesArg(%q) should be false", f)
		}
	}
}

func TestFilterNativeLibraries(t *testing.T) {
	args := []string{
		"-o", "out",
		"/opt/toolchains_llvm/lib/libc++.a",
		"/usr/lib/libunwind.a",
		"mylib/_objs/foo/libfoo.a",
		"-lfoo",
	}
	result := filterNativeLibraries(args)
	// Native libs removed:
	for _, bad := range []string{"/opt/toolchains_llvm/lib/libc++.a", "/usr/lib/libunwind.a"} {
		for _, a := range result {
			if a == bad {
				t.Errorf("expected native lib %q to be removed, got %v", bad, result)
			}
		}
	}
	// Project lib kept:
	found := false
	for _, a := range result {
		if a == "mylib/_objs/foo/libfoo.a" {
			found = true
		}
	}
	if !found {
		t.Error("expected project .a to be kept")
	}
}

func TestIsNativeToolchainLib(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/opt/toolchains_llvm/lib/libc++.a", true},
		{"/home/user/llvm_toolchain/lib/libfoo.a", true},
		{"/usr/lib/libunwind.a", true},
		{"/Library/Developer/Xcode/libfoo.a", true},
		{"bazel-out/k8-fastbuild/bin/lib.a", false},
		{"mylib/_objs/lib.a", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := isNativeToolchainLib(tt.path); got != tt.want {
				t.Errorf("isNativeToolchainLib(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestExtractOutputFromArgs(t *testing.T) {
	args := []string{"-c", "-O2", "-o", "out.o", "test.cc"}
	if got := extractOutputFromArgs(args); got != "out.o" {
		t.Errorf("got %q, want out.o", got)
	}
	// No -o
	if got := extractOutputFromArgs([]string{"-c", "test.cc"}); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
	// -o with no value
	if got := extractOutputFromArgs([]string{"test.cc", "-o"}); got != "" {
		t.Errorf("expected empty for dangling -o, got %q", got)
	}
}

func TestIsCPPCompiler(t *testing.T) {
	for _, name := range []string{"c++", "g++", "clang++"} {
		if !isCPPCompiler(name) {
			t.Errorf("isCPPCompiler(%q) should be true", name)
		}
	}
	for _, name := range []string{"cc", "gcc", "clang"} {
		if isCPPCompiler(name) {
			t.Errorf("isCPPCompiler(%q) should be false", name)
		}
	}
}

func TestResolveCompiler(t *testing.T) {
	cfg := WasmConfig{WasiSDKPath: "/opt/wasi-sdk"}
	// C++ language → clang++
	got := resolveCompiler("clang", "c++", cfg)
	want := filepath.Join("/opt/wasi-sdk", "bin", "clang++")
	if got != want {
		t.Errorf("resolveCompiler c++ → %q, want %q", got, want)
	}
	// C → clang
	got = resolveCompiler("clang", "c", cfg)
	want = filepath.Join("/opt/wasi-sdk", "bin", "clang")
	if got != want {
		t.Errorf("resolveCompiler c → %q, want %q", got, want)
	}
	// g++ compiler → clang++
	got = resolveCompiler("g++", "", cfg)
	want = filepath.Join("/opt/wasi-sdk", "bin", "clang++")
	if got != want {
		t.Errorf("resolveCompiler g++ → %q, want %q", got, want)
	}
}

func TestWasmCompileFlagsWithPosixCompat(t *testing.T) {
	cfg := WasmConfig{
		WasiSDKPath:    "/opt/wasi-sdk",
		Target:         "wasm32-wasip1",
		PosixCompatDir: "/tmp/posix",
	}
	flags := wasmCompileFlags(cfg)
	// Check key flags
	joined := strings.Join(flags, " ")
	for _, must := range []string{
		"-isystem", "/tmp/posix",
		"--target=wasm32-wasip1",
		"-D__EMSCRIPTEN__",
		"-D_WASI_EMULATED_SIGNAL",
		"-D_WASI_EMULATED_PROCESS_CLOCKS",
		"-D_WASI_EMULATED_MMAN",
	} {
		if !strings.Contains(joined, must) {
			t.Errorf("expected %q in wasmCompileFlags, got %v", must, flags)
		}
	}
}


// TestWasmFlagsLTOToggle pins the contract that --lto adds
// `-flto=thin` to BOTH the per-file compile and the link, and that
// the flag is absent when LTO is off. Mismatched compile/link LTO
// state silently regresses to ordinary linking, so we assert both

func TestWasmLinkFlags(t *testing.T) {
	cfg := WasmConfig{
		WasiSDKPath:    "/opt/wasi-sdk",
		Target:         "wasm32-wasip1",
		NoEntry:        true,
		AllowUndefined: true,
		StackSize:      1024 * 1024,
	}
	flags := wasmLinkFlags(cfg)
	joined := strings.Join(flags, " ")
	for _, must := range []string{
		"--target=wasm32-wasip1",
		"-mexec-model=reactor",
		"-Wl,--export=wasm_alloc",
		"-Wl,--export=wasm_free",
		"-Wl,--export=wasm_init",
		"-Wl,--export=_initialize",
		"-Wl,--gc-sections",
		"-Wl,--strip-all",
		"-Wl,--allow-undefined",
		"stack-size=1048576",
		"-lwasi-emulated-signal",
		"-lwasi-emulated-process-clocks",
		"-lwasi-emulated-mman",
	} {
		if !strings.Contains(joined, must) {
			t.Errorf("expected %q in wasmLinkFlags, got %v", must, flags)
		}
	}
}

func TestWasmLinkFlagsDefaultStackSize(t *testing.T) {
	cfg := WasmConfig{
		WasiSDKPath: "/opt/wasi-sdk",
		Target:      "wasm32-wasip1",
	}
	// StackSize=0 should default to DefaultStackSize
	flags := wasmLinkFlags(cfg)
	joined := strings.Join(flags, " ")
	if !strings.Contains(joined, "stack-size=33554432") {
		t.Errorf("expected default stack size in flags, got %v", flags)
	}
	// NoEntry false → no -mexec-model
	if strings.Contains(joined, "-mexec-model") {
		t.Errorf("no NoEntry should not emit -mexec-model, got %v", flags)
	}
	// AllowUndefined false → no --allow-undefined
	if strings.Contains(joined, "--allow-undefined") {
		t.Errorf("AllowUndefined=false should not emit --allow-undefined, got %v", flags)
	}
}

func TestReplaceOutputArg(t *testing.T) {
	args := []string{"-c", "-o", "old.o", "test.cc"}
	result := replaceOutputArg(args, "new.o")
	if result[2] != "new.o" {
		t.Errorf("expected new.o, got %v", result)
	}
	// Original unchanged
	if args[2] != "old.o" {
		t.Errorf("original args modified: %v", args)
	}
	// No -o: return copy unchanged
	noOut := replaceOutputArg([]string{"-c", "test.cc"}, "new.o")
	if len(noOut) != 2 || noOut[0] != "-c" || noOut[1] != "test.cc" {
		t.Errorf("expected copy unchanged, got %v", noOut)
	}
}

func TestCollectInputFiles(t *testing.T) {
	args := []string{
		"-c", "-O2",
		"-o", "out.o",
		"src/main.cc",
		"lib/foo.a",
		"obj/bar.o",
		"header.h",
		"-I/usr/include",
	}
	inputs := collectInputFiles(args)
	// Should include: main.cc, foo.a, bar.o, header.h
	for _, want := range []string{"src/main.cc", "lib/foo.a", "obj/bar.o", "header.h"} {
		found := false
		for _, i := range inputs {
			if i == want {
				found = true
			}
		}
		if !found {
			t.Errorf("expected %q in inputs, got %v", want, inputs)
		}
	}
	// Should not include out.o (output) or -I flag
	for _, bad := range []string{"out.o", "-I/usr/include"} {
		for _, i := range inputs {
			if i == bad {
				t.Errorf("unexpected %q in inputs: %v", bad, inputs)
			}
		}
	}
}

func TestSanitizeDirName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"abc", "abc"},
		{"abc-def", "abc_def"},
		{"a/b/c", "a_b_c"},
		{"hello~world", "hello_world"},
		{"0123456789abcdefghij_extra", "0123456789abcdefghij"}, // truncated to 20
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := sanitizeDirName(tt.in)
			if got != tt.want {
				t.Errorf("sanitizeDirName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRewriteOutputPath(t *testing.T) {
	tests := []struct {
		name     string
		orig     string
		buildDir string
		subdir   string
		want     string
	}{
		{"empty", "", "/build", "obj", ""},
		{"plain .a", "bazel-out/k8-fastbuild/bin/mylib/libfoo.a", "/build", "lib", "/build/lib/libfoo.a"},
		{"plain output", "bazel-out/bin/main", "/build", "output", "/build/output/main"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteOutputPath(tt.orig, tt.buildDir, tt.subdir)
			if got != tt.want {
				t.Errorf("rewriteOutputPath(%q) = %q, want %q", tt.orig, got, tt.want)
			}
		})
	}
}

func TestRewriteOutputPath_ObjDisambiguation(t *testing.T) {
	// _objs/foo/bar.o should disambiguate with "foo" parent dir
	got := rewriteOutputPath("bazel-out/k8/bin/project/lib/_objs/importer/parser.o", "/build", "obj")
	// Should be /build/obj/parser_<something>.o (a disambiguation)
	if !strings.HasPrefix(got, "/build/obj/parser_") {
		t.Errorf("expected disambiguated name, got %q", got)
	}
	if !strings.HasSuffix(got, ".o") {
		t.Errorf("expected .o extension, got %q", got)
	}
}

func TestRewriteInputPaths(t *testing.T) {
	buildDir := "/build"
	args := []string{
		"-c",
		"obj/main.o",
		"lib/foo.a",
		"some.lo",
		"-I/usr/include",
	}
	result := rewriteInputPaths(args, buildDir)
	// Flags preserved
	if result[0] != "-c" {
		t.Errorf("expected -c preserved, got %q", result[0])
	}
	if result[4] != "-I/usr/include" {
		t.Errorf("expected flag preserved, got %q", result[4])
	}
	// .a and .lo → lib/
	if result[2] != filepath.Join(buildDir, "lib", "foo.a") {
		t.Errorf("got %q for .a", result[2])
	}
	if result[3] != filepath.Join(buildDir, "lib", "some.lo") {
		t.Errorf("got %q for .lo", result[3])
	}
}

func TestExpandResponseFiles(t *testing.T) {
	tmp := t.TempDir()
	respFile := filepath.Join(tmp, "resp.txt")
	// One arg per line, with blank lines
	content := "-O2\n  -Iinclude  \n\nfoo.cc\n"
	if err := os.WriteFile(respFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	args := []string{"-c", "@" + respFile, "main.cc"}
	result := expandResponseFiles(args, tmp)
	// -c and main.cc kept; @file expanded
	want := []string{"-c", "-O2", "-Iinclude", "foo.cc", "main.cc"}
	if len(result) != len(want) {
		t.Fatalf("got %v, want %v", result, want)
	}
	for i, w := range want {
		if result[i] != w {
			t.Errorf("idx %d: got %q, want %q", i, result[i], w)
		}
	}
}

func TestExpandResponseFiles_Missing(t *testing.T) {
	// Missing response file: keep @file as-is
	result := expandResponseFiles([]string{"@/nonexistent"}, "/tmp")
	if len(result) != 1 || result[0] != "@/nonexistent" {
		t.Errorf("expected kept @/nonexistent, got %v", result)
	}
}

func TestExpandResponseFiles_Relative(t *testing.T) {
	tmp := t.TempDir()
	respFile := "resp.txt"
	if err := os.WriteFile(filepath.Join(tmp, respFile), []byte("-O2\nfoo.cc"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := expandResponseFiles([]string{"@" + respFile}, tmp)
	if len(result) != 2 || result[0] != "-O2" || result[1] != "foo.cc" {
		t.Errorf("unexpected: %v", result)
	}
}

func TestConvertLibtoolToAr(t *testing.T) {
	args := []string{
		"-static",
		"-o", "libout.a",
		"obj/a.o", "obj/b.o",
		"-no_warning_for_no_symbols",
	}
	result, output := convertLibtoolToAr(args)
	if output != "libout.a" {
		t.Errorf("expected output libout.a, got %q", output)
	}
	if len(result) < 3 || result[0] != "rcs" || result[1] != "libout.a" {
		t.Errorf("expected rcs libout.a inputs..., got %v", result)
	}
	// Inputs preserved
	inputs := result[2:]
	for _, want := range []string{"obj/a.o", "obj/b.o"} {
		found := false
		for _, i := range inputs {
			if i == want {
				found = true
			}
		}
		if !found {
			t.Errorf("expected %q in inputs, got %v", want, inputs)
		}
	}
}

func TestConvertLibtoolToAr_NotLibtool(t *testing.T) {
	// No -static or -o → return as-is
	args := []string{"rcs", "foo.a", "a.o"}
	result, output := convertLibtoolToAr(args)
	if output != "" {
		t.Errorf("expected empty output for non-libtool, got %q", output)
	}
	if len(result) != 3 {
		t.Errorf("expected args unchanged, got %v", result)
	}
}

func TestConvertLibtoolToAr_NoOutput(t *testing.T) {
	// -static with no -o (malformed)
	args := []string{"-static", "foo.o"}
	result, output := convertLibtoolToAr(args)
	if output != "" {
		t.Errorf("expected no output when -o missing, got %q", output)
	}
	// Returns original args
	if len(result) != 2 {
		t.Errorf("expected original args returned, got %v", result)
	}
}

func TestTransformCompileStep(t *testing.T) {
	cfg := WasmConfig{
		WasiSDKPath: "/opt/wasi-sdk",
		Target:      "wasm32-wasip1",
		BuildDir:    "/build",
	}
	step := buildjson.BuildStep{
		Type:       buildjson.StepCompile,
		Compiler:   "clang",
		Language:   "c++",
		Args:       []string{"-c", "-O2", "-pthread", "-o", "bazel-out/bin/main.o", "main.cc"},
		OutputFile: "bazel-out/bin/main.o",
		WorkDir:    "/src",
	}
	result := transformCompileStep(step, cfg)
	// Compiler resolved to wasi-sdk clang++
	if result.Executable != "/opt/wasi-sdk/bin/clang++" {
		t.Errorf("expected clang++, got %q", result.Executable)
	}
	// Type preserved
	if result.Type != buildjson.StepCompile {
		t.Errorf("expected compile type")
	}
	// WorkDir preserved
	if result.WorkDir != "/src" {
		t.Errorf("expected WorkDir preserved")
	}
	// -pthread filtered
	for _, a := range result.Args {
		if a == "-pthread" {
			t.Error("expected -pthread removed")
		}
	}
	// Target added
	foundTarget := false
	for _, a := range result.Args {
		if strings.HasPrefix(a, "--target=") {
			foundTarget = true
		}
	}
	if !foundTarget {
		t.Error("expected --target=... added")
	}
	// Output rewritten to /build/obj/
	if !strings.HasPrefix(result.OutputFile, "/build/obj/") {
		t.Errorf("expected /build/obj/... output, got %q", result.OutputFile)
	}
}

func TestTransformLinkStep(t *testing.T) {
	cfg := WasmConfig{
		WasiSDKPath: "/opt/wasi-sdk",
		Target:      "wasm32-wasip1",
		BuildDir:    "/build",
		NoEntry:     true,
	}
	step := buildjson.BuildStep{
		Type:       buildjson.StepLink,
		Compiler:   "clang++",
		Language:   "c++",
		Args:       []string{"-o", "bazel-out/bin/main", "-pthread", "obj/a.o", "lib/foo.a", "-lm"},
		OutputFile: "bazel-out/bin/main",
		WorkDir:    "/src",
	}
	result := transformLinkStep(step, cfg)
	// Output gets .wasm suffix
	if !strings.HasSuffix(result.OutputFile, ".wasm") {
		t.Errorf("expected .wasm suffix, got %q", result.OutputFile)
	}
	if !strings.HasPrefix(result.OutputFile, "/build/output/") {
		t.Errorf("expected /build/output/..., got %q", result.OutputFile)
	}
	// Must include wasm link flags. Per-method `w_<svc>_<mid>` exports
	// come from the bridge's own export_name attributes; only the
	// utility helpers need force-export from the link command.
	joined := strings.Join(result.Args, " ")
	if !strings.Contains(joined, "-Wl,--export=wasm_alloc") {
		t.Error("expected wasm_alloc export in link flags")
	}
}

func TestTransformArchiveStep(t *testing.T) {
	cfg := WasmConfig{
		WasiSDKPath: "/opt/wasi-sdk",
		BuildDir:    "/build",
	}
	step := buildjson.BuildStep{
		Type:       buildjson.StepArchive,
		Args:       []string{"rcs", "libfoo.a", "obj/a.o", "obj/b.o"},
		OutputFile: "bazel-out/bin/libfoo.a",
	}
	result := transformArchiveStep(step, cfg)
	// Uses wasi-sdk's llvm-ar
	if !strings.HasSuffix(result.Executable, "llvm-ar") {
		t.Errorf("expected llvm-ar, got %q", result.Executable)
	}
	// Output rewritten to /build/lib/
	if !strings.HasPrefix(result.OutputFile, "/build/lib/") {
		t.Errorf("expected /build/lib/..., got %q", result.OutputFile)
	}
	// args[1] updated with new output
	if len(result.Args) >= 2 && result.Args[0] == "rcs" {
		if !strings.HasPrefix(result.Args[1], "/build/lib/") {
			t.Errorf("expected archive name updated, got args: %v", result.Args)
		}
	}
}

func TestTransformSteps_Unsupported(t *testing.T) {
	cfg := WasmConfig{
		WasiSDKPath: "/opt/wasi-sdk",
		Target:      "wasm32-wasip1",
		BuildDir:    "/build",
	}
	// Unknown step type → marked skipped
	steps := []buildjson.BuildStep{
		{
			Type:       buildjson.StepType("unknown"),
			Executable: "/bin/bar",
			Args:       []string{"x"},
		},
	}
	result := TransformSteps(steps, cfg)
	if len(result) != 1 {
		t.Fatalf("expected 1 step")
	}
	if !result[0].Skipped {
		t.Error("expected unknown step type to be skipped")
	}
}

func TestTransformSteps_IDs(t *testing.T) {
	cfg := WasmConfig{
		WasiSDKPath: "/opt/wasi-sdk",
		Target:      "wasm32-wasip1",
		BuildDir:    "/build",
	}
	steps := []buildjson.BuildStep{
		{ID: 100, Type: buildjson.StepCompile, Compiler: "clang", Language: "c", Args: []string{"-c", "-o", "out/a.o", "a.c"}, OutputFile: "out/a.o"},
		{ID: 200, Type: buildjson.StepCompile, Compiler: "clang", Language: "c", Args: []string{"-c", "-o", "out/b.o", "b.c"}, OutputFile: "out/b.o"},
	}
	result := TransformSteps(steps, cfg)
	if result[0].ID != 1 || result[0].OriginalID != 100 {
		t.Errorf("expected ID=1 OriginalID=100, got %d/%d", result[0].ID, result[0].OriginalID)
	}
	if result[1].ID != 2 || result[1].OriginalID != 200 {
		t.Errorf("expected ID=2 OriginalID=200, got %d/%d", result[1].ID, result[1].OriginalID)
	}
}

func TestResolveOutputCollisions(t *testing.T) {
	// Two compile steps output same basename "arena.o" but from different source dirs.
	steps := []WasmBuildStep{
		{
			ID:         1,
			Type:       buildjson.StepCompile,
			Args:       []string{"-c", "-o", "/build/obj/arena.o", "proj/base/arena.cc"},
			OutputFile: "/build/obj/arena.o",
		},
		{
			ID:         2,
			Type:       buildjson.StepCompile,
			Args:       []string{"-c", "-o", "/build/obj/arena.o", "protobuf/compiler/arena.cc"},
			OutputFile: "/build/obj/arena.o",
		},
		{
			ID:         3,
			Type:       buildjson.StepArchive,
			Args:       []string{"rcs", "/build/lib/foo.a", "/build/obj/arena.o"},
			OutputFile: "/build/lib/foo.a",
		},
	}
	resolveOutputCollisions(steps)
	// Both compile steps must now have different output paths
	if steps[0].OutputFile == steps[1].OutputFile {
		t.Errorf("expected collision resolved, both = %q", steps[0].OutputFile)
	}
}

func TestUniqueOutputName(t *testing.T) {
	got := uniqueOutputName("/build/obj/arena.o", "proj/base/arena.cc")
	// Must end with .o
	if !strings.HasSuffix(got, ".o") {
		t.Errorf("expected .o suffix, got %q", got)
	}
	// Must include "arena" and a suffix
	if !strings.Contains(got, "arena_") {
		t.Errorf("expected arena_ prefix, got %q", got)
	}
}

func TestUniqueOutputName_TruncateLongDir(t *testing.T) {
	// Source path with very long dir name gets truncated (first 12 chars)
	got := uniqueOutputName("/build/obj/x.o", "my-very-long-directory-name-here/x.cc")
	if !strings.HasSuffix(got, ".o") {
		t.Errorf("expected .o suffix, got %q", got)
	}
}

func TestRewriteOutputPath_ObjNoDisambig(t *testing.T) {
	// When parent dir equals stem and grandparent walks to stem, no rename
	got := rewriteOutputPath("abc/parser.o", "/build", "obj")
	// "abc" is the parent, stem is "parser", they differ → disambiguation expected
	if !strings.Contains(got, "parser_abc") {
		t.Logf("got: %q", got)
	}
}

func TestTransformStep_Compile(t *testing.T) {
	cfg := WasmConfig{
		WasiSDKPath: "/opt/wasi-sdk",
		Target:      "wasm32-wasip1",
		BuildDir:    "/build",
	}
	step := buildjson.BuildStep{
		Type:     buildjson.StepCompile,
		Compiler: "clang",
		Language: "c",
		Args:     []string{"-c", "-o", "a.o", "a.c"},
	}
	ws := transformStep(step, cfg)
	if ws.Type != buildjson.StepCompile {
		t.Errorf("expected compile, got %s", ws.Type)
	}
}

func TestTransformStep_Link(t *testing.T) {
	cfg := WasmConfig{WasiSDKPath: "/opt/wasi-sdk", Target: "wasm32-wasip1", BuildDir: "/build"}
	step := buildjson.BuildStep{
		Type:     buildjson.StepLink,
		Compiler: "clang++",
		Language: "c++",
		Args:     []string{"-o", "out", "a.o"},
	}
	ws := transformStep(step, cfg)
	if ws.Type != buildjson.StepLink {
		t.Errorf("expected link, got %s", ws.Type)
	}
}

func TestTransformStep_Archive(t *testing.T) {
	cfg := WasmConfig{WasiSDKPath: "/opt/wasi-sdk", BuildDir: "/build"}
	step := buildjson.BuildStep{
		Type:       buildjson.StepArchive,
		Args:       []string{"rcs", "libfoo.a", "a.o"},
		OutputFile: "libfoo.a",
	}
	ws := transformStep(step, cfg)
	if ws.Type != buildjson.StepArchive {
		t.Errorf("expected archive, got %s", ws.Type)
	}
}
