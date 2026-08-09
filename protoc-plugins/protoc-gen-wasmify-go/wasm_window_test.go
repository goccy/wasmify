package main

import (
	"regexp"
	"strings"
	"testing"
)

// Wasm pointers are unsigned: a wasm32 pointer past the 2 GiB line has
// a negative Go int32 form, so a signed slice index panics — observed
// as `slice bounds out of range [-…:]` in long-lived consumers once
// the module's memory grows beyond 2 GiB. Every guest-pointer slice
// index in the bridge template must therefore go through wptrOff (the
// width-specific unsigned widening defined in the result codecs)
// before touching linear memory; only decodeResult's already-unsigned
// uint64 outputs may index directly.
func TestBridgePointersWidenPast2GiB(t *testing.T) {
	for name, codec := range map[string]string{
		"wasm32": wasm32ResultCodec,
		"wasm64": wasm64ResultCodec,
	} {
		if !strings.Contains(codec, "func wptrOff(p wptr) uint64") {
			t.Errorf("%s codec lost the wptrOff widening helper", name)
		}
	}
	for _, body := range []string{moduleBodyWasm2go, callbackInfraWasm2go, wasm32ResultCodec, wasm64ResultCodec} {
		// A wptr-typed value must never be a raw slice index. respPtr /
		// respLen (decodeResult outputs) are uint64 and exempt.
		for _, banned := range []string{
			"[reqPtr:",
			"[ptr:",
			"[m.cbDesc:",
		} {
			if strings.Contains(body, banned) {
				t.Errorf("template slices wasm memory with a raw wptr (%q); widen through wptrOff", banned)
			}
		}
		// Direct Memory(...)[...] indexing is allowed only when the index
		// expression is the widening helper or decodeResult's already-
		// unsigned respPtr.
		raw := regexp.MustCompile(`Memory\((?:m\.g|g)\)\[(wptrOff\(|respPtr)?`)
		for _, m := range raw.FindAllStringSubmatch(body, -1) {
			if m[1] == "" {
				t.Errorf("template indexes wasm memory with a raw pointer (%q); widen through wptrOff", m[0])
			}
		}
	}
}
