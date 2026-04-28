package wasmbuild

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DetectWasiSDK finds the wasi-sdk installation path.
// It checks in order: explicit path, $WASI_SDK_PATH, common install locations.
func DetectWasiSDK(explicit string) (string, error) {
	candidates := []string{}

	if explicit != "" {
		candidates = append(candidates, explicit)
	}
	if env := os.Getenv("WASI_SDK_PATH"); env != "" {
		candidates = append(candidates, env)
	}
	candidates = append(candidates,
		"/opt/wasi-sdk",
		"/usr/local/wasi-sdk",
	)

	// Also check for versioned directories
	for _, prefix := range []string{"/opt", "/usr/local"} {
		entries, err := os.ReadDir(prefix)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() && strings.HasPrefix(e.Name(), "wasi-sdk-") {
				candidates = append(candidates, filepath.Join(prefix, e.Name()))
			}
		}
	}

	for _, path := range candidates {
		if err := validateWasiSDK(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("wasi-sdk not found. Install it or set WASI_SDK_PATH. Checked: %s", strings.Join(candidates, ", "))
}

// validateWasiSDK checks that the given path contains the required wasi-sdk tools.
func validateWasiSDK(sdkPath string) error {
	info, err := os.Stat(sdkPath)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("not a directory: %s", sdkPath)
	}

	requiredTools := []string{
		filepath.Join("bin", "clang"),
		filepath.Join("bin", "clang++"),
		filepath.Join("bin", "llvm-ar"),
		filepath.Join("bin", "wasm-ld"),
	}

	for _, tool := range requiredTools {
		toolPath := filepath.Join(sdkPath, tool)
		if _, err := os.Stat(toolPath); err != nil {
			return fmt.Errorf("missing tool: %s", toolPath)
		}
	}

	sysroot := filepath.Join(sdkPath, "share", "wasi-sysroot")
	if _, err := os.Stat(sysroot); err != nil {
		return fmt.Errorf("missing sysroot: %s", sysroot)
	}

	return nil
}

// WasiSDKVersion returns the clang version from the wasi-sdk.
func WasiSDKVersion(sdkPath string) string {
	clang := filepath.Join(sdkPath, "bin", "clang")
	out, err := exec.Command(clang, "--version").Output()
	if err != nil {
		return "unknown"
	}
	lines := strings.Split(string(out), "\n")
	if len(lines) > 0 {
		return strings.TrimSpace(lines[0])
	}
	return "unknown"
}

// Sysroot returns the sysroot path for the given wasi-sdk.
func Sysroot(sdkPath string) string {
	return filepath.Join(sdkPath, "share", "wasi-sysroot")
}
