package wasmbuild

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// BuildCache tracks input/output hashes for incremental builds.
type BuildCache struct {
	Entries map[string]CacheEntry `json:"entries"` // key = cache key (step ID based)
	path    string                // file path for save
}

// CacheEntry stores the state of a single build step's inputs and output.
type CacheEntry struct {
	ArgsHash   string `json:"args_hash"`
	OutputFile string `json:"output_file"`
	Timestamp  string `json:"timestamp"`
}

// LoadBuildCache loads the build cache from buildDir/build-cache.json.
func LoadBuildCache(buildDir string) *BuildCache {
	bc := &BuildCache{
		Entries: make(map[string]CacheEntry),
		path:    filepath.Join(buildDir, "build-cache.json"),
	}

	data, err := os.ReadFile(bc.path)
	if err != nil {
		return bc
	}

	if err := json.Unmarshal(data, bc); err != nil {
		return &BuildCache{
			Entries: make(map[string]CacheEntry),
			path:    bc.path,
		}
	}

	return bc
}

// Save writes the cache to disk.
func (bc *BuildCache) Save() error {
	if err := os.MkdirAll(filepath.Dir(bc.path), 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(bc, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(bc.path, data, 0o644)
}

// cacheKey returns a unique key for the step based on its original ID.
// This avoids collisions when multiple steps produce the same output file name.
func cacheKey(step WasmBuildStep) string {
	return fmt.Sprintf("%d:%s", step.OriginalID, step.OutputFile)
}

// IsCached returns true if the step's output is up-to-date.
//
// Two-tier cache policy:
//  1. A previously saved cache entry matches this step's args hash AND
//     every input file is older than the output (strong cache: same
//     command, same inputs, output present).
//  2. If no entry exists (e.g. build-cache.json was deleted or a prior
//     build aborted before saving), fall back to pure make(1) rules:
//     treat the output as cached when it exists and every input file
//     is older. This lets subsequent runs reuse orphan obj files that
//     a previous build already produced — otherwise every interrupted
//     build would recompile from zero.
func (bc *BuildCache) IsCached(step WasmBuildStep) bool {
	if step.OutputFile == "" {
		return false
	}

	outputInfo, err := os.Stat(step.OutputFile)
	if err != nil {
		return false
	}
	outputMTime := outputInfo.ModTime()

	// Input mtime check applies to both tiers.
	inputsOlder := func() bool {
		for _, p := range step.InputFiles {
			inInfo, err := os.Stat(p)
			if err != nil {
				continue
			}
			if inInfo.ModTime().After(outputMTime) {
				return false
			}
		}
		return true
	}

	entry, ok := bc.Entries[cacheKey(step)]
	if ok {
		if entry.ArgsHash != hashArgs(step.Args) {
			return false
		}
		return inputsOlder()
	}
	// No entry — fall back to pure mtime only if the step declares
	// input files. A step with no inputs has no meaningful "older
	// than inputs" signal, so we defer to the prior contract of
	// requiring an explicit cache entry. The typical path (real
	// compile/archive steps) carries input_files, so orphan obj
	// reuse still works after an interrupted build.
	if len(step.InputFiles) == 0 {
		return false
	}
	return inputsOlder()
}

// Update records the current state of a step after execution.
func (bc *BuildCache) Update(step WasmBuildStep) {
	bc.Entries[cacheKey(step)] = CacheEntry{
		ArgsHash:   hashArgs(step.Args),
		OutputFile: step.OutputFile,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
	}
}

func hashArgs(args []string) string {
	h := sha256.Sum256([]byte(strings.Join(args, "\x00")))
	return hex.EncodeToString(h[:])
}

// DebugIsCached prints why a step is not cached (used with WASMIFY_CACHE_DEBUG=1).
func (bc *BuildCache) DebugIsCached(step WasmBuildStep) {
	if step.OutputFile == "" {
		fmt.Fprintf(os.Stderr, "[cache-debug] no output file\n")
		return
	}
	entry, ok := bc.Entries[cacheKey(step)]
	if !ok {
		fmt.Fprintf(os.Stderr, "[cache-debug] MISS (no entry): %s\n", filepath.Base(step.OutputFile))
		return
	}
	if _, err := os.Stat(step.OutputFile); err != nil {
		fmt.Fprintf(os.Stderr, "[cache-debug] MISS (output gone): %s\n", filepath.Base(step.OutputFile))
		return
	}
	h := hashArgs(step.Args)
	if entry.ArgsHash != h {
		fmt.Fprintf(os.Stderr, "[cache-debug] MISS (args): %s cached=%s current=%s nargs=%d\n",
			filepath.Base(step.OutputFile), entry.ArgsHash[:12], h[:12], len(step.Args))
		return
	}
	outInfo, err := os.Stat(step.OutputFile)
	if err == nil {
		for _, p := range step.InputFiles {
			inInfo, inErr := os.Stat(p)
			if inErr == nil && inInfo.ModTime().After(outInfo.ModTime()) {
				fmt.Fprintf(os.Stderr, "[cache-debug] MISS (stale): %s input=%s newer than output\n",
					filepath.Base(step.OutputFile), filepath.Base(p))
				return
			}
		}
	}
	fmt.Fprintf(os.Stderr, "[cache-debug] HIT: %s\n", filepath.Base(step.OutputFile))
}
