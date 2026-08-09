package main

import (
	"strings"
	"testing"
)

// An engine that imports wasi_snapshot_preview1 exposes NewWithWASI /
// NewWithWASIReserve, so the bridge must offer Options.WASI (a sandboxed host
// interface) and Options.MemoryReserveBytes (a pre-sized linear memory) and
// route the construction through them. An engine without that import has
// neither constructor, and referencing either would not compile — the bridge
// must fall back to plain New and drop the two fields from Options.
func TestGenerateModule_InitOptions(t *testing.T) {
	prevEnv, prevWasmify, prevWasi, prevPath := wasm2goHasEnv, wasm2goHasWasmify, wasm2goHasWasi, wasm2goImportPath
	t.Cleanup(func() {
		wasm2goHasEnv, wasm2goHasWasmify, wasm2goHasWasi, wasm2goImportPath = prevEnv, prevWasmify, prevWasi, prevPath
	})
	cfgPrev := cfg
	t.Cleanup(func() { cfg = cfgPrev })
	cfg.runtime = "wasm2go"
	wasm2goImportPath = "example.com/enginewasm2go"
	wasm2goHasWasmify = false
	wasm2goHasEnv = true

	wasm2goHasWasi = true
	got := generateModule("engine")
	for _, want := range []string{
		"func Init() error { return InitWith(Options{}) }",
		"func InitWith(opts Options) error",
		"WASI base.Wasi_snapshot_preview1Imports",
		"MemoryReserveBytes int",
		"wasi = base.DefaultWASI()",
		// The engine's constructor takes one parameter per import module in
		// wasm2go's fixed wasi, env, wasmify order.
		"wasm2go.NewWithWASIReserve(wasi, env, opts.MemoryReserveBytes)",
		"wasm2go.NewWithWASI(wasi, env)",
		"wasm2go.SetMaxMemory(m.g, opts.MaxMemoryBytes)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("WASI-importing engine: missing %q:\n%s", want, excerpt(got))
		}
	}

	wasm2goHasWasi = false
	got = generateModule("engine")
	for _, want := range []string{
		"func InitWith(opts Options) error",
		"wasm2go.New(env)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("engine without WASI: missing %q:\n%s", want, excerpt(got))
		}
	}
	for _, unwanted := range []string{
		"base.Wasi_snapshot_preview1Imports",
		"base.DefaultWASI()",
		"MemoryReserveBytes",
		"NewWithWASIReserve",
	} {
		if strings.Contains(got, unwanted) {
			t.Errorf("engine without WASI must not reference %q:\n%s", unwanted, excerpt(got))
		}
	}
}

// Instance() is how an embedder reaches linear memory from a goroutine other
// than the one running a call — base.AccessMemory's use case. It must be
// emitted whatever the engine's import set.
func TestGenerateModule_InstanceAccessor(t *testing.T) {
	prevEnv, prevWasmify, prevWasi, prevPath := wasm2goHasEnv, wasm2goHasWasmify, wasm2goHasWasi, wasm2goImportPath
	t.Cleanup(func() {
		wasm2goHasEnv, wasm2goHasWasmify, wasm2goHasWasi, wasm2goImportPath = prevEnv, prevWasmify, prevWasi, prevPath
	})
	cfgPrev := cfg
	t.Cleanup(func() { cfg = cfgPrev })
	cfg.runtime = "wasm2go"
	wasm2goImportPath = "example.com/enginewasm2go"
	wasm2goHasWasmify = false

	for _, hasWasi := range []bool{true, false} {
		wasm2goHasWasi = hasWasi
		wasm2goHasEnv = hasWasi
		got := generateModule("engine")
		if !strings.Contains(got, "func Instance() *base.Module {") {
			t.Errorf("hasWasi=%v: Instance accessor missing:\n%s", hasWasi, excerpt(got))
		}
	}
}

// The growth ceiling is a Module field wasm2go spells MaxMem in
// multi-package mode and maxMem in single-package mode, so the setter has to
// be emitted into the engine package rather than called from the bridge.
func TestWasm2GoMaxMemSetter(t *testing.T) {
	single := wasm2goMaxMemSetter(true, "example.com/enginewasm2go")
	if !strings.Contains(single, "func SetMaxMemory(m *Module, n uint64) { m.maxMem = n }") {
		t.Errorf("single-package setter must reach the unexported field:\n%s", single)
	}
	if strings.Contains(single, "import") {
		t.Errorf("single-package setter needs no import:\n%s", single)
	}

	multi := wasm2goMaxMemSetter(false, "example.com/enginewasm2go")
	if !strings.Contains(multi, `import base "example.com/enginewasm2go/base"`) {
		t.Errorf("multi-package setter must import base:\n%s", multi)
	}
	if !strings.Contains(multi, "func SetMaxMemory(m *base.Module, n uint64) { m.MaxMem = n }") {
		t.Errorf("multi-package setter must use the exported field:\n%s", multi)
	}
}
