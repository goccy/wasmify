package wasmbuild

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/wasmify/internal/buildjson"
)

func TestInjectBridgeSteps_NoBridge(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := WasmConfig{
		WasiSDKPath: "/opt/wasi-sdk",
		Target:      "wasm32-wasi",
		BuildDir:    tmpDir,
	}

	steps := []WasmBuildStep{
		{ID: 1, Type: buildjson.StepCompile, Args: []string{"-c", "test.cc"}},
		{ID: 2, Type: buildjson.StepLink, Args: []string{"-o", "out.wasm"}},
	}

	// No api_bridge.cc → steps unchanged
	result := InjectBridgeSteps(steps, cfg, filepath.Join(tmpDir, "src"))
	if len(result) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(result))
	}
}

func TestInjectBridgeSteps_WithBridge(t *testing.T) {
	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "src")
	_ = os.MkdirAll(srcDir, 0o755)
	_ = os.WriteFile(filepath.Join(srcDir, "api_bridge.cc"), []byte("// bridge"), 0o644)

	cfg := WasmConfig{
		WasiSDKPath: "/opt/wasi-sdk",
		Target:      "wasm32-wasi",
		BuildDir:    tmpDir,
	}

	steps := []WasmBuildStep{
		{ID: 1, Type: buildjson.StepCompile, Args: []string{"-c", "-I/usr/include", "test.cc"}, OutputFile: filepath.Join(tmpDir, "obj", "test.o")},
		{ID: 2, Type: buildjson.StepLink, Args: []string{"-o", filepath.Join(tmpDir, "output", "out.wasm")}, OutputFile: filepath.Join(tmpDir, "output", "out.wasm")},
	}

	result := InjectBridgeSteps(steps, cfg, srcDir)

	// Should have 3 steps: original compile + bridge compile + link
	if len(result) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(result))
	}

	// Find bridge compile step
	var bridgeStep *WasmBuildStep
	for i := range result {
		if result[i].OutputFile == filepath.Join(tmpDir, "obj", "api_bridge.o") {
			bridgeStep = &result[i]
			break
		}
	}
	if bridgeStep == nil {
		t.Fatal("bridge compile step not found")
	}
	if bridgeStep.Type != buildjson.StepCompile {
		t.Fatalf("expected compile step, got %s", bridgeStep.Type)
	}

	// Link step should include bridge object
	var linkStep *WasmBuildStep
	for i := range result {
		if result[i].Type == buildjson.StepLink {
			linkStep = &result[i]
			break
		}
	}
	if linkStep == nil {
		t.Fatal("link step not found")
	}
	found := false
	for _, arg := range linkStep.Args {
		if arg == filepath.Join(tmpDir, "obj", "api_bridge.o") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("bridge object not added to link step args")
	}
}

func TestExtractIncludeFlags(t *testing.T) {
	steps := []WasmBuildStep{
		{
			Type: buildjson.StepCompile,
			Args: []string{"-c", "-I/usr/include", "-I/usr/local/include", "-isystem", "/opt/include", "test.cc"},
		},
		{
			Type: buildjson.StepCompile,
			Args: []string{"-c", "-I/usr/include", "-I/other/include", "test2.cc"},
		},
		{
			Type:    buildjson.StepCompile,
			Args:    []string{"-c", "-I/skipped/include", "test3.cc"},
			Skipped: true,
		},
	}

	flags := extractIncludeFlags(steps)

	// Should include unique flags only, not from skipped steps
	expected := map[string]bool{
		"-I/usr/include":       true,
		"-I/usr/local/include": true,
		"-isystem":             true,
		"/opt/include":         true,
		"-I/other/include":     true,
	}

	for _, f := range flags {
		delete(expected, f)
	}
	if len(expected) > 0 {
		t.Fatalf("missing expected flags: %v", expected)
	}
}

func TestWasmCompileFlagsIncludesEmscripten(t *testing.T) {
	cfg := WasmConfig{
		WasiSDKPath: "/fake/wasi-sdk",
		Target:      "wasm32-wasip1",
	}
	flags := wasmCompileFlags(cfg)
	found := false
	for _, f := range flags {
		if f == "-D__EMSCRIPTEN__" {
			found = true
			break
		}
	}
	if !found {
		t.Error("wasmCompileFlags should include -D__EMSCRIPTEN__")
	}
}

func TestWasmLinkFlagsIncludesStackSize(t *testing.T) {
	cfg := WasmConfig{
		WasiSDKPath: "/fake/wasi-sdk",
		Target:      "wasm32-wasip1",
	}
	flags := wasmLinkFlags(cfg)
	found := false
	for _, f := range flags {
		if strings.Contains(f, "stack-size") {
			found = true
			break
		}
	}
	if !found {
		t.Error("wasmLinkFlags should include stack-size")
	}
}
