package main

import (
	"regexp"
	"strings"
	"testing"
)

// Wasm pointers are unsigned i32s: past the 2 GiB line their Go
// int32 form is negative, so a signed slice index panics — observed
// as `slice bounds out of range [-…:]` in long-lived consumers once
// the module's memory grows beyond 2 GiB — and 32-bit ptr+len sums
// can wrap. The wasm2go template must route every memory window
// through wasmWindow, which widens before slicing.
func TestBridgePointersWidenPast2GiB(t *testing.T) {
	body := moduleBodyWasm2go
	if !strings.Contains(body, "func wasmWindow(mem []byte, ptr uint32, n uint32) []byte") {
		t.Fatal("template lost the wasmWindow helper")
	}
	for _, banned := range []string{
		"[reqPtr:",
		"[respPtr:",
		"[ptr:",
	} {
		if strings.Contains(body, banned) {
			t.Errorf("template slices wasm memory with a raw pointer (%q); use wasmWindow",
				banned)
		}
	}
	// Every memory window must come from the widening helper.
	rawSlice := regexp.MustCompile(`Memory\(m\.g\)\[`)
	if loc := rawSlice.FindString(body); loc != "" {
		t.Errorf("template indexes Memory(m.g) directly; use wasmWindow")
	}
}
