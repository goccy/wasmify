// Package parsecache caches the expensive parts of `wasmify parse-headers`
// so a re-run skips work whose inputs are unchanged. There are two
// independent layers, both stored under <dataDir>/cache/parse-headers/:
//
//   - groups.json — the [][]string output of mergeCompatibleGroups, keyed
//     on the header path/group set + compile flags + wasmify version. A
//     hit lets the caller skip the binary-search-of-`clang -fsyntax-only`
//     validation that mergeCompatibleGroups runs.
//
//   - batch-<digest>.json — one *apispec.APISpec per umbrella batch,
//     keyed on the batch's headers + their content hashes + compile flags
//     + wasmify version. A hit lets the caller skip the per-batch
//     `clang -ast-dump=json` invocation and the JSON parse that follows.
//
// The two layers are complementary: a single header content edit hits the
// group cache (path-only key) but invalidates only the batch caches whose
// digests cover that header's bytes — so re-runs do partial recomputation
// rather than starting over.
package parsecache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/goccy/wasmify/internal/apispec"
)

// SchemaVersion bumps when the on-disk format of GroupCache or
// batch-<digest>.json changes in a way that makes earlier files unsafe
// to load. A mismatch forces both caches to be treated as missing.
const SchemaVersion = 1

// WasmifyVersionForTest, when non-empty, is the wasmify version string
// hashed into every cache key. Production callers leave it empty and
// pass the runtime-derived version through the wasmifyVersion arguments
// below; tests set it through t.Cleanup-guarded assignment so they can
// simulate a wasmify upgrade without rebuilding the binary.
var WasmifyVersionForTest string

// resolveVersion picks the test override when one is set, otherwise the
// caller-supplied version.
func resolveVersion(wasmifyVersion string) string {
	if WasmifyVersionForTest != "" {
		return WasmifyVersionForTest
	}
	return wasmifyVersion
}

// GroupCache is the on-disk shape of groups.json.
type GroupCache struct {
	SchemaVersion    int        `json:"schema_version"`
	WasmifyVersion   string     `json:"wasmify_version"`
	KeyDigest        string     `json:"key_digest"`
	HeaderPathsHash  string     `json:"header_paths_hash"`
	HeaderGroupsHash string     `json:"header_groups_hash"`
	CompileFlagsHash string     `json:"compile_flags_hash"`
	Batches          [][]string `json:"batches"`
	UpdatedAt        string     `json:"updated_at"`
}

// CacheDir returns <dataDir>/cache/parse-headers — the single directory
// every cache file lives in. Callers should not synthesise this path
// themselves; the tests pin the layout.
func CacheDir(dataDir string) string {
	return filepath.Join(dataDir, "cache", "parse-headers")
}

func groupCachePath(dataDir string) string {
	return filepath.Join(CacheDir(dataDir), "groups.json")
}

func batchCachePath(dataDir, digest string) string {
	return filepath.Join(CacheDir(dataDir), "batch-"+digest+".json")
}

// ComputeGroupKey hashes the group cache's three input components and
// returns them along with the combined keyDigest. Hashing is done in a
// canonical, stable order so identical inputs always produce identical
// digests:
//   - headerPathsHash hashes the *sorted* set of absolute header paths.
//   - headerGroupsHash hashes a sorted-of-sorted view of headerGroups
//     (each inner slice sorted; outer slice then sorted lexicographically
//     by its first element followed by length). Group decomposition
//     order is therefore irrelevant.
//   - compileFlagsHash hashes the join of compileFlags WITHOUT sorting,
//     because flag order is semantically meaningful (`-I` ordering).
//
// keyDigest is sha256 over the canonical JSON of {schema_version,
// wasmify_version, headerPathsHash, headerGroupsHash, compileFlagsHash}.
// Callers compare keyDigest end-to-end; the component hashes are exposed
// only so that GroupCache stores them for diagnostics.
func ComputeGroupKey(headers []string, headerGroups [][]string, compileFlags []string, wasmifyVersion string) (keyDigest, headerPathsHash, headerGroupsHash, compileFlagsHash string) {
	version := resolveVersion(wasmifyVersion)

	headerPathsHash = hashSortedStrings(headers)
	headerGroupsHash = hashHeaderGroups(headerGroups)
	compileFlagsHash = hashOrderedStrings(compileFlags)

	keyMaterial := struct {
		SchemaVersion    int    `json:"schema_version"`
		WasmifyVersion   string `json:"wasmify_version"`
		HeaderPathsHash  string `json:"header_paths_hash"`
		HeaderGroupsHash string `json:"header_groups_hash"`
		CompileFlagsHash string `json:"compile_flags_hash"`
	}{
		SchemaVersion:    SchemaVersion,
		WasmifyVersion:   version,
		HeaderPathsHash:  headerPathsHash,
		HeaderGroupsHash: headerGroupsHash,
		CompileFlagsHash: compileFlagsHash,
	}
	buf, _ := json.Marshal(keyMaterial)
	sum := sha256.Sum256(buf)
	keyDigest = hex.EncodeToString(sum[:])
	return
}

// LoadGroupCache reads <dataDir>/cache/parse-headers/groups.json. A
// missing file is not an error — callers treat (nil, nil) as a miss. A
// schema-version mismatch is also a miss, not an error: bumping
// SchemaVersion intentionally invalidates every previously-written file
// without forcing humans to delete it. Malformed JSON returns a non-nil
// error so the caller can surface a warning.
func LoadGroupCache(dataDir string) (*GroupCache, error) {
	data, err := os.ReadFile(groupCachePath(dataDir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read groups.json: %w", err)
	}
	var c GroupCache
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse groups.json: %w", err)
	}
	if c.SchemaVersion != SchemaVersion {
		return nil, nil
	}
	return &c, nil
}

// SaveGroupCache writes groups.json, creating the cache directory if
// necessary. UpdatedAt is set to time.Now().UTC().RFC3339 unconditionally
// — tests that need a deterministic timestamp should compare every other
// field individually.
func SaveGroupCache(dataDir string, c *GroupCache) error {
	if err := os.MkdirAll(CacheDir(dataDir), 0o755); err != nil {
		return fmt.Errorf("mkdir cache dir: %w", err)
	}
	c.SchemaVersion = SchemaVersion
	c.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal groups.json: %w", err)
	}
	return os.WriteFile(groupCachePath(dataDir), data, 0o644)
}

// ComputeBatchDigest returns the hex-encoded sha256 over a canonical
// description of the batch's parse-headers inputs: schema version,
// wasmify version, every header path paired with its file content hash
// (sorted by path so order does not matter), and the order-significant
// compile flag join.
//
// On a header-read failure the function returns ("", err) and the caller
// should treat the batch as a cache miss (digest unknown → cannot look
// up an existing batch-*.json) and fall through to clang.
func ComputeBatchDigest(batch []string, compileFlags []string, wasmifyVersion string) (string, error) {
	version := resolveVersion(wasmifyVersion)

	type entry struct {
		Path string `json:"path"`
		Hash string `json:"hash"`
	}
	entries := make([]entry, 0, len(batch))
	for _, path := range batch {
		hash, err := hashFileContent(path)
		if err != nil {
			return "", fmt.Errorf("hash %s: %w", path, err)
		}
		entries = append(entries, entry{Path: path, Hash: hash})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })

	keyMaterial := struct {
		SchemaVersion    int     `json:"schema_version"`
		WasmifyVersion   string  `json:"wasmify_version"`
		Headers          []entry `json:"headers"`
		CompileFlagsHash string  `json:"compile_flags_hash"`
	}{
		SchemaVersion:    SchemaVersion,
		WasmifyVersion:   version,
		Headers:          entries,
		CompileFlagsHash: hashOrderedStrings(compileFlags),
	}
	buf, _ := json.Marshal(keyMaterial)
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:]), nil
}

// LoadBatchAPISpec returns (nil, nil) when no cache file exists for the
// given digest. Malformed JSON returns a non-nil error so the caller can
// log a warning before falling back to clang.
func LoadBatchAPISpec(dataDir, digest string) (*apispec.APISpec, error) {
	data, err := os.ReadFile(batchCachePath(dataDir, digest))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read batch cache %s: %w", digest, err)
	}
	var spec apispec.APISpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parse batch cache %s: %w", digest, err)
	}
	return &spec, nil
}

// SaveBatchAPISpec writes batch-<digest>.json. The cache directory is
// created on demand. Saving is best-effort from the caller's perspective:
// failure means the next run will be a miss but does not invalidate this
// run's results.
func SaveBatchAPISpec(dataDir, digest string, spec *apispec.APISpec) error {
	if err := os.MkdirAll(CacheDir(dataDir), 0o755); err != nil {
		return fmt.Errorf("mkdir cache dir: %w", err)
	}
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal batch cache %s: %w", digest, err)
	}
	return os.WriteFile(batchCachePath(dataDir, digest), data, 0o644)
}

// ResetCache removes every file under <dataDir>/cache/parse-headers/. It
// is idempotent: a missing directory is not an error. Sibling caches
// under <dataDir>/cache/<other-phase>/ are untouched.
func ResetCache(dataDir string) error {
	dir := CacheDir(dataDir)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("stat cache dir: %w", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove cache dir: %w", err)
	}
	return nil
}

// PruneStaleBatches deletes every batch-*.json file under the cache
// directory whose digest is not present in keepDigests. groups.json is
// preserved. Failures on individual files are non-fatal (the function
// returns the first error after attempting all entries).
func PruneStaleBatches(dataDir string, keepDigests map[string]bool) error {
	dir := CacheDir(dataDir)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read cache dir: %w", err)
	}
	var firstErr error
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "batch-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		digest := strings.TrimSuffix(strings.TrimPrefix(name, "batch-"), ".json")
		if keepDigests[digest] {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// hashSortedStrings returns the hex sha256 of the strings joined by NUL
// after sorting. Used for "set-like" inputs where order is not
// semantically meaningful.
func hashSortedStrings(in []string) string {
	cp := make([]string, len(in))
	copy(cp, in)
	sort.Strings(cp)
	return hashOrderedStrings(cp)
}

// hashOrderedStrings returns the hex sha256 of the strings joined by NUL
// in the given order. Used for inputs where order matters (compile
// flags).
func hashOrderedStrings(in []string) string {
	h := sha256.New()
	for i, s := range in {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(s))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// hashHeaderGroups returns the hex sha256 of a canonical encoding of
// the header-group decomposition: each inner slice is sorted, then the
// outer slice is sorted lexicographically. Two callers passing the
// "same" groups in different orders therefore agree.
func hashHeaderGroups(groups [][]string) string {
	canon := make([][]string, len(groups))
	for i, g := range groups {
		cp := make([]string, len(g))
		copy(cp, g)
		sort.Strings(cp)
		canon[i] = cp
	}
	sort.Slice(canon, func(i, j int) bool {
		// Sort by joined-with-NUL form; cheaper than element-wise compare
		// and produces a total order.
		return strings.Join(canon[i], "\x00") < strings.Join(canon[j], "\x00")
	})
	buf, _ := json.Marshal(canon)
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:])
}

// hashFileContent returns the hex sha256 of a file's bytes. Used to
// detect header content changes for the per-batch digest.
func hashFileContent(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
