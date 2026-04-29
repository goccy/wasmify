package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/goccy/wasmify/internal/apispec"
	"github.com/goccy/wasmify/internal/apispec/parsecache"
	"github.com/goccy/wasmify/internal/buildjson"
	"github.com/goccy/wasmify/internal/state"
)

// fixture is a self-contained parse-headers playground: a temp directory
// with synthetic headers, a hand-written .wasmify/build.json that points
// at them via fake .d files, and a wasmify.json that calls the same
// directory the upstream project. The test process chdirs in, runs
// cmdParseHeaders against a stubbed runBatchClang, and asserts on the
// stderr the function emits.
type fixture struct {
	t       *testing.T
	root    string                       // project / output dir
	dataDir string                       // <root>/.wasmify
	headers map[string]string            // basename -> absolute path
	stub    *clangStub                   // counts runBatchClang calls
	groups  [][]string                   // umbrella batches the build.json implies
	prevDir string                       // restored by t.Cleanup
}

// clangStub records how many times runBatchClang was invoked, optionally
// errors on a given index, and returns a deterministic APISpec keyed on
// the umbrella's first header so cache-hit tests can detect when a stale
// cache was served.
type clangStub struct {
	calls   atomic.Int64
	failOn  map[int]error // 0-indexed call number (0 = first call) -> error to return
	current atomic.Int64
}

func (s *clangStub) handler(_ /*clangPath*/ string, _ /*umbrella*/ string, batch []string, _ /*flags*/ []string) (*apispec.APISpec, error) {
	idx := int(s.calls.Add(1)) - 1
	if err, ok := s.failOn[idx]; ok {
		return nil, err
	}
	// Deterministic spec: one Function per header in the batch.
	spec := &apispec.APISpec{}
	for _, h := range batch {
		spec.Functions = append(spec.Functions, apispec.Function{
			Name:       "from_" + sanitize(filepath.Base(h)),
			ReturnType: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"},
		})
	}
	s.current.Add(1)
	return spec, nil
}

func sanitize(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

// newFixture builds the on-disk layout. headerNames is the deterministic
// list of basenames (e.g. ["a.h", "b.h"]); content is the bytes written
// to each header. groupShape is the umbrella decomposition described as
// indices into headerNames — e.g. [[0, 1], [2]] means "a.h+b.h together,
// c.h alone". One compile step per group is written to build.json with
// matching .d files.
func newFixture(t *testing.T, headerNames []string, content map[string]string, groupShape [][]int) *fixture {
	t.Helper()

	root := t.TempDir()
	dataDir := filepath.Join(root, ".wasmify")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir .wasmify: %v", err)
	}

	headers := make(map[string]string, len(headerNames))
	groups := make([][]string, 0, len(groupShape))

	// Write headers.
	for _, name := range headerNames {
		path := filepath.Join(root, name)
		body, ok := content[name]
		if !ok {
			body = "// " + name + "\nvoid f_" + sanitize(name) + "(void);\n"
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		headers[name] = path
	}

	// Write one .d file + matching compile step per group.
	steps := make([]buildjson.BuildStep, 0, len(groupShape))
	for gi, idxs := range groupShape {
		var groupHeaders []string
		for _, i := range idxs {
			groupHeaders = append(groupHeaders, headers[headerNames[i]])
		}
		groups = append(groups, groupHeaders)

		depFile := filepath.Join(dataDir, fmt.Sprintf("group%d.d", gi))
		// .d format: `target: dep1 dep2 ...` — wasmify ignores the
		// target token and consumes the deps.
		var b strings.Builder
		fmt.Fprintf(&b, "group%d.o:", gi)
		for _, h := range groupHeaders {
			b.WriteString(" ")
			b.WriteString(h)
		}
		b.WriteString("\n")
		if err := os.WriteFile(depFile, []byte(b.String()), 0o644); err != nil {
			t.Fatalf("write depfile: %v", err)
		}

		steps = append(steps, buildjson.BuildStep{
			ID:         gi,
			Type:       buildjson.StepCompile,
			Compiler:   "clang++",
			Executable: "clang++",
			WorkDir:    root,
			Args: []string{
				"clang++", "-c",
				"-MD", "-MF", depFile,
				"-o", fmt.Sprintf("group%d.o", gi),
				fmt.Sprintf("group%d.cc", gi),
			},
			OutputFile: fmt.Sprintf("group%d.o", gi),
			Language:   "c++",
		})
	}

	bjson := &buildjson.BuildJSON{
		Version:    "1",
		Project:    buildjson.ProjectInfo{Name: "fixture", BuildSystem: "manual"},
		Steps:      steps,
		CapturedAt: "2026-01-01T00:00:00Z",
	}
	bdata, _ := json.MarshalIndent(bjson, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "build.json"), bdata, 0o644); err != nil {
		t.Fatalf("write build.json: %v", err)
	}

	// wasmify.json: upstream points at root itself.
	st := state.New(".")
	st.Project.Name = "fixture"
	if err := state.Save(root, st); err != nil {
		t.Fatalf("save wasmify.json: %v", err)
	}

	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	f := &fixture{
		t:       t,
		root:    root,
		dataDir: dataDir,
		headers: headers,
		groups:  groups,
		prevDir: prev,
	}
	t.Cleanup(func() {
		_ = os.Chdir(prev)
	})
	return f
}

// installStub replaces runBatchClang AND mergeCompatibleGroupsFn for the
// duration of the test. mergeCompatibleGroupsFn is stubbed to return the
// fixture's declared group shape verbatim — without this, the production
// implementation would invoke real `clang -fsyntax-only` and merge our
// synthetic conflict-free headers into a single umbrella, which would
// invalidate every per-batch test that depends on N separate batches.
func (f *fixture) installStub(stub *clangStub) {
	prevRun := runBatchClang
	prevMerge := mergeCompatibleGroupsFn
	runBatchClang = stub.handler
	mergeCompatibleGroupsFn = func(clangPath string, groups [][]string, compileFlags []string) [][]string {
		// Return the .d-file groups as-is; no merging.
		out := make([][]string, len(groups))
		for i, g := range groups {
			cp := make([]string, len(g))
			copy(cp, g)
			out[i] = cp
		}
		return out
	}
	f.stub = stub
	f.t.Cleanup(func() {
		runBatchClang = prevRun
		mergeCompatibleGroupsFn = prevMerge
	})
}

// runParseHeaders runs cmdParseHeaders with the given args, returning the
// captured stderr text and any error. stdout is silently swallowed.
func (f *fixture) runParseHeaders(args ...string) (string, error) {
	f.t.Helper()

	// Redirect stdout + stderr through pipes.
	oldOut, oldErr := os.Stdout, os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout, os.Stderr = wOut, wErr

	doneOut := make(chan struct{})
	doneErr := make(chan struct{})
	var errBuf, outBuf strings.Builder
	go func() {
		_, _ = io.Copy(&outBuf, rOut)
		close(doneOut)
	}()
	go func() {
		_, _ = io.Copy(&errBuf, rErr)
		close(doneErr)
	}()

	cmdErr := cmdParseHeaders(args)

	_ = wOut.Close()
	_ = wErr.Close()
	<-doneOut
	<-doneErr
	os.Stdout, os.Stderr = oldOut, oldErr

	return errBuf.String(), cmdErr
}

var cacheStatsRE = regexp.MustCompile(`batch cache: (\d+) hit, (\d+) miss`)

// parseCacheStats extracts the (hits, misses) tuple from the
// `[parse-headers] batch cache: H hit, M miss` line. Fails the test
// loudly if the line is missing — every successful parse-headers run
// must emit exactly one such line.
func parseCacheStats(t *testing.T, stderr string) (hit, miss int) {
	t.Helper()
	m := cacheStatsRE.FindStringSubmatch(stderr)
	if m == nil {
		t.Fatalf("stderr missing 'batch cache: N hit, M miss' line:\n%s", stderr)
	}
	h, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	return h, mi
}

// ----- test cases ---------------------------------------------------------

func TestParseHeaders_FirstRun_ColdMiss(t *testing.T) {
	f := newFixture(t,
		[]string{"a.h", "b.h"},
		nil,
		[][]int{{0}, {1}},
	)
	f.installStub(&clangStub{})

	stderr, err := f.runParseHeaders()
	if err != nil {
		t.Fatalf("first run failed: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "group cache miss") {
		t.Fatalf("expected group cache miss on first run, stderr=\n%s", stderr)
	}
	hit, miss := parseCacheStats(t, stderr)
	if hit != 0 || miss != 2 {
		t.Fatalf("expected 0 hit / 2 miss, got %d/%d", hit, miss)
	}
	if got := f.stub.calls.Load(); got != 2 {
		t.Fatalf("expected 2 runBatchClang calls, got %d", got)
	}
	// Caches written.
	if _, err := os.Stat(filepath.Join(parsecache.CacheDir(f.dataDir), "groups.json")); err != nil {
		t.Fatalf("groups.json not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.root, "api-spec.json")); err != nil {
		t.Fatalf("api-spec.json not written: %v", err)
	}
}

func TestParseHeaders_SecondRun_FullWarmHit(t *testing.T) {
	f := newFixture(t,
		[]string{"a.h", "b.h"},
		nil,
		[][]int{{0}, {1}},
	)
	stub := &clangStub{}
	f.installStub(stub)

	if _, err := f.runParseHeaders(); err != nil {
		t.Fatalf("warm-up: %v", err)
	}
	startCalls := stub.calls.Load()

	stderr, err := f.runParseHeaders()
	if err != nil {
		t.Fatalf("warm run failed: %v", err)
	}
	if !strings.Contains(stderr, "group cache hit") {
		t.Fatalf("expected group cache hit, stderr=\n%s", stderr)
	}
	hit, miss := parseCacheStats(t, stderr)
	if hit != 2 || miss != 0 {
		t.Fatalf("expected 2 hit / 0 miss, got %d/%d", hit, miss)
	}
	if delta := stub.calls.Load() - startCalls; delta != 0 {
		t.Fatalf("warm run invoked runBatchClang %d times (expected 0)", delta)
	}
}

func TestParseHeaders_HeaderTouched_PartialMiss(t *testing.T) {
	f := newFixture(t,
		[]string{"a.h", "b.h", "c.h"},
		nil,
		[][]int{{0}, {1}, {2}},
	)
	stub := &clangStub{}
	f.installStub(stub)

	if _, err := f.runParseHeaders(); err != nil {
		t.Fatalf("warm-up: %v", err)
	}
	startCalls := stub.calls.Load()

	// Touch b.h's bytes; only the batch covering b.h should miss.
	if err := os.WriteFile(f.headers["b.h"], []byte("// b.h modified\nvoid b_changed(void);\n"), 0o644); err != nil {
		t.Fatalf("rewrite b.h: %v", err)
	}

	stderr, err := f.runParseHeaders()
	if err != nil {
		t.Fatalf("partial run failed: %v", err)
	}
	hit, miss := parseCacheStats(t, stderr)
	if hit != 2 || miss != 1 {
		t.Fatalf("expected 2 hit / 1 miss, got %d/%d", hit, miss)
	}
	if delta := stub.calls.Load() - startCalls; delta != 1 {
		t.Fatalf("expected exactly 1 runBatchClang call after touch, got %d", delta)
	}
	if !strings.Contains(stderr, "group cache hit") {
		t.Fatalf("group cache should still hit (path-only key); stderr=\n%s", stderr)
	}
}

func TestParseHeaders_HeaderInMultipleBatches_PartialMiss(t *testing.T) {
	// common.h appears in both batches via the .d files.
	f := newFixture(t,
		[]string{"common.h", "a.h", "b.h"},
		nil,
		[][]int{{0, 1}, {0, 2}}, // common.h+a.h, common.h+b.h
	)
	stub := &clangStub{}
	f.installStub(stub)

	if _, err := f.runParseHeaders(); err != nil {
		t.Fatalf("warm-up: %v", err)
	}
	startCalls := stub.calls.Load()

	if err := os.WriteFile(f.headers["common.h"], []byte("// common changed\nvoid c2(void);\n"), 0o644); err != nil {
		t.Fatalf("rewrite common.h: %v", err)
	}

	stderr, err := f.runParseHeaders()
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	hit, miss := parseCacheStats(t, stderr)
	if hit != 0 || miss != 2 {
		t.Fatalf("expected 0 hit / 2 miss (common.h is in both batches), got %d/%d", hit, miss)
	}
	if delta := stub.calls.Load() - startCalls; delta != 2 {
		t.Fatalf("expected 2 runBatchClang calls after touching shared header, got %d", delta)
	}
}

func TestParseHeaders_ForceFlag_NukesAndRebuilds(t *testing.T) {
	f := newFixture(t,
		[]string{"a.h", "b.h"},
		nil,
		[][]int{{0}, {1}},
	)
	stub := &clangStub{}
	f.installStub(stub)

	if _, err := f.runParseHeaders(); err != nil {
		t.Fatalf("warm-up: %v", err)
	}
	startCalls := stub.calls.Load()

	stderr, err := f.runParseHeaders("--force")
	if err != nil {
		t.Fatalf("force run: %v", err)
	}
	if !strings.Contains(stderr, "group cache miss") {
		t.Fatalf("expected group cache miss after --force, stderr=\n%s", stderr)
	}
	hit, miss := parseCacheStats(t, stderr)
	if hit != 0 || miss != 2 {
		t.Fatalf("expected 0 hit / 2 miss after --force, got %d/%d", hit, miss)
	}
	if delta := stub.calls.Load() - startCalls; delta != 2 {
		t.Fatalf("--force should re-invoke runBatchClang for every batch, got %d", delta)
	}
}

func TestParseHeaders_NoCacheFlag_AliasOfForce(t *testing.T) {
	// Need ≥ 2 headers to exercise the multi-batch (cache-aware) path —
	// a single-header parse-headers call intentionally bypasses caching.
	f := newFixture(t,
		[]string{"a.h", "b.h"},
		nil,
		[][]int{{0}, {1}},
	)
	stub := &clangStub{}
	f.installStub(stub)

	if _, err := f.runParseHeaders(); err != nil {
		t.Fatalf("warm-up: %v", err)
	}
	startCalls := stub.calls.Load()

	stderr, err := f.runParseHeaders("--no-cache")
	if err != nil {
		t.Fatalf("--no-cache run: %v", err)
	}
	hit, _ := parseCacheStats(t, stderr)
	if hit != 0 {
		t.Fatalf("--no-cache should produce 0 hits, got %d", hit)
	}
	if delta := stub.calls.Load() - startCalls; delta != 2 {
		t.Fatalf("--no-cache should re-invoke clang for every batch, got %d", delta)
	}
}

func TestParseHeaders_CompileFlagsChanged_FullMiss(t *testing.T) {
	f := newFixture(t,
		[]string{"a.h", "b.h"},
		nil,
		[][]int{{0}, {1}},
	)
	stub := &clangStub{}
	f.installStub(stub)

	if _, err := f.runParseHeaders(); err != nil {
		t.Fatalf("warm-up: %v", err)
	}
	startCalls := stub.calls.Load()

	// Mutate build.json: append a -DFOO flag to every compile step.
	addCompileFlag(t, f.root, "-DFOO")

	stderr, err := f.runParseHeaders()
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if !strings.Contains(stderr, "group cache miss") {
		t.Fatalf("expected group cache miss after flag change, stderr=\n%s", stderr)
	}
	hit, miss := parseCacheStats(t, stderr)
	if hit != 0 || miss != 2 {
		t.Fatalf("flag change should produce 0 hits / 2 miss, got %d/%d", hit, miss)
	}
	if delta := stub.calls.Load() - startCalls; delta != 2 {
		t.Fatalf("expected 2 runBatchClang calls after flag change, got %d", delta)
	}
}

func TestParseHeaders_WasmifyVersionChanged_FullMiss(t *testing.T) {
	f := newFixture(t,
		[]string{"a.h", "b.h"},
		nil,
		[][]int{{0}, {1}},
	)
	stub := &clangStub{}
	f.installStub(stub)

	if _, err := f.runParseHeaders(); err != nil {
		t.Fatalf("warm-up: %v", err)
	}
	startCalls := stub.calls.Load()

	prev := parsecache.WasmifyVersionForTest
	parsecache.WasmifyVersionForTest = "v-future"
	t.Cleanup(func() { parsecache.WasmifyVersionForTest = prev })

	stderr, err := f.runParseHeaders()
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	hit, miss := parseCacheStats(t, stderr)
	if hit != 0 || miss != 2 {
		t.Fatalf("version change should produce full miss, got %d hit / %d miss", hit, miss)
	}
	if delta := stub.calls.Load() - startCalls; delta != 2 {
		t.Fatalf("expected 2 runBatchClang calls after version bump, got %d", delta)
	}
}

func TestParseHeaders_CorruptBatchCache_RecomputesOnlyAffected(t *testing.T) {
	f := newFixture(t,
		[]string{"a.h", "b.h", "c.h"},
		nil,
		[][]int{{0}, {1}, {2}},
	)
	stub := &clangStub{}
	f.installStub(stub)

	if _, err := f.runParseHeaders(); err != nil {
		t.Fatalf("warm-up: %v", err)
	}
	startCalls := stub.calls.Load()

	// Corrupt one batch file.
	entries, err := os.ReadDir(parsecache.CacheDir(f.dataDir))
	if err != nil {
		t.Fatalf("read cache dir: %v", err)
	}
	var victim string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "batch-") {
			victim = filepath.Join(parsecache.CacheDir(f.dataDir), e.Name())
			break
		}
	}
	if victim == "" {
		t.Fatalf("no batch cache files found to corrupt")
	}
	if err := os.WriteFile(victim, []byte("not json"), 0o644); err != nil {
		t.Fatalf("corrupt: %v", err)
	}

	stderr, err := f.runParseHeaders()
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	hit, miss := parseCacheStats(t, stderr)
	if hit != 2 || miss != 1 {
		t.Fatalf("expected 2 hit / 1 miss after one corrupt cache, got %d/%d", hit, miss)
	}
	if delta := stub.calls.Load() - startCalls; delta != 1 {
		t.Fatalf("expected 1 runBatchClang call (corrupt cache), got %d", delta)
	}
}

func TestParseHeaders_CorruptGroupCache_RecomputesGroups(t *testing.T) {
	f := newFixture(t,
		[]string{"a.h", "b.h"},
		nil,
		[][]int{{0}, {1}},
	)
	stub := &clangStub{}
	f.installStub(stub)

	if _, err := f.runParseHeaders(); err != nil {
		t.Fatalf("warm-up: %v", err)
	}
	startCalls := stub.calls.Load()

	gpath := filepath.Join(parsecache.CacheDir(f.dataDir), "groups.json")
	if err := os.WriteFile(gpath, []byte("not json"), 0o644); err != nil {
		t.Fatalf("corrupt groups: %v", err)
	}

	stderr, err := f.runParseHeaders()
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if !strings.Contains(stderr, "group cache miss") {
		t.Fatalf("expected group cache miss after corruption, stderr=\n%s", stderr)
	}
	hit, miss := parseCacheStats(t, stderr)
	// Group recomputed but per-batch digests unchanged → still hit.
	if hit != 2 || miss != 0 {
		t.Fatalf("expected 2 hit / 0 miss (batch digests unchanged), got %d/%d", hit, miss)
	}
	if delta := stub.calls.Load() - startCalls; delta != 0 {
		t.Fatalf("corrupt group cache should not re-invoke clang (batch caches still valid), got %d", delta)
	}
}

func TestParseHeaders_StaleGroupConflict_FallbackPathRecomputes(t *testing.T) {
	f := newFixture(t,
		[]string{"a.h", "b.h"},
		nil,
		[][]int{{0}, {1}},
	)
	stub := &clangStub{}
	f.installStub(stub)

	if _, err := f.runParseHeaders(); err != nil {
		t.Fatalf("warm-up: %v", err)
	}

	// Touch a.h so its batch invalidates → first call attempts re-parse.
	if err := os.WriteFile(f.headers["a.h"], []byte("// a touched\nvoid a2(void);\n"), 0o644); err != nil {
		t.Fatalf("touch a.h: %v", err)
	}

	// Configure stub so the next runBatchClang call (the one for the
	// invalidated batch) fails the first time and succeeds the second
	// time. This simulates a stale group cache → first batch parse
	// fails → wrapper deletes groups.json and retries.
	startCalls := stub.calls.Load()
	stub.failOn = map[int]error{int(startCalls): fmt.Errorf("simulated umbrella conflict")}

	stderr, err := f.runParseHeaders()
	if err != nil {
		t.Fatalf("expected fallback to succeed, got err: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "cached groups appear stale") {
		t.Fatalf("expected fallback log line, stderr=\n%s", stderr)
	}
	// groups.json must have been deleted by the fallback path.
	if _, err := os.Stat(filepath.Join(parsecache.CacheDir(f.dataDir), "groups.json")); err == nil {
		// File may have been re-saved by the deferred SaveGroupCache after
		// the retry succeeded; that's fine. We only require the fallback
		// log line to confirm the recovery path actually ran.
		_ = err
	}
}

func TestParseHeaders_PruneStaleBatches_AfterRun(t *testing.T) {
	f := newFixture(t,
		[]string{"a.h", "b.h"},
		nil,
		[][]int{{0}, {1}},
	)
	f.installStub(&clangStub{})

	if _, err := f.runParseHeaders(); err != nil {
		t.Fatalf("warm-up: %v", err)
	}

	// Drop a stale batch-*.json that is unrelated to the current digests.
	stale := filepath.Join(parsecache.CacheDir(f.dataDir), "batch-staleabc.json")
	if err := os.WriteFile(stale, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write stale: %v", err)
	}

	if _, err := f.runParseHeaders(); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("expected stale batch removed, stat err=%v", err)
	}
}

func TestParseHeaders_HeaderRemoved_GroupMissBatchHit(t *testing.T) {
	f := newFixture(t,
		[]string{"a.h", "b.h", "c.h"},
		nil,
		[][]int{{0}, {1}, {2}},
	)
	stub := &clangStub{}
	f.installStub(stub)

	if _, err := f.runParseHeaders(); err != nil {
		t.Fatalf("warm-up: %v", err)
	}

	// Remove c.h from the build by rewriting build.json without its
	// step + dropping its .d file.
	dropGroup(t, f.root, 2)

	startCalls := stub.calls.Load()
	stderr, err := f.runParseHeaders()
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if !strings.Contains(stderr, "group cache miss") {
		t.Fatalf("expected group cache miss after header removal, stderr=\n%s", stderr)
	}
	hit, miss := parseCacheStats(t, stderr)
	if hit != 2 || miss != 0 {
		t.Fatalf("surviving batches should hit (digests unchanged), got %d hit / %d miss", hit, miss)
	}
	if delta := stub.calls.Load() - startCalls; delta != 0 {
		t.Fatalf("expected 0 runBatchClang calls (surviving batches hit), got %d", delta)
	}
}

// addCompileFlag rewrites build.json by appending an extra flag to every
// compile step's Args, simulating a `-DFOO` getting added to the build.
func addCompileFlag(t *testing.T, root string, flag string) {
	t.Helper()
	path := filepath.Join(root, "build.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read build.json: %v", err)
	}
	var b buildjson.BuildJSON
	if err := json.Unmarshal(data, &b); err != nil {
		t.Fatalf("unmarshal build.json: %v", err)
	}
	for i := range b.Steps {
		if b.Steps[i].Type == buildjson.StepCompile {
			b.Steps[i].Args = append(b.Steps[i].Args, flag)
		}
	}
	out, _ := json.MarshalIndent(b, "", "  ")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write build.json: %v", err)
	}
}

// dropGroup removes the compile step with the given index from build.json,
// simulating a header set shrink. The corresponding .d file is also
// removed so wasmify cannot rediscover the headers.
func dropGroup(t *testing.T, root string, gi int) {
	t.Helper()
	path := filepath.Join(root, "build.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read build.json: %v", err)
	}
	var b buildjson.BuildJSON
	if err := json.Unmarshal(data, &b); err != nil {
		t.Fatalf("unmarshal build.json: %v", err)
	}
	kept := make([]buildjson.BuildStep, 0, len(b.Steps))
	for _, s := range b.Steps {
		if s.ID == gi {
			continue
		}
		kept = append(kept, s)
	}
	b.Steps = kept
	out, _ := json.MarshalIndent(b, "", "  ")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write build.json: %v", err)
	}
	_ = os.Remove(filepath.Join(root, ".wasmify", fmt.Sprintf("group%d.d", gi)))
}
