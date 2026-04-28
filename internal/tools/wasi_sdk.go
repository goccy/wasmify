package tools

import (
	"fmt"
	"os"

	"github.com/goccy/wasmify/internal/wasmbuild"
)

// EnsureWasiSDK detects or installs the wasi-sdk at the shared XDG location
// (~/.config/wasmify/bin/wasi-sdk). Unlike other build artefacts which live
// inside the project's .wasmify/ dir, the SDK is installed once per machine
// and shared across every project that wasmify builds — it is a ~500MB
// toolchain that does not change between projects.
func EnsureWasiSDK() error {
	if _, err := wasmbuild.DetectOrInstallWasiSDK(""); err == nil {
		fmt.Fprintf(os.Stderr, "[ensure-tools] wasi-sdk: already installed\n")
		return nil
	}
	fmt.Fprintf(os.Stderr, "[ensure-tools] wasi-sdk: installing...\n")
	if _, err := wasmbuild.InstallWasiSDK(""); err != nil {
		return fmt.Errorf("wasi-sdk install failed: %w", err)
	}
	return nil
}
