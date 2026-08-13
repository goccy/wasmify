package wasmbuild

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/wasmify/internal/buildjson"
)

// A target-triple change (wasm_build.wasm64 flipped, HostThreads
// toggled) must discard the whole cache: steps whose command lines
// never mention the target — llvm-ar archives — would otherwise keep
// serving outputs of the previous width and the link fails on a
// mixed-width object set.
func TestBuildCacheInvalidatedOnTargetChange(t *testing.T) {
	tmp := t.TempDir()

	out := filepath.Join(tmp, "libx.a")
	if err := os.WriteFile(out, []byte("ar"), 0o644); err != nil {
		t.Fatal(err)
	}
	// An archive-shaped step: args carry no target triple and no
	// input files are recorded.
	step := WasmBuildStep{
		OriginalID: 7,
		Type:       buildjson.StepArchive,
		Args:       []string{"llvm-ar", "rcs", out},
		OutputFile: out,
	}

	bc := LoadBuildCache(tmp, "wasm64-wasip1")
	bc.Update(step)
	if err := bc.Save(); err != nil {
		t.Fatal(err)
	}

	// Same target: the archive is served from cache.
	if !LoadBuildCache(tmp, "wasm64-wasip1").IsCached(step) {
		t.Fatal("same-target reload should keep the cache entry")
	}

	// Flipped width: the identical step must MISS.
	if LoadBuildCache(tmp, "wasm32-wasip1").IsCached(step) {
		t.Fatal("width flip kept a cached archive of the previous width")
	}
}

// A cache written before the target field existed reads back as
// unrecorded and is discarded, never inheriting the current target.
func TestBuildCachePreTargetFileIsDiscarded(t *testing.T) {
	tmp := t.TempDir()
	pre := `{"entries":{"7:libx.a":{"args_hash":"x","output_file":"libx.a","timestamp":"t"}}}`
	if err := os.WriteFile(filepath.Join(tmp, "build-cache.json"), []byte(pre), 0o644); err != nil {
		t.Fatal(err)
	}
	bc := LoadBuildCache(tmp, "wasm32-wasip1")
	if len(bc.Entries) != 0 {
		t.Fatalf("pre-target cache entries survived the load: %v", bc.Entries)
	}
	if bc.Target != "wasm32-wasip1" {
		t.Fatalf("fresh cache target = %q, want the requested target", bc.Target)
	}
}

// Save must persist the target so the next load can compare it.
func TestBuildCacheSaveRecordsTarget(t *testing.T) {
	tmp := t.TempDir()
	bc := LoadBuildCache(tmp, "wasm64-wasip1")
	if err := bc.Save(); err != nil {
		t.Fatal(err)
	}
	if got := LoadBuildCache(tmp, "wasm64-wasip1").Target; got != "wasm64-wasip1" {
		t.Fatalf("reloaded target = %q, want wasm64-wasip1", got)
	}
}
