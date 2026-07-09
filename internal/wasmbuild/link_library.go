package wasmbuild

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// LinkLibrary creates a single .wasm from all successfully built .a files
// in the build directory. This is used for library targets that have no
// native link step (e.g., Bazel cc_library targets).
// extraObjects are additional .o files to include (e.g., api_bridge.o).
func LinkLibrary(targetName string, cfg WasmConfig, extraObjects []string) (string, error) {
	libDir := filepath.Join(cfg.BuildDir, "lib")

	// Archives are laid out under lib/ mirroring their source directories
	// (target isolation, see rewriteOutputPath), so walk the tree recursively.
	// A cc_library archive is recorded with either a `.a` or a `.lo`
	// (libtool-style "library object") extension; both hold wasm32-wasip1 .o
	// files after wasm-build replay, so pick up either.
	var archives []string
	err := filepath.WalkDir(libDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".a") || strings.HasSuffix(path, ".lo") {
			archives = append(archives, path)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("failed to walk lib directory: %w", err)
	}
	if len(archives) == 0 {
		return "", fmt.Errorf("no .a/.lo archives found under %s", libDir)
	}
	// Order archives by basename (full path breaks ties deterministically).
	// wasm-ld pulls archive members to satisfy still-undefined symbols, so the
	// order affects which members — and their transitive host imports — end up
	// in the wasm. Keying on basename keeps that order stable against where an
	// archive is nested under lib/ (target isolation), so the linked output is
	// independent of the intermediate directory layout.
	sort.Slice(archives, func(i, j int) bool {
		bi, bj := filepath.Base(archives[i]), filepath.Base(archives[j])
		if bi != bj {
			return bi < bj
		}
		return archives[i] < archives[j]
	})

	outputFile := filepath.Join(cfg.BuildDir, "output", targetName+".wasm")
	if err := os.MkdirAll(filepath.Dir(outputFile), 0o755); err != nil {
		return "", fmt.Errorf("failed to create output directory: %w", err)
	}

	compiler := filepath.Join(cfg.WasiSDKPath, "bin", "clang++")
	args := wasmLinkFlags(cfg)
	args = append(args,
		"-Wl,--allow-multiple-definition",
		"-o", outputFile,
	)
	// Extra linker flags (e.g. -Wl,--wrap=connect to route libc socket calls
	// to host-provided shims in the bridge sources) from cfg.ExtraLDFlags. A nil
	// slice appends nothing, so no guard is needed.
	args = append(args, cfg.ExtraLDFlags...)

	// Add extra objects (e.g., bridge code) before archives. These
	// objects carry the per-method `w_<svc>_<mid>` and
	// `wasmify_get_type_name` exports — the references they make into
	// the archive libraries are what pull the needed code in.
	args = append(args, extraObjects...)
	args = append(args, archives...)

	// Resolve the LLVM SjLj runtime last. Code compiled with
	// `-mllvm -wasm-enable-sjlj` lowers every setjmp/longjmp to calls into
	// __wasm_setjmp / __wasm_longjmp / __wasm_setjmp_test, which live in the
	// wasi-sysroot's libsetjmp.a (they implement the jump via wasm exception
	// handling). Without -lsetjmp those stay UNDEFINED and the transpiler
	// surfaces them as no-op host imports, so a longjmp silently falls through
	// to the `unreachable` the compiler placed after it and traps at run time
	// (this is how a guest's longjmp-based unwinding is realised at run time). -l is
	// harmless when unreferenced: the linker only pulls archive members that
	// something actually calls, so projects with no setjmp/longjmp are
	// unaffected. It must come AFTER the archives that reference it.
	args = append(args, "-lsetjmp")

	fmt.Fprintf(os.Stderr, "[wasm-build] Linking library: %s (%d archives, %d extra objects)\n",
		targetName+".wasm", len(archives), len(extraObjects))

	// Log stack-size flag for debugging
	for _, a := range args {
		if strings.Contains(a, "stack-size") {
			fmt.Fprintf(os.Stderr, "[wasm-build] Link flag: %s\n", a)
		}
	}

	cmd := exec.Command(compiler, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("library link failed: %w", err)
	}

	// Report file size
	if info, err := os.Stat(outputFile); err == nil {
		sizeMB := float64(info.Size()) / (1024 * 1024)
		fmt.Fprintf(os.Stderr, "[wasm-build] Output: %s (%.1f MB)\n", outputFile, sizeMB)
	}

	return outputFile, nil
}
