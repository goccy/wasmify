// Package wasm64 provisions a wasm64-wasip1 sysroot inside an existing
// wasi-sdk installation: wasi-libc built for wasm64 from a pinned commit
// plus the wasmify patch set (LP64 WASI ABI), compiler-rt builtins, and
// C++ runtimes built with wasm exception handling. The official wasi-sdk
// ships wasm32 sysroots only; after this runs, the SDK's clang can build
// wasm64 C and C++ (with -fwasm-exceptions) with no extra flags beyond
// --target=wasm64-wasip1.
package wasm64

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed build_sysroot.sh patches/*.patch
var assets embed.FS

// BuiltinsRevision names the recipe the compiler-rt builtins are built
// with; build_sysroot.sh stamps it next to the archive, and an install
// stamped otherwise (by an earlier recipe) is not complete. wasm-cmp-int:
// the soft-float comparisons return an int, the result LLVM's WebAssembly
// backend calls them with.
const BuiltinsRevision = "wasm-cmp-int"

// InstallSysroot builds and installs the wasm64-wasip1 sysroot into
// sdkPath; threads selects the wasm64-wasip1-threads flavor (wasi-libc's
// posix thread model over wasi_thread_spawn, every archive compiled with
// -pthread so a --shared-memory link accepts it). Idempotent: completed
// stages are stamped and skipped. The build needs cmake, ninja, python3
// and curl on the host.
func InstallSysroot(sdkPath string, threads bool) error {
	for _, tool := range []string{"cmake", "ninja", "python3", "curl", "patch"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("wasm64 sysroot build needs %q on PATH", tool)
		}
	}

	// Materialize the embedded script + patches; the Go binary is the
	// only artifact a `go install`ed wasmify has.
	stage, err := os.MkdirTemp("", "wasmify-wasm64-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if err := fs.WalkDir(assets, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := assets.ReadFile(path)
		if err != nil {
			return err
		}
		dst := filepath.Join(stage, path)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o755)
	}); err != nil {
		return err
	}

	cmd := exec.Command("bash", filepath.Join(stage, "build_sysroot.sh"))
	cmd.Env = append(os.Environ(),
		"WASI_SDK_PATH="+sdkPath,
		"PATCH_DIR="+filepath.Join(stage, "patches"),
		"BUILTINS_REVISION="+BuiltinsRevision,
	)
	if threads {
		cmd.Env = append(cmd.Env, "WASM64_THREADS=1")
	}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("wasm64 sysroot build failed: %w", err)
	}
	return nil
}

// SysrootInstalled reports whether sdkPath already carries a complete
// wasm64 sysroot of the requested flavor (all stage stamps present, the
// builtins' of the current recipe). A stamp that cannot be read for another
// reason than its absence is an error.
func SysrootInstalled(sdkPath string, threads bool) (bool, error) {
	triple := "wasm64-wasip1"
	if threads {
		triple = "wasm64-wasip1-threads"
	}
	libDir := filepath.Join(sdkPath, "share", "wasi-sysroot", "lib", triple)
	for _, stamp := range []string{".wasmify-libc-tag", ".wasmify-cxx-tag"} {
		if _, err := os.Stat(filepath.Join(libDir, stamp)); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return false, nil
			}
			return false, err
		}
	}
	return builtinsCurrent(sdkPath, triple)
}

// builtinsCurrent reports whether the compiler-rt builtins of the triple
// in the SDK's clang resource directory carry the stamp of the current
// recipe: "<LLVM version> <BuiltinsRevision>".
func builtinsCurrent(sdkPath, triple string) (bool, error) {
	rtTriple := strings.Replace(triple, "wasm64-", "wasm64-unknown-", 1)
	stamps, err := filepath.Glob(filepath.Join(sdkPath, "lib", "clang", "*", "lib", rtTriple, ".wasmify-tag"))
	if err != nil {
		return false, err
	}
	for _, stamp := range stamps {
		data, err := os.ReadFile(stamp)
		if err != nil {
			return false, err
		}
		fields := strings.Fields(string(data))
		if len(fields) == 2 && fields[1] == BuiltinsRevision {
			return true, nil
		}
	}
	return false, nil
}
