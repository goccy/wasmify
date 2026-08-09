package wasmbuild

import (
	"reflect"
	"testing"
)

// The width-specific linker flag groups append after the common set on
// the matching pointer width only: a vendored wasm32 -L tree must never
// reach a wasm64 link (a wasm32 archive found first is a hard lld
// error), and vice versa.
func TestEffectiveExtraLDFlagsByWidth(t *testing.T) {
	cfg := WasmConfig{
		ExtraLDFlags:       []string{"-nostdlib++", "-lc++"},
		ExtraLDFlagsWasm32: []string{"-Ldeps/wasi-eh/lib"},
		ExtraLDFlagsWasm64: []string{"-Lwasm64-only"},
	}

	got := cfg.EffectiveExtraLDFlags()
	want := []string{"-nostdlib++", "-lc++", "-Ldeps/wasi-eh/lib"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wasm32 flags = %v, want %v", got, want)
	}

	cfg.Wasm64 = true
	got = cfg.EffectiveExtraLDFlags()
	want = []string{"-nostdlib++", "-lc++", "-Lwasm64-only"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wasm64 flags = %v, want %v", got, want)
	}

	// The accessor must not alias the common slice's backing array into
	// its result: two calls at different widths would otherwise clobber
	// each other through the shared append target.
	cfg.Wasm64 = false
	a := cfg.EffectiveExtraLDFlags()
	cfg.Wasm64 = true
	_ = cfg.EffectiveExtraLDFlags()
	if !reflect.DeepEqual(a, []string{"-nostdlib++", "-lc++", "-Ldeps/wasi-eh/lib"}) {
		t.Errorf("first result mutated by second call: %v", a)
	}
}
