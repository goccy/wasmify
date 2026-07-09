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
		workDir  string
		root     string
		want     string
	}{
		{"empty", "", "/build", "obj", "", "", ""},
		// The "output" subdir (final wasm) is never nested.
		{"plain output", "bazel-out/bin/main", "/build", "output", "/root/x", "/root", "/build/output/main"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteOutputPath(tt.orig, tt.buildDir, tt.subdir, tt.workDir, tt.root)
			if got != tt.want {
				t.Errorf("rewriteOutputPath(%q) = %q, want %q", tt.orig, got, tt.want)
			}
		})
	}
}

// TestRewriteOutputPath_TargetNesting: when the step's work dir lives under the
// project root, outputs mirror that structure under obj//lib/ so same-basename
// outputs from different source dirs never collide — no name hashing.
func TestRewriteOutputPath_TargetNesting(t *testing.T) {
	root := "/src/proj"
	// Two modules each compile a compress.c; nested under their work dirs.
	z := rewriteOutputPath("compress.o", "/build", "obj", root+"/mods/zlib-codec", root)
	b := rewriteOutputPath("compress.o", "/build", "obj", root+"/mods/bzip2-codec", root)
	if z != "/build/obj/mods/zlib-codec/compress.o" {
		t.Errorf("zlib compress.o = %q, want /build/obj/mods/zlib-codec/compress.o", z)
	}
	if b != "/build/obj/mods/bzip2-codec/compress.o" {
		t.Errorf("bzip2 compress.o = %q, want /build/obj/mods/bzip2-codec/compress.o", b)
	}
	if z == b {
		t.Fatal("two compress.o collided after nesting")
	}
	// Two modules each archive a Util.a (leaf dir == stem); nested by work dir.
	lu := rewriteOutputPath("../../build/x/Util/Util.a", "/build", "lib", root+"/mod-a", root)
	hu := rewriteOutputPath("../../build/y/Util/Util.a", "/build", "lib", root+"/mod-b", root)
	if lu != "/build/lib/mod-a/Util.a" {
		t.Errorf("mod-a Util.a = %q, want /build/lib/mod-a/Util.a", lu)
	}
	if hu != "/build/lib/mod-b/Util.a" {
		t.Errorf("mod-b Util.a = %q, want /build/lib/mod-b/Util.a", hu)
	}
	if lu == hu {
		t.Fatal("two Util.a collided after nesting")
	}
	// Work dir == project root nests nothing (stays flat).
	flat := rewriteOutputPath("core.o", "/build", "obj", root, root)
	if flat != "/build/obj/core.o" {
		t.Errorf("root-level object = %q, want /build/obj/core.o", flat)
	}
}

func TestRewriteOutputPath_Idempotent(t *testing.T) {
	// Archive steps set args to rewritten output paths and then pass the whole
	// arg list through rewriteInputPaths, which calls rewriteOutputPath again on
	// every .a/.lo arg. Running the second pass must be a no-op — a path already
	// under <buildDir>/<subdir>/ is returned unchanged.
	a := "/build/lib/list-utils/Util.a"
	if got := rewriteOutputPath(a, "/build", "lib", "/src/proj/list-utils", "/src/proj"); got != a {
		t.Errorf("rewriteOutputPath should be idempotent for already-rewritten paths\n got %q\nwant %q", got, a)
	}
	o := "/build/obj/mods/zlib/compress.o"
	if got := rewriteOutputPath(o, "/build", "obj", "/src/proj/mods/zlib", "/src/proj"); got != o {
		t.Errorf("rewriteOutputPath should be idempotent for .o paths\n got %q\nwant %q", got, o)
	}
}

func TestRewriteOutputPath_FallbackObjStructure(t *testing.T) {
	// When the work dir is NOT under the project root (e.g. a build sandbox
	// whose exec root sits elsewhere), objects keep the target-subdir + package
	// token scheme so two same-named .o from different packages stay distinct.
	a := rewriteOutputPath("bazel-out/darwin_arm64-opt/bin/googlesql/public/_objs/type_proto/type.pb.o", "/build", "obj", "", "")
	b := rewriteOutputPath("bazel-out/darwin_arm64-opt/bin/external/protobuf~/src/google/protobuf/_objs/type_proto/type.pb.o", "/build", "obj", "", "")
	if a == b {
		t.Fatalf("colliding .o paths: %q", a)
	}
	if a != "/build/obj/type.pb_type_proto_public.o" {
		t.Errorf("first .o got %q, want %q", a, "/build/obj/type.pb_type_proto_public.o")
	}
	if b != "/build/obj/type.pb_type_proto_protobuf.o" {
		t.Errorf("second .o got %q, want %q", b, "/build/obj/type.pb_type_proto_protobuf.o")
	}
}

func TestRewriteOutputPath_FallbackArchiveStructure(t *testing.T) {
	// When the work dir is not under the project root, archives mirror their own
	// source directory structure under lib/ (hash-free), so two same-basename
	// archives from different source trees keep distinct nested paths.
	a := rewriteOutputPath("bazel-out/aarch64-opt/bin/googlesql/public/libtype.a", "/build", "lib", "", "")
	b := rewriteOutputPath("bazel-out/aarch64-opt/bin/googlesql/public/types/libtype.a", "/build", "lib", "", "")
	if a == b {
		t.Fatalf("colliding archives mapped to the same path: %q", a)
	}
	if a != "/build/lib/bazel-out/aarch64-opt/bin/googlesql/public/libtype.a" {
		t.Errorf("first libtype.a got %q", a)
	}
	if b != "/build/lib/bazel-out/aarch64-opt/bin/googlesql/public/types/libtype.a" {
		t.Errorf("second libtype.a got %q", b)
	}
	// .lo archives (libtool-style) follow the same rule.
	c := rewriteOutputPath("bazel-out/aarch64-opt/bin/googlesql/public/libfoo.lo", "/build", "lib", "", "")
	if c != "/build/lib/bazel-out/aarch64-opt/bin/googlesql/public/libfoo.lo" {
		t.Errorf("libfoo.lo got %q", c)
	}
}

func TestRewriteInputPaths(t *testing.T) {
	buildDir := "/build"
	workDir, root := "", "" // build-sandbox layout: work dir not under project root
	args := []string{
		"-c",
		// A bazel per-target object whose basename collides across targets:
		// rewriteOutputPath disambiguates it to obj/analyzer_<suffix>.o, so the
		// archive MUST reference that same suffixed path, not a flat obj/analyzer.o
		// that nothing wrote (the regression that broke libanalyzer.a).
		"bazel-out/darwin_arm64-opt/bin/googlesql/public/_objs/analyzer/analyzer.o",
		"bazel-out/darwin_arm64-opt/bin/googlesql/public/libtype.a",
		"some.lo",
		"-I/usr/include",
	}
	result := rewriteInputPaths(args, buildDir, workDir, root)
	// Flags preserved.
	if result[0] != "-c" {
		t.Errorf("expected -c preserved, got %q", result[0])
	}
	if result[4] != "-I/usr/include" {
		t.Errorf("expected flag preserved, got %q", result[4])
	}
	// .o / .a / .lo refs must get the SAME collision-safe path rewriteOutputPath
	// gives the producing compile/archive OUTPUT, so archive inputs resolve to
	// files that were actually written.
	if want := rewriteOutputPath(args[1], buildDir, "obj", workDir, root); result[1] != want {
		t.Errorf("got %q for .o, want %q (must match the compile output)", result[1], want)
	}
	if want := rewriteOutputPath(args[2], buildDir, "lib", workDir, root); result[2] != want {
		t.Errorf("got %q for .a, want %q", result[2], want)
	}
	if want := rewriteOutputPath(args[3], buildDir, "lib", workDir, root); result[3] != want {
		t.Errorf("got %q for .lo, want %q", result[3], want)
	}
	// Guard the specific regression: the colliding .o must NOT collapse to a
	// bare flat path.
	if result[1] == filepath.Join(buildDir, "obj", "analyzer.o") {
		t.Errorf("colliding .o collapsed to flat %q — archive would reference an object that was never written", result[1])
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

// TestTransformSteps_ArchiveResolvesCollidingObject reproduces the libanalyzer.a
// failure: a bazel per-target object whose basename is disambiguated on OUTPUT
// (analyzer.o → obj/analyzer_<suffix>.o) must still be referenced by the archive
// that consumes it at that same suffixed path. The v0.4.6 regression rewrote
// archive inputs to a flat obj/analyzer.o that nothing wrote, so `llvm-ar`
// failed with "obj/analyzer.o: No such file".
func TestTransformSteps_ArchiveResolvesCollidingObject(t *testing.T) {
	cfg := WasmConfig{WasiSDKPath: "/opt/wasi-sdk", BuildDir: "/build"}
	const objDir = "bazel-out/darwin_arm64-opt/bin/googlesql/public/_objs/analyzer/"
	objPath := objDir + "analyzer.o"
	// A SECOND object in the same archive/work dir. With more than one produced
	// object the "exactly one unreferenced output" orphan heuristic in
	// resolveObjectRefs can no longer paper over a mismatched input path, so the
	// archive genuinely needs each input rewritten to its real (suffixed) output.
	obj2Path := objDir + "table_name_resolver.o"
	steps := []buildjson.BuildStep{
		{
			Type: buildjson.StepCompile, Compiler: "clang++", Language: "c++",
			Args:       []string{"-c", "-o", objPath, "googlesql/public/analyzer.cc"},
			OutputFile: objPath,
		},
		{
			Type: buildjson.StepCompile, Compiler: "clang++", Language: "c++",
			Args:       []string{"-c", "-o", obj2Path, "googlesql/public/table_name_resolver.cc"},
			OutputFile: obj2Path,
		},
		{
			Type:       buildjson.StepArchive,
			Args:       []string{"rcsD", "bazel-out/darwin_arm64-opt/bin/googlesql/public/libanalyzer.a", objPath, obj2Path},
			OutputFile: "bazel-out/darwin_arm64-opt/bin/googlesql/public/libanalyzer.a",
		},
	}
	result := TransformSteps(steps, cfg)

	// Every compile output must be an object the archive actually references.
	var compileOuts []string
	var archive WasmBuildStep
	for _, s := range result {
		switch s.Type {
		case buildjson.StepCompile:
			if !strings.HasPrefix(s.OutputFile, "/build/obj/") {
				t.Fatalf("compile output not under /build/obj/: %q", s.OutputFile)
			}
			compileOuts = append(compileOuts, s.OutputFile)
		case buildjson.StepArchive:
			archive = s
		}
	}
	argSet := map[string]bool{}
	for _, a := range archive.Args {
		argSet[a] = true
	}
	// The flat names nothing wrote must not appear (the regression symptom).
	for _, flat := range []string{"/build/obj/analyzer.o", "/build/obj/table_name_resolver.o"} {
		if argSet[flat] {
			t.Errorf("archive references flat %q, which no compile step wrote", flat)
		}
	}
	// Each produced object must be referenced at its real (suffixed) path.
	for _, out := range compileOuts {
		if !argSet[out] {
			t.Errorf("archive does not reference produced object %q; args=%v", out, archive.Args)
		}
	}
}

// TestTransformSteps_PerlStyleCollidingDirs models perl-wasm's multi-directory
// build: two ext/ modules each compile a same-basename object in their OWN work
// dir (under the project root, so target isolation namespaces them by subdir)
// and archive it locally. Each archive must reference its own object, not the
// other module's — the collision case the target-isolation work (#24) added.
func TestTransformSteps_PerlStyleCollidingDirs(t *testing.T) {
	const root = "/src/perl"
	cfg := WasmConfig{WasiSDKPath: "/opt/wasi-sdk", BuildDir: "/wb", ProjectRoot: root}
	steps := []buildjson.BuildStep{
		{Type: buildjson.StepCompile, Compiler: "cc", Language: "c", WorkDir: root + "/ext/A",
			Args: []string{"-c", "-o", "Mod.o", "Mod.c"}, OutputFile: "Mod.o"},
		{Type: buildjson.StepArchive, WorkDir: root + "/ext/A",
			Args: []string{"cr", "Mod.a", "Mod.o"}, OutputFile: "Mod.a"},
		{Type: buildjson.StepCompile, Compiler: "cc", Language: "c", WorkDir: root + "/ext/B",
			Args: []string{"-c", "-o", "Mod.o", "Mod.c"}, OutputFile: "Mod.o"},
		{Type: buildjson.StepArchive, WorkDir: root + "/ext/B",
			Args: []string{"cr", "Mod.a", "Mod.o"}, OutputFile: "Mod.a"},
	}
	result := TransformSteps(steps, cfg)

	produced := map[string]bool{}
	byWD := map[string]string{} // archive workDir → its Mod.o arg
	for _, s := range result {
		switch s.Type {
		case buildjson.StepCompile:
			produced[s.OutputFile] = true
		case buildjson.StepArchive:
			for _, a := range s.Args {
				if strings.HasSuffix(a, "Mod.o") {
					byWD[s.WorkDir] = a
				}
			}
		}
	}
	// Namespaced by subdir — distinct paths, both actually produced.
	if a := byWD[root+"/ext/A"]; a != "/wb/obj/ext/A/Mod.o" || !produced[a] {
		t.Errorf("ext/A archive references %q (produced=%v), want /wb/obj/ext/A/Mod.o", a, produced[a])
	}
	if b := byWD[root+"/ext/B"]; b != "/wb/obj/ext/B/Mod.o" || !produced[b] {
		t.Errorf("ext/B archive references %q (produced=%v), want /wb/obj/ext/B/Mod.o", b, produced[b])
	}
	if byWD[root+"/ext/A"] == byWD[root+"/ext/B"] {
		t.Error("the two modules' Mod.o collided — target isolation lost")
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

// TestResolveObjectRefs covers the two cases per-step nesting can't handle on
// its own: (1) a CROSS-work-dir reference — a top-level archive pulling in an
// object compiled elsewhere (libcore.a ← plugins/loader's plugin.o) — resolves
// by unique basename regardless of the archive's own work dir; and (2) a
// colliding basename (two codec.o) resolves to the object from the SAME work
// dir as the referencing archive.
func TestResolveObjectRefs(t *testing.T) {
	const aWD = "/src/proj/codecs/a"
	const bWD = "/src/proj/codecs/b"
	const plWD = "/src/proj/plugins/loader"
	const coreWD = "/src/proj"
	steps := []WasmBuildStep{
		// codec.o compiled in each codec dir (nested outputs).
		{ID: 1, Type: buildjson.StepCompile, WorkDir: aWD,
			OutputFile: "/build/obj/codecs/a/codec.o"},
		{ID: 2, Type: buildjson.StepCompile, WorkDir: bWD,
			OutputFile: "/build/obj/codecs/b/codec.o"},
		// plugin.o compiled under plugins/loader (unique basename).
		{ID: 3, Type: buildjson.StepCompile, WorkDir: plWD,
			OutputFile: "/build/obj/plugins/loader/plugin.o"},
		// Each codec archives its own codec.o (flat ref, pre-resolution).
		{ID: 4, Type: buildjson.StepArchive, WorkDir: aWD,
			Args: []string{"cr", "/build/lib/codecs/a/CodecA.a", "/build/obj/codec.o"}},
		{ID: 5, Type: buildjson.StepArchive, WorkDir: bWD,
			Args: []string{"cr", "/build/lib/codecs/b/CodecB.a", "/build/obj/codec.o"}},
		// libcore.a (project root) pulls in plugin.o from plugins/loader.
		{ID: 6, Type: buildjson.StepArchive, WorkDir: coreWD,
			Args: []string{"rc", "/build/lib/libcore.a", "/build/obj/plugin.o"}},
	}
	resolveObjectRefs(steps)

	// (2) collision disambiguation by work dir.
	if got := steps[3].Args[len(steps[3].Args)-1]; got != "/build/obj/codecs/a/codec.o" {
		t.Errorf("CodecA.a codec.o ref = %q, want the codec-a object", got)
	}
	if got := steps[4].Args[len(steps[4].Args)-1]; got != "/build/obj/codecs/b/codec.o" {
		t.Errorf("CodecB.a codec.o ref = %q, want the codec-b object", got)
	}
	// (1) cross-work-dir reference to a unique basename.
	if got := steps[5].Args[len(steps[5].Args)-1]; got != "/build/obj/plugins/loader/plugin.o" {
		t.Errorf("libcore.a plugin.o ref = %q, want the plugins/loader object", got)
	}
}

// TestResolveObjectRefs_RenamedObject models a build that compiles FooBar.c to
// FooBar.o and then renames it (a shell `mv FooBar.o Bar.o` wasmify never
// wraps), so the archive references Bar.o but only FooBar.o was recorded. The
// sole unreferenced compile output in that work dir IS the renamed-away object.
func TestResolveObjectRefs_RenamedObject(t *testing.T) {
	const wd = "/src/proj/ext/Foo-Bar"
	steps := []WasmBuildStep{
		{ID: 1, Type: buildjson.StepCompile, WorkDir: wd,
			OutputFile: "/build/obj/ext/Foo-Bar/FooBar.o"},
		{ID: 2, Type: buildjson.StepArchive, WorkDir: wd,
			Args: []string{"cr", "/build/lib/ext/Foo-Bar/Bar.a", "/build/obj/Bar.o"}},
	}
	resolveObjectRefs(steps)
	if got := steps[1].Args[len(steps[1].Args)-1]; got != "/build/obj/ext/Foo-Bar/FooBar.o" {
		t.Errorf("Bar.a Bar.o ref = %q, want the renamed-away FooBar.o", got)
	}
}

// TestResolveObjectRefs_ExternalLibKept: a reference to something wasmify did
// not build (no producer, no lone orphan) is left untouched.
func TestResolveObjectRefs_ExternalLibKept(t *testing.T) {
	steps := []WasmBuildStep{
		{ID: 1, Type: buildjson.StepLink, WorkDir: "/src/proj",
			Args: []string{"-o", "out", "/usr/lib/libcrypto.a"}},
	}
	resolveObjectRefs(steps)
	if got := steps[0].Args[len(steps[0].Args)-1]; got != "/usr/lib/libcrypto.a" {
		t.Errorf("external lib ref = %q, want it left as-is", got)
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
