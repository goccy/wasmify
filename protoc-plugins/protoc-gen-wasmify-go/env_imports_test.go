package main

import (
	"strings"
	"testing"
)

// A wasm whose imports are all wasi_snapshot_preview1 — a self-contained engine
// with host sockets and subprocess off — names no "env" module, so the
// transpiler emits neither an EnvImports interface nor a New that takes one.
// The bridge must then not declare envStubs, not construct one, and not pass it
// to New; each of those would fail to compile against the engine that was
// actually generated.
func TestGenerateModule_NoEnvImports(t *testing.T) {
	prevEnv, prevWasmify, prevPath := wasm2goHasEnv, wasm2goHasWasmify, wasm2goImportPath
	t.Cleanup(func() {
		wasm2goHasEnv, wasm2goHasWasmify, wasm2goImportPath = prevEnv, prevWasmify, prevPath
	})
	cfgPrev := cfg
	t.Cleanup(func() { cfg = cfgPrev })
	cfg.runtime = "wasm2go"
	wasm2goImportPath = "example.com/enginewasm2go"
	wasm2goHasWasmify = false

	wasm2goHasEnv = false
	got := generateModule("engine")
	if !strings.Contains(got, "wasm2go.New()") {
		t.Errorf("New must take no arguments when the wasm has no env imports:\n%s", excerpt(got))
	}
	if strings.Contains(got, "envStubs") {
		t.Errorf("envStubs must not appear when the wasm has no env imports:\n%s", excerpt(got))
	}

	// With env imports the receiver comes back, and New takes it.
	wasm2goHasEnv = true
	got = generateModule("engine")
	if !strings.Contains(got, "wasm2go.New(env)") {
		t.Errorf("New must take env when the wasm imports it:\n%s", excerpt(got))
	}
	if !strings.Contains(got, "type envStubs struct{ m *Module }") {
		t.Errorf("envStubs declaration missing:\n%s", excerpt(got))
	}
	if !strings.Contains(got, "env := envStubs{m: m}") {
		t.Errorf("envStubs must be constructed in initModule:\n%s", excerpt(got))
	}
}

// generateEnvStubs reads the method set off the EnvImports interface. Without
// one there is nothing to satisfy, so it must stay quiet rather than fail the
// whole generation.
func TestGenerateEnvStubs_NoInterface(t *testing.T) {
	prev := wasm2goHasEnv
	t.Cleanup(func() { wasm2goHasEnv = prev })

	wasm2goHasEnv = false
	got, err := generateEnvStubs([]byte("package base\n"))
	if err != nil {
		t.Fatalf("generateEnvStubs: %v", err)
	}
	if got != "" {
		t.Errorf("expected no stubs, got:\n%s", got)
	}

	// The interface really being absent while the detector says otherwise is a
	// generator bug, and must still be reported rather than silently skipped.
	wasm2goHasEnv = true
	if _, err := generateEnvStubs([]byte("package base\n")); err == nil {
		t.Error("expected an error when EnvImports is declared missing but expected")
	}
}

func excerpt(s string) string {
	if i := strings.Index(s, "func initModule"); i >= 0 {
		end := i + 400
		if end > len(s) {
			end = len(s)
		}
		return s[i:end]
	}
	if len(s) > 400 {
		return s[:400]
	}
	return s
}
