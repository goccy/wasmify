package parsecache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/goccy/wasmify/internal/apispec"
)

// fixtureHeaders writes n synthetic header files to dir and returns
// their absolute paths and the contents written so callers can mutate
// individual files when checking content-sensitive digests.
func fixtureHeaders(t *testing.T, dir string, contents map[string]string) []string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	paths := make([]string, 0, len(contents))
	for name, body := range contents {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		paths = append(paths, path)
	}
	return paths
}

// ----- ComputeGroupKey ---------------------------------------------------

func TestComputeGroupKey_Deterministic(t *testing.T) {
	headers := []string{"/p/a.h", "/p/b.h"}
	groups := [][]string{{"/p/a.h"}, {"/p/b.h"}}
	flags := []string{"-I/usr/include", "-DFOO"}

	k1, p1, g1, f1 := ComputeGroupKey(headers, groups, flags, "v1")
	k2, p2, g2, f2 := ComputeGroupKey(headers, groups, flags, "v1")

	if k1 != k2 || p1 != p2 || g1 != g2 || f1 != f2 {
		t.Fatalf("digests differ across calls: %q/%q vs %q/%q", k1, p1, k2, p2)
	}
}

func TestComputeGroupKey_HeaderPathsChange(t *testing.T) {
	flags := []string{"-DFOO"}
	groups := [][]string{{"/p/a.h"}}

	base, _, _, _ := ComputeGroupKey([]string{"/p/a.h"}, groups, flags, "v1")
	added, _, _, _ := ComputeGroupKey([]string{"/p/a.h", "/p/b.h"}, groups, flags, "v1")
	renamed, _, _, _ := ComputeGroupKey([]string{"/p/c.h"}, groups, flags, "v1")

	if base == added {
		t.Fatal("adding a header did not change keyDigest")
	}
	if base == renamed {
		t.Fatal("renaming a header did not change keyDigest")
	}
}

func TestComputeGroupKey_HeaderGroupsChange(t *testing.T) {
	headers := []string{"/p/a.h", "/p/b.h"}
	flags := []string{"-DFOO"}

	together, _, _, _ := ComputeGroupKey(headers, [][]string{{"/p/a.h", "/p/b.h"}}, flags, "v1")
	split, _, _, _ := ComputeGroupKey(headers, [][]string{{"/p/a.h"}, {"/p/b.h"}}, flags, "v1")

	if together == split {
		t.Fatal("changing group decomposition did not change keyDigest")
	}
}

func TestComputeGroupKey_CompileFlagsChange(t *testing.T) {
	headers := []string{"/p/a.h"}
	groups := [][]string{{"/p/a.h"}}

	base, _, _, _ := ComputeGroupKey(headers, groups, []string{"-DA", "-DB"}, "v1")
	added, _, _, _ := ComputeGroupKey(headers, groups, []string{"-DA", "-DB", "-DC"}, "v1")
	reordered, _, _, _ := ComputeGroupKey(headers, groups, []string{"-DB", "-DA"}, "v1")

	if base == added {
		t.Fatal("adding a flag did not change keyDigest")
	}
	if base == reordered {
		t.Fatal("reordering flags did not change keyDigest (flag order is significant)")
	}
}

func TestComputeGroupKey_WasmifyVersionChange(t *testing.T) {
	headers := []string{"/p/a.h"}
	groups := [][]string{{"/p/a.h"}}
	flags := []string{"-DFOO"}

	v1, _, _, _ := ComputeGroupKey(headers, groups, flags, "v1")
	v2, _, _, _ := ComputeGroupKey(headers, groups, flags, "v2")

	if v1 == v2 {
		t.Fatal("changing wasmify version did not change keyDigest")
	}
}

func TestComputeGroupKey_HeaderOrderInvariant(t *testing.T) {
	groups := [][]string{{"/p/a.h"}, {"/p/b.h"}}
	flags := []string{"-DFOO"}

	a, _, _, _ := ComputeGroupKey([]string{"/p/a.h", "/p/b.h"}, groups, flags, "v1")
	b, _, _, _ := ComputeGroupKey([]string{"/p/b.h", "/p/a.h"}, groups, flags, "v1")

	if a != b {
		t.Fatal("header order should be irrelevant to keyDigest")
	}
}

func TestComputeGroupKey_GroupOrderInvariant(t *testing.T) {
	headers := []string{"/p/a.h", "/p/b.h"}
	flags := []string{"-DFOO"}

	a, _, _, _ := ComputeGroupKey(headers, [][]string{{"/p/a.h"}, {"/p/b.h"}}, flags, "v1")
	b, _, _, _ := ComputeGroupKey(headers, [][]string{{"/p/b.h"}, {"/p/a.h"}}, flags, "v1")

	if a != b {
		t.Fatal("outer group order should be irrelevant to keyDigest")
	}
}

// ----- LoadGroupCache / SaveGroupCache -----------------------------------

func TestLoadGroupCache_Missing(t *testing.T) {
	dir := t.TempDir()
	c, err := LoadGroupCache(dir)
	if err != nil {
		t.Fatalf("expected (nil, nil) for missing file, got err: %v", err)
	}
	if c != nil {
		t.Fatalf("expected nil cache, got %+v", c)
	}
}

func TestLoadGroupCache_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(CacheDir(dir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(CacheDir(dir), "groups.json"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadGroupCache(dir); err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestLoadGroupCache_SchemaMismatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(CacheDir(dir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stale := GroupCache{SchemaVersion: SchemaVersion + 100, KeyDigest: "deadbeef"}
	data, _ := json.Marshal(stale)
	if err := os.WriteFile(filepath.Join(CacheDir(dir), "groups.json"), data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := LoadGroupCache(dir)
	if err != nil {
		t.Fatalf("schema mismatch should not error, got %v", err)
	}
	if c != nil {
		t.Fatalf("schema mismatch should yield nil cache, got %+v", c)
	}
}

func TestSaveLoadGroupCache_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := &GroupCache{
		WasmifyVersion:   "v1",
		KeyDigest:        "abc123",
		HeaderPathsHash:  "ph",
		HeaderGroupsHash: "gh",
		CompileFlagsHash: "fh",
		Batches:          [][]string{{"a.h"}, {"b.h", "c.h"}},
	}
	if err := SaveGroupCache(dir, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := LoadGroupCache(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil cache after round trip")
	}
	got.UpdatedAt = "" // SaveGroupCache stamps this; clear before compare
	want.UpdatedAt = ""
	want.SchemaVersion = SchemaVersion
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip mismatch:\n got=%+v\nwant=%+v", got, want)
	}
}

func TestSaveGroupCache_FilePermissionsAndPath(t *testing.T) {
	dir := t.TempDir()
	if err := SaveGroupCache(dir, &GroupCache{KeyDigest: "x"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	want := filepath.Join(dir, "cache", "parse-headers", "groups.json")
	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("expected %s to exist: %v", want, err)
	}
	if mode := info.Mode().Perm(); mode != 0o644 {
		t.Fatalf("expected mode 0o644, got %o", mode)
	}
}

func TestSaveGroupCache_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	// dataDir/cache/parse-headers does not yet exist — Save must create it.
	if err := SaveGroupCache(dir, &GroupCache{KeyDigest: "x"}); err != nil {
		t.Fatalf("save: %v", err)
	}
}

// ----- ComputeBatchDigest -------------------------------------------------

func TestComputeBatchDigest_Deterministic(t *testing.T) {
	dir := t.TempDir()
	paths := fixtureHeaders(t, dir, map[string]string{
		"a.h": "int a();",
		"b.h": "int b();",
	})

	d1, err := ComputeBatchDigest(paths, []string{"-DFOO"}, "v1")
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	d2, err := ComputeBatchDigest(paths, []string{"-DFOO"}, "v1")
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	if d1 != d2 {
		t.Fatalf("digest differs across identical calls: %q vs %q", d1, d2)
	}
}

func TestComputeBatchDigest_ContentChange(t *testing.T) {
	dir := t.TempDir()
	paths := fixtureHeaders(t, dir, map[string]string{"a.h": "int a();"})

	before, _ := ComputeBatchDigest(paths, nil, "v1")

	if err := os.WriteFile(paths[0], []byte("int a(int);"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	after, _ := ComputeBatchDigest(paths, nil, "v1")

	if before == after {
		t.Fatal("modifying header content did not change batch digest")
	}
}

func TestComputeBatchDigest_FlagsChange(t *testing.T) {
	dir := t.TempDir()
	paths := fixtureHeaders(t, dir, map[string]string{"a.h": "int a();"})

	base, _ := ComputeBatchDigest(paths, []string{"-DA", "-DB"}, "v1")
	added, _ := ComputeBatchDigest(paths, []string{"-DA", "-DB", "-DC"}, "v1")
	reordered, _ := ComputeBatchDigest(paths, []string{"-DB", "-DA"}, "v1")

	if base == added {
		t.Fatal("adding a flag did not change batch digest")
	}
	if base == reordered {
		t.Fatal("reordering flags did not change batch digest")
	}
}

func TestComputeBatchDigest_VersionChange(t *testing.T) {
	dir := t.TempDir()
	paths := fixtureHeaders(t, dir, map[string]string{"a.h": "int a();"})

	v1, _ := ComputeBatchDigest(paths, nil, "v1")
	v2, _ := ComputeBatchDigest(paths, nil, "v2")
	if v1 == v2 {
		t.Fatal("changing wasmify version did not change batch digest")
	}
}

func TestComputeBatchDigest_HeaderOrderInvariant(t *testing.T) {
	dir := t.TempDir()
	paths := fixtureHeaders(t, dir, map[string]string{"a.h": "int a();", "b.h": "int b();"})

	d1, _ := ComputeBatchDigest([]string{paths[0], paths[1]}, nil, "v1")
	d2, _ := ComputeBatchDigest([]string{paths[1], paths[0]}, nil, "v1")
	if d1 != d2 {
		t.Fatal("header order should be irrelevant to batch digest")
	}
}

func TestComputeBatchDigest_MissingFile(t *testing.T) {
	if _, err := ComputeBatchDigest([]string{"/no/such/header.h"}, nil, "v1"); err == nil {
		t.Fatal("expected error for missing header, got nil")
	}
}

func TestComputeBatchDigest_DistinctBatches(t *testing.T) {
	dir := t.TempDir()
	paths := fixtureHeaders(t, dir, map[string]string{
		"a.h": "int a();",
		"b.h": "int b();",
		"c.h": "int c();",
	})
	// Need stable references to the fixture files by basename.
	byName := func(name string) string {
		for _, p := range paths {
			if filepath.Base(p) == name {
				return p
			}
		}
		t.Fatalf("missing fixture %s", name)
		return ""
	}

	d1, _ := ComputeBatchDigest([]string{byName("a.h"), byName("b.h")}, nil, "v1")
	d2, _ := ComputeBatchDigest([]string{byName("a.h"), byName("c.h")}, nil, "v1")
	if d1 == d2 {
		t.Fatal("two batches with different headers must yield different digests")
	}
}

// ----- LoadBatchAPISpec / SaveBatchAPISpec -------------------------------

func TestLoadBatchAPISpec_Missing(t *testing.T) {
	spec, err := LoadBatchAPISpec(t.TempDir(), "deadbeef")
	if err != nil {
		t.Fatalf("expected (nil, nil) for missing file, got err: %v", err)
	}
	if spec != nil {
		t.Fatalf("expected nil spec, got %+v", spec)
	}
}

func TestLoadBatchAPISpec_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(CacheDir(dir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(batchCachePath(dir, "abc"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadBatchAPISpec(dir, "abc"); err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestSaveLoadBatchAPISpec_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := &apispec.APISpec{
		Namespace: "demo",
		Functions: []apispec.Function{{Name: "f", ReturnType: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"}}},
		Classes:   []apispec.Class{{Name: "C", QualName: "demo::C"}},
		Enums:     []apispec.Enum{{Name: "E", Values: []apispec.EnumValue{{Name: "X", Value: 0}}}},
	}
	if err := SaveBatchAPISpec(dir, "abc", want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := LoadBatchAPISpec(dir, "abc")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil spec")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip mismatch:\n got=%+v\nwant=%+v", got, want)
	}
}

func TestSaveBatchAPISpec_PathLayout(t *testing.T) {
	dir := t.TempDir()
	if err := SaveBatchAPISpec(dir, "deadbeef", &apispec.APISpec{}); err != nil {
		t.Fatalf("save: %v", err)
	}
	want := filepath.Join(dir, "cache", "parse-headers", "batch-deadbeef.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected %s, got %v", want, err)
	}
}

func TestSaveBatchAPISpec_OverwriteExisting(t *testing.T) {
	dir := t.TempDir()
	first := &apispec.APISpec{Namespace: "first"}
	second := &apispec.APISpec{Namespace: "second"}
	if err := SaveBatchAPISpec(dir, "k", first); err != nil {
		t.Fatalf("save first: %v", err)
	}
	if err := SaveBatchAPISpec(dir, "k", second); err != nil {
		t.Fatalf("save second: %v", err)
	}
	got, err := LoadBatchAPISpec(dir, "k")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Namespace != "second" {
		t.Fatalf("expected overwrite to second, got %q", got.Namespace)
	}
}

// ----- ResetCache / PruneStaleBatches ------------------------------------

func TestResetCache_RemovesEverything(t *testing.T) {
	dir := t.TempDir()
	if err := SaveGroupCache(dir, &GroupCache{KeyDigest: "x"}); err != nil {
		t.Fatalf("save groups: %v", err)
	}
	for _, d := range []string{"a", "b", "c"} {
		if err := SaveBatchAPISpec(dir, d, &apispec.APISpec{}); err != nil {
			t.Fatalf("save batch %s: %v", d, err)
		}
	}
	if err := ResetCache(dir); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := os.Stat(CacheDir(dir)); !os.IsNotExist(err) {
		t.Fatalf("expected cache dir to be removed, stat err=%v", err)
	}
}

func TestResetCache_NoCacheDir(t *testing.T) {
	if err := ResetCache(t.TempDir()); err != nil {
		t.Fatalf("reset on missing dir should not error, got %v", err)
	}
}

func TestResetCache_PreservesUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	other := filepath.Join(dir, "cache", "wasm-build")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatalf("mkdir other: %v", err)
	}
	keep := filepath.Join(other, "marker")
	if err := os.WriteFile(keep, []byte("keep"), 0o644); err != nil {
		t.Fatalf("write keep: %v", err)
	}
	if err := SaveGroupCache(dir, &GroupCache{KeyDigest: "x"}); err != nil {
		t.Fatalf("save groups: %v", err)
	}
	if err := ResetCache(dir); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("ResetCache should not have touched %s, got %v", keep, err)
	}
}

func TestPruneStaleBatches_KeepsListed(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"keep1", "keep2", "drop1", "drop2", "drop3"} {
		if err := SaveBatchAPISpec(dir, d, &apispec.APISpec{}); err != nil {
			t.Fatalf("save %s: %v", d, err)
		}
	}
	if err := SaveGroupCache(dir, &GroupCache{KeyDigest: "x"}); err != nil {
		t.Fatalf("save groups: %v", err)
	}
	keep := map[string]bool{"keep1": true, "keep2": true}
	if err := PruneStaleBatches(dir, keep); err != nil {
		t.Fatalf("prune: %v", err)
	}
	for _, d := range []string{"keep1", "keep2"} {
		if _, err := os.Stat(batchCachePath(dir, d)); err != nil {
			t.Fatalf("expected %s preserved, got %v", d, err)
		}
	}
	for _, d := range []string{"drop1", "drop2", "drop3"} {
		if _, err := os.Stat(batchCachePath(dir, d)); !os.IsNotExist(err) {
			t.Fatalf("expected %s removed, stat err=%v", d, err)
		}
	}
	if _, err := os.Stat(filepath.Join(CacheDir(dir), "groups.json")); err != nil {
		t.Fatalf("groups.json must survive prune, got %v", err)
	}
}

func TestPruneStaleBatches_EmptyKeep(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"a", "b"} {
		if err := SaveBatchAPISpec(dir, d, &apispec.APISpec{}); err != nil {
			t.Fatalf("save %s: %v", d, err)
		}
	}
	if err := SaveGroupCache(dir, &GroupCache{KeyDigest: "x"}); err != nil {
		t.Fatalf("save groups: %v", err)
	}
	if err := PruneStaleBatches(dir, map[string]bool{}); err != nil {
		t.Fatalf("prune: %v", err)
	}
	for _, d := range []string{"a", "b"} {
		if _, err := os.Stat(batchCachePath(dir, d)); !os.IsNotExist(err) {
			t.Fatalf("expected batch-%s removed", d)
		}
	}
	if _, err := os.Stat(filepath.Join(CacheDir(dir), "groups.json")); err != nil {
		t.Fatalf("groups.json must survive empty-keep prune, got %v", err)
	}
}

// ----- Error paths --------------------------------------------------------

// blockedDataDir returns a path whose `cache/` parent is a regular file
// rather than a directory, so any os.MkdirAll under it fails with ENOTDIR.
// Used to exercise the MkdirAll error branches of SaveGroupCache /
// SaveBatchAPISpec / PruneStaleBatches without depending on permissions.
func blockedDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cache"), []byte("blocker"), 0o644); err != nil {
		t.Fatalf("blocker write: %v", err)
	}
	return dir
}

func TestSaveGroupCache_MkdirError(t *testing.T) {
	if err := SaveGroupCache(blockedDataDir(t), &GroupCache{KeyDigest: "x"}); err == nil {
		t.Fatal("expected MkdirAll error when cache parent is a regular file")
	}
}

func TestSaveBatchAPISpec_MkdirError(t *testing.T) {
	if err := SaveBatchAPISpec(blockedDataDir(t), "abc", &apispec.APISpec{}); err == nil {
		t.Fatal("expected MkdirAll error when cache parent is a regular file")
	}
}

func TestPruneStaleBatches_NoCacheDir(t *testing.T) {
	if err := PruneStaleBatches(t.TempDir(), nil); err != nil {
		t.Fatalf("missing cache dir should not error, got %v", err)
	}
}

func TestPruneStaleBatches_IgnoresUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(CacheDir(dir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Files that are NOT batch-*.json should be left alone.
	for _, name := range []string{"groups.json", "notes.txt", "batch-bogus.bak", "subdir"} {
		path := filepath.Join(CacheDir(dir), name)
		if name == "subdir" {
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatalf("mkdir subdir: %v", err)
			}
			continue
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := PruneStaleBatches(dir, map[string]bool{}); err != nil {
		t.Fatalf("prune: %v", err)
	}
	for _, name := range []string{"groups.json", "notes.txt", "batch-bogus.bak", "subdir"} {
		if _, err := os.Stat(filepath.Join(CacheDir(dir), name)); err != nil {
			t.Fatalf("expected %s preserved, got %v", name, err)
		}
	}
}

func TestComputeBatchDigest_DirectoryAsHeader(t *testing.T) {
	// Passing a directory path should surface as an error (not a panic):
	// hashFileContent opens then io.Copy returns EISDIR on read.
	if _, err := ComputeBatchDigest([]string{t.TempDir()}, nil, "v1"); err == nil {
		t.Fatal("expected error when 'header' is actually a directory")
	}
}

// ----- WasmifyVersionForTest seam ----------------------------------------

func TestWasmifyVersionForTest_OverridesGroupKey(t *testing.T) {
	headers := []string{"/p/a.h"}
	groups := [][]string{{"/p/a.h"}}
	flags := []string{"-DX"}

	WasmifyVersionForTest = "override"
	t.Cleanup(func() { WasmifyVersionForTest = "" })

	override, _, _, _ := ComputeGroupKey(headers, groups, flags, "real")

	WasmifyVersionForTest = ""
	real, _, _, _ := ComputeGroupKey(headers, groups, flags, "real")
	matchOverride, _, _, _ := ComputeGroupKey(headers, groups, flags, "override")

	if override == real {
		t.Fatal("WasmifyVersionForTest did not override the runtime version")
	}
	if override != matchOverride {
		t.Fatal("override should match a real call with the same version string")
	}
}

func TestWasmifyVersionForTest_OverridesBatchDigest(t *testing.T) {
	dir := t.TempDir()
	paths := fixtureHeaders(t, dir, map[string]string{"a.h": "int a();"})

	WasmifyVersionForTest = "override"
	t.Cleanup(func() { WasmifyVersionForTest = "" })
	override, _ := ComputeBatchDigest(paths, nil, "real")

	WasmifyVersionForTest = ""
	real, _ := ComputeBatchDigest(paths, nil, "real")
	matchOverride, _ := ComputeBatchDigest(paths, nil, "override")

	if override == real {
		t.Fatal("WasmifyVersionForTest did not override the runtime version")
	}
	if override != matchOverride {
		t.Fatal("override should match a real call with the same version string")
	}
}
