// Package binaryen wraps the install + invocation of Binaryen tools
// (wasm-opt, wasm-strip-like passes) used by `wasmify optimize` to
// shrink a built wasm artefact post-link. The pattern mirrors the
// wasi-sdk auto-install in internal/wasmbuild/install.go: download a
// pinned release tarball into ~/.config/wasmify/bin/binaryen/ on
// first use, then reuse it forever.
package binaryen

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

const (
	// Version is the Binaryen release wasmify pins to. Bumping this
	// constant invalidates every machine's local install on next
	// invocation (DetectOrInstall checks the version file).
	Version = "version_119"

	// githubURL is the Binaryen release tarball template:
	// binaryen-<version>-<arch>-<os>.tar.gz
	githubURL = "https://github.com/WebAssembly/binaryen/releases/download/%s/binaryen-%s-%s-%s.tar.gz"
)

// defaultInstallDir returns ~/.config/wasmify/bin/binaryen, the
// shared install location every wasmify project reuses.
func defaultInstallDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "wasmify", "bin", "binaryen"), nil
}

// versionFile sits inside the install dir and records the Binaryen
// version that was unpacked there. DetectOrInstall reads it to decide
// whether the cached tree is still current.
func versionFile(installDir string) string {
	return filepath.Join(installDir, ".wasmify-version")
}

// WasmOptPath returns the absolute path to wasm-opt inside an
// install directory.
func WasmOptPath(installDir string) string {
	return filepath.Join(installDir, "bin", "wasm-opt")
}

// validate succeeds when wasm-opt and the version stamp both look
// healthy; failure means we should re-install.
func validate(installDir string) error {
	if _, err := os.Stat(WasmOptPath(installDir)); err != nil {
		return fmt.Errorf("wasm-opt missing at %s: %w", WasmOptPath(installDir), err)
	}
	data, err := os.ReadFile(versionFile(installDir))
	if err != nil {
		return fmt.Errorf("read version stamp: %w", err)
	}
	if got := string(data); got != Version {
		return fmt.Errorf("binaryen version mismatch: have %q, want %q", got, Version)
	}
	return nil
}

// detectPlatform maps Go's runtime to Binaryen release filename
// fragments.
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

// Install downloads and unpacks the pinned Binaryen release into
// installDir. When installDir is empty the default XDG path is used.
// Returns the directory that was populated.
func Install(installDir string) (string, error) {
	if installDir == "" {
		var err error
		installDir, err = defaultInstallDir()
		if err != nil {
			return "", err
		}
	}
	arch, osName, err := detectPlatform()
	if err != nil {
		return "", err
	}
	url := fmt.Sprintf(githubURL, Version, Version, arch, osName)
	fmt.Fprintf(os.Stderr, "[optimize] Downloading binaryen %s for %s-%s...\n", Version, arch, osName)
	fmt.Fprintf(os.Stderr, "[optimize] URL: %s\n", url)

	tmpFile, err := os.CreateTemp("", "binaryen-*.tar.gz")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	defer func() { _ = tmpFile.Close() }()

	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("download binaryen: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("binaryen download failed: HTTP %d (%s)", resp.StatusCode, url)
	}
	size, err := io.Copy(tmpFile, resp.Body)
	if err != nil {
		return "", fmt.Errorf("save binaryen tarball: %w", err)
	}
	_ = tmpFile.Close()
	fmt.Fprintf(os.Stderr, "[optimize] Downloaded %d MB\n", size/(1024*1024))

	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return "", fmt.Errorf("create install dir %s: %w", installDir, err)
	}
	cmd := exec.Command("tar", "xzf", tmpFile.Name(), "--strip-components=1", "-C", installDir)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("extract binaryen: %w", err)
	}
	if err := os.WriteFile(versionFile(installDir), []byte(Version), 0o644); err != nil {
		return "", fmt.Errorf("write version stamp: %w", err)
	}
	if err := validate(installDir); err != nil {
		return "", fmt.Errorf("post-install validation: %w", err)
	}
	fmt.Fprintf(os.Stderr, "[optimize] Installed binaryen %s to %s\n", Version, installDir)
	return installDir, nil
}

// DetectOrInstall returns the path to a Binaryen install directory.
// Resolution order:
//
//  1. wasm-opt found on $PATH → return its parent's parent (so the
//     caller can locate companion tools beside it).
//  2. The shared XDG dir (~/.config/wasmify/bin/binaryen) if it
//     validates.
//  3. Fresh download into the XDG dir.
//
// The returned directory always has a working wasm-opt at
// <dir>/bin/wasm-opt by the time the function succeeds.
func DetectOrInstall() (string, error) {
	if path, err := exec.LookPath("wasm-opt"); err == nil {
		// Walk up from <bin>/wasm-opt to <root>.
		return filepath.Dir(filepath.Dir(path)), nil
	}
	if dir, err := defaultInstallDir(); err == nil {
		if err := validate(dir); err == nil {
			return dir, nil
		}
	}
	return Install("")
}
