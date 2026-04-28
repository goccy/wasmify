package wasmbuild

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	wasiSDKVersion   = "31"
	wasiSDKMinorVer  = "31.0"
	wasiSDKGitHubURL = "https://github.com/WebAssembly/wasi-sdk/releases/download/wasi-sdk-%s/wasi-sdk-%s-%s-%s.tar.gz"

	// WasiSDKPinnedVersion is the wasi-sdk version this wasmify build
	// installs. Exposed so generated scaffolds can use it as a cache key.
	WasiSDKPinnedVersion = wasiSDKMinorVer
)

// defaultInstallDir returns the default wasi-sdk install path under ~/.config/wasmify/bin/wasi-sdk.
func defaultInstallDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "wasmify", "bin", "wasi-sdk"), nil
}

// InstallWasiSDK downloads and installs WASI SDK to the specified directory.
// If installDir is empty, defaults to ~/.config/wasmify/bin/wasi-sdk.
func InstallWasiSDK(installDir string) (string, error) {
	if installDir == "" {
		var err error
		installDir, err = defaultInstallDir()
		if err != nil {
			return "", err
		}
	}

	// Check if already installed
	if err := validateWasiSDK(installDir); err == nil {
		return installDir, fmt.Errorf("wasi-sdk already installed at %s", installDir)
	}

	arch, osName, err := detectPlatform()
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf(wasiSDKGitHubURL, wasiSDKVersion, wasiSDKMinorVer, arch, osName)
	fmt.Fprintf(os.Stderr, "[install-sdk] Downloading wasi-sdk %s for %s-%s...\n", wasiSDKMinorVer, arch, osName)
	fmt.Fprintf(os.Stderr, "[install-sdk] URL: %s\n", url)

	// Download to temp file
	tmpFile, err := os.CreateTemp("", "wasi-sdk-*.tar.gz")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	defer func() { _ = tmpFile.Close() }()

	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	size, err := io.Copy(tmpFile, resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to save download: %w", err)
	}
	_ = tmpFile.Close()
	fmt.Fprintf(os.Stderr, "[install-sdk] Downloaded %d MB\n", size/(1024*1024))

	// Create install directory
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create install directory %s: %w\nTry: sudo mkdir -p %s && sudo chown $USER %s", installDir, err, installDir, installDir)
	}

	// Extract
	fmt.Fprintf(os.Stderr, "[install-sdk] Extracting to %s...\n", installDir)
	cmd := exec.Command("tar", "xzf", tmpFile.Name(), "--strip-components=1", "-C", installDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to extract: %w", err)
	}

	// Validate
	if err := validateWasiSDK(installDir); err != nil {
		return "", fmt.Errorf("installation validation failed: %w", err)
	}

	fmt.Fprintf(os.Stderr, "[install-sdk] Successfully installed wasi-sdk %s to %s\n", wasiSDKMinorVer, installDir)
	return installDir, nil
}

func detectPlatform() (arch, osName string, err error) {
	switch runtime.GOARCH {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "arm64"
	default:
		return "", "", fmt.Errorf("unsupported architecture: %s", runtime.GOARCH)
	}

	switch runtime.GOOS {
	case "darwin":
		osName = "macos"
	case "linux":
		osName = "linux"
	default:
		return "", "", fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}

	return arch, osName, nil
}

// WasiSDKInstallPath returns the default install path for the SDK.
func WasiSDKInstallPath() string {
	dir, err := defaultInstallDir()
	if err != nil {
		return "/opt/wasi-sdk"
	}
	return dir
}

// DetectOrInstallWasiSDK tries to detect an existing SDK at the shared XDG
// install path (~/.config/wasmify/bin/wasi-sdk), explicit overrides, or
// common $HOME locations (wasi-sdk-X.Y). The SDK is a toolchain shared
// across every project wasmify builds.
func DetectOrInstallWasiSDK(explicit string) (string, error) {
	if sdkPath, err := DetectWasiSDK(explicit); err == nil {
		return sdkPath, nil
	}

	// Shared XDG install location.
	if defDir, err := defaultInstallDir(); err == nil {
		if err := validateWasiSDK(defDir); err == nil {
			return defDir, nil
		}
	}

	// Ad-hoc home directory installs (e.g., ~/wasi-sdk-31.0).
	home, _ := os.UserHomeDir()
	if home != "" {
		candidates := []string{filepath.Join(home, "wasi-sdk")}
		entries, _ := os.ReadDir(home)
		for _, e := range entries {
			if e.IsDir() && strings.HasPrefix(e.Name(), "wasi-sdk-") {
				candidates = append(candidates, filepath.Join(home, e.Name()))
			}
		}
		for _, p := range candidates {
			if err := validateWasiSDK(p); err == nil {
				return p, nil
			}
		}
	}

	return "", fmt.Errorf("wasi-sdk not found. Run 'wasmify install-sdk' to install it")
}
