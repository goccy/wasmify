package wasmbuild

import (
	"strings"
	"testing"
)

// The wasm64 option must swap the target triple for every compile and
// link — the triple is what selects the wasm64 sysroot libraries — and
// must be settable through the WASMIFY_WASM64 environment mirror like
// every other build option.
func TestWasm64TargetSwap(t *testing.T) {
	cfg := WasmConfig{Target: "wasm32-wasip1", Wasm64: true}
	if got := effectiveTarget(cfg); got != "wasm64-wasip1" {
		t.Errorf("effectiveTarget with Wasm64 = %q, want wasm64-wasip1", got)
	}
	cfg.Wasm64 = false
	if got := effectiveTarget(cfg); got != "wasm32-wasip1" {
		t.Errorf("effectiveTarget without Wasm64 = %q, want wasm32-wasip1", got)
	}

	cfg.Wasm64 = true
	compile := strings.Join(wasmCompileFlags(cfg), " ")
	if !strings.Contains(compile, "--target=wasm64-wasip1") {
		t.Errorf("compile flags missing the wasm64 triple: %s", compile)
	}
	link := strings.Join(wasmLinkFlags(cfg), " ")
	if !strings.Contains(link, "--target=wasm64-wasip1") {
		t.Errorf("link flags missing the wasm64 triple: %s", link)
	}
}

func TestWasm64EnvOverride(t *testing.T) {
	t.Setenv("WASMIFY_WASM64", "1")
	cfg := WasmConfig{Target: "wasm32-wasip1"}
	cfg.ApplyEnvOverrides()
	if !cfg.Wasm64 {
		t.Error("WASMIFY_WASM64 did not enable Wasm64")
	}
}
