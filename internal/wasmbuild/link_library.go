package wasmbuild

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// LinkLibrary creates a single .wasm from all successfully built .a files
// in the build directory. This is used for library targets that have no
// native link step (e.g., Bazel cc_library targets).
// extraObjects are additional .o files to include (e.g., api_bridge.o).
func LinkLibrary(targetName string, cfg WasmConfig, extraObjects []string) (string, error) {
	libDir := filepath.Join(cfg.BuildDir, "lib")
	entries, err := os.ReadDir(libDir)
	if err != nil {
		return "", fmt.Errorf("failed to read lib directory: %w", err)
	}

	var archives []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// Bazel records cc_library archives with either `.a` or `.lo`
		// (libtool-style "library object") extensions; both contain
		// wasm32-wasip1 .o files after wasm-build replay. Pick up
		// either so libraries that bazel ships exclusively in `.lo`
		// form (e.g. some googlesql public types) end up linked into
		// the final wasm.
		name := e.Name()
		if strings.HasSuffix(name, ".a") || strings.HasSuffix(name, ".lo") {
			archives = append(archives, filepath.Join(libDir, name))
		}
	}
	if len(archives) == 0 {
		return "", fmt.Errorf("no .a/.lo archives found in %s", libDir)
	}

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

	// Add extra objects (e.g., bridge code) before archives. These
	// objects carry the per-method `w_<svc>_<mid>` and
	// `wasmify_get_type_name` exports — the references they make into
	// the archive libraries are what pull the needed code in.
	args = append(args, extraObjects...)
	args = append(args, archives...)

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
