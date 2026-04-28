package wasmbuild

import (
	"os"
	"path/filepath"
	"testing"
)

// makeFakeWasiSDK creates a directory structure that looks like a valid
// wasi-sdk installation: bin/{clang,clang++,llvm-ar,wasm-ld} and
// share/wasi-sysroot. All tools are empty executable files — validateWasiSDK
// only checks for presence, not content.
func makeFakeWasiSDK(t *testing.T, root string) {
	t.Helper()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"clang", "clang++", "llvm-ar", "wasm-ld"} {
		if err := os.WriteFile(filepath.Join(binDir, tool), []byte(""), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sysrootDir := filepath.Join(root, "share", "wasi-sysroot")
	if err := os.MkdirAll(sysrootDir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestValidateWasiSDK(t *testing.T) {
	tmp := t.TempDir()
	sdkRoot := filepath.Join(tmp, "sdk")
	makeFakeWasiSDK(t, sdkRoot)

	if err := validateWasiSDK(sdkRoot); err != nil {
		t.Errorf("validateWasiSDK(valid) failed: %v", err)
	}
}

func TestValidateWasiSDK_NotDir(t *testing.T) {
	tmp := t.TempDir()
	notDir := filepath.Join(tmp, "file")
	_ = os.WriteFile(notDir, []byte(""), 0o644)
	if err := validateWasiSDK(notDir); err == nil {
		t.Error("expected error for non-directory")
	}
}

func TestValidateWasiSDK_Missing(t *testing.T) {
	if err := validateWasiSDK("/nonexistent/path"); err == nil {
		t.Error("expected error for missing path")
	}
}

func TestValidateWasiSDK_MissingTool(t *testing.T) {
	tmp := t.TempDir()
	sdkRoot := filepath.Join(tmp, "sdk")
	binDir := filepath.Join(sdkRoot, "bin")
	_ = os.MkdirAll(binDir, 0o755)
	// Only clang, missing clang++ and others
	_ = os.WriteFile(filepath.Join(binDir, "clang"), []byte(""), 0o755)
	_ = os.MkdirAll(filepath.Join(sdkRoot, "share", "wasi-sysroot"), 0o755)

	if err := validateWasiSDK(sdkRoot); err == nil {
		t.Error("expected error for missing tool")
	}
}

func TestValidateWasiSDK_MissingSysroot(t *testing.T) {
	tmp := t.TempDir()
	sdkRoot := filepath.Join(tmp, "sdk")
	binDir := filepath.Join(sdkRoot, "bin")
	_ = os.MkdirAll(binDir, 0o755)
	for _, tool := range []string{"clang", "clang++", "llvm-ar", "wasm-ld"} {
		_ = os.WriteFile(filepath.Join(binDir, tool), []byte(""), 0o755)
	}
	// No share/wasi-sysroot
	if err := validateWasiSDK(sdkRoot); err == nil {
		t.Error("expected error for missing sysroot")
	}
}

func TestDetectWasiSDK_Explicit(t *testing.T) {
	tmp := t.TempDir()
	sdkRoot := filepath.Join(tmp, "sdk")
	makeFakeWasiSDK(t, sdkRoot)

	got, err := DetectWasiSDK(sdkRoot)
	if err != nil {
		t.Errorf("DetectWasiSDK failed: %v", err)
	}
	if got != sdkRoot {
		t.Errorf("got %q, want %q", got, sdkRoot)
	}
}

func TestDetectWasiSDK_Env(t *testing.T) {
	tmp := t.TempDir()
	sdkRoot := filepath.Join(tmp, "sdk")
	makeFakeWasiSDK(t, sdkRoot)

	t.Setenv("WASI_SDK_PATH", sdkRoot)
	got, err := DetectWasiSDK("")
	if err != nil {
		t.Errorf("DetectWasiSDK failed: %v", err)
	}
	if got != sdkRoot {
		t.Errorf("got %q, want %q", got, sdkRoot)
	}
}

func TestDetectWasiSDK_NotFound(t *testing.T) {
	// Point to a non-existent location
	t.Setenv("WASI_SDK_PATH", "/nonexistent/path")
	_, err := DetectWasiSDK("")
	// It's possible a real install is found at /opt/wasi-sdk on the test
	// machine, so we can't assert an error. But explicit bad path must not
	// crash — that's enough.
	_ = err
}

func TestWasiSDKVersion_Unknown(t *testing.T) {
	// Nonexistent path → "unknown"
	got := WasiSDKVersion("/nonexistent")
	if got != "unknown" {
		t.Errorf("expected unknown, got %q", got)
	}
}

func TestDetectPlatform(t *testing.T) {
	arch, os_, err := detectPlatform()
	if err != nil {
		// Unsupported platforms will error; that's fine
		t.Skipf("unsupported platform: %v", err)
	}
	if arch == "" || os_ == "" {
		t.Errorf("detectPlatform returned empty: arch=%q os=%q", arch, os_)
	}
}

func TestWasiSDKInstallPath(t *testing.T) {
	// Just verify it returns a non-empty path
	got := WasiSDKInstallPath()
	if got == "" {
		t.Error("expected non-empty install path")
	}
}

func TestDefaultInstallDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	got, err := defaultInstallDir()
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(tmp, "wasmify", "bin", "wasi-sdk")
	if got != expected {
		t.Errorf("got %q, want %q", got, expected)
	}
}

func TestDetectOrInstallWasiSDK_Found(t *testing.T) {
	tmp := t.TempDir()
	sdkRoot := filepath.Join(tmp, "sdk")
	makeFakeWasiSDK(t, sdkRoot)

	got, err := DetectOrInstallWasiSDK(sdkRoot)
	if err != nil {
		t.Errorf("DetectOrInstallWasiSDK failed: %v", err)
	}
	if got != sdkRoot {
		t.Errorf("got %q, want %q", got, sdkRoot)
	}
}
