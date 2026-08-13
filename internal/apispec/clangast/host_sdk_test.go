package clangast

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestTripleTargetsDarwin(t *testing.T) {
	cases := []struct {
		triple string
		want   bool
	}{
		{"arm64-apple-darwin24.5.0", true},
		{"x86_64-apple-macosx15.0.0", true},
		{"arm64-apple-ios17.0", true}, // apple vendor: still the host toolchain
		{"wasm32-unknown-wasi", false},
		{"wasm32-unknown-wasip1", false},
		{"wasm64-unknown-wasip1", false},
		{"x86_64-pc-linux-gnu", false},
		{"aarch64-unknown-linux-gnu", false},
		{"", false},
		{"wasm32", false}, // malformed: too few components
	}
	for _, tc := range cases {
		if got := tripleTargetsDarwin(tc.triple); got != tc.want {
			t.Errorf("tripleTargetsDarwin(%q) = %v, want %v", tc.triple, got, tc.want)
		}
	}
}

// stubClang writes an executable script that answers -print-target-triple
// with the given triple, standing in for a real compiler.
func stubClang(t *testing.T, triple string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub compiler script needs a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "clang")
	script := "#!/bin/sh\nif [ \"$1\" = -print-target-triple ]; then echo " + triple + "; fi\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// A cross compiler must never receive the host SDK sysroot: wasi-sdk's
// clang resolves its own sysroot, and -isysroot pointing at the macOS
// SDK mixes two standard libraries in one include path. This holds on
// every host OS (on non-darwin the injection never fires at all).
func TestNoHostSDKForCrossCompiler(t *testing.T) {
	clang := stubClang(t, "wasm32-unknown-wasip1")
	for _, args := range [][]string{
		BuildSyntaxCheckArgs(clang, "api.h", nil),
		buildClangArgs(clang, "api.h", nil),
	} {
		for _, a := range args {
			if a == "-isysroot" {
				t.Fatalf("host SDK sysroot injected for a wasm-targeting compiler: %v", args)
			}
		}
	}
}

// A compiler that already received a sysroot in the captured flags
// keeps it, host-targeting or not.
func TestExplicitSysrootSuppressesInjection(t *testing.T) {
	clang := stubClang(t, "arm64-apple-darwin24.0.0")
	args := BuildSyntaxCheckArgs(clang, "api.h", []string{"--sysroot=/opt/sysroot"})
	count := 0
	for _, a := range args {
		if a == "-isysroot" {
			count++
		}
	}
	if count != 0 {
		t.Fatalf("SDK sysroot injected although flags already carry one: %v", args)
	}
}

// On macOS the host-targeting compiler still gets the SDK, exactly as
// before the cross-compiler gate.
func TestHostSDKForHostCompiler(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("host SDK injection is darwin-only")
	}
	if len(detectMacOSSDKFlags()) == 0 {
		t.Skip("no macOS SDK on this machine (xcrun failed)")
	}
	clang := stubClang(t, "arm64-apple-darwin24.0.0")
	args := BuildSyntaxCheckArgs(clang, "api.h", nil)
	found := false
	for _, a := range args {
		if a == "-isysroot" {
			found = true
		}
	}
	if !found {
		t.Fatalf("host-targeting compiler lost its SDK sysroot: %v", args)
	}
}
