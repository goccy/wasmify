package wasmbuild

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLinkLibrary_NoLibDir(t *testing.T) {
	tmp := t.TempDir()
	cfg := WasmConfig{
		WasiSDKPath: "/opt/wasi-sdk",
		Target:      "wasm32-wasip1",
		BuildDir:    tmp, // no lib/ subdir
	}
	_, err := LinkLibrary("mylib", cfg, nil)
	if err == nil {
		t.Error("expected error when lib dir missing")
	}
}

func TestLinkLibrary_NoArchives(t *testing.T) {
	tmp := t.TempDir()
	// Create empty lib dir
	libDir := filepath.Join(tmp, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := WasmConfig{
		WasiSDKPath: "/opt/wasi-sdk",
		Target:      "wasm32-wasip1",
		BuildDir:    tmp,
	}
	_, err := LinkLibrary("mylib", cfg, nil)
	if err == nil {
		t.Error("expected error for empty lib dir")
	}
}
