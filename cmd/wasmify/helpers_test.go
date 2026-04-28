package main

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/goccy/wasmify/internal/apispec"
	"github.com/goccy/wasmify/internal/arch"
	"github.com/goccy/wasmify/internal/buildjson"
	"github.com/goccy/wasmify/internal/cache"
	"github.com/goccy/wasmify/internal/state"
)

// --- test helpers -----------------------------------------------------------

// captureStdout runs f while redirecting os.Stdout to a buffer. Returns the
// captured output.
func captureStdout(t *testing.T, f func() error) (string, error) {
	t.Helper()
	return captureFD(t, &os.Stdout, f)
}

// captureStderr runs f while redirecting os.Stderr.
func captureStderr(t *testing.T, f func() error) (string, error) {
	t.Helper()
	return captureFD(t, &os.Stderr, f)
}

// captureFD redirects a stdio handle to an in-memory pipe while f runs and
// returns the captured bytes. The pipe's write end is closed via a defer in
// the worker goroutine so ReadAll unblocks even when f triggers
// runtime.Goexit (e.g. via t.Fatalf called from a nested capture) — without
// the defer the pipe would stay open and the parent test deadlocks.
func captureFD(t *testing.T, fd **os.File, f func() error) (string, error) {
	t.Helper()
	orig := *fd
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	*fd = w
	defer func() { *fd = orig }()

	errCh := make(chan error, 1)
	go func() {
		defer func() { _ = w.Close() }()
		errCh <- f()
	}()

	data, _ := io.ReadAll(r)
	callErr := <-errCh
	return string(data), callErr
}

// withStdin replaces os.Stdin with a pipe containing input for the duration of
// calling f. Restores stdin after.
func withStdin(t *testing.T, input string, f func() error) error {
	t.Helper()
	orig := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdin = r
	defer func() {
		os.Stdin = orig
	}()
	_, _ = w.WriteString(input)
	_ = w.Close()
	return f()
}

// isolatedXDG isolates XDG_CONFIG_HOME for the test AND chdirs into a fresh
// temp directory. After wasmify retired the per-project XDG data dir in
// favor of <cwd>/.wasmify, tests depend on a unique cwd for isolation just
// as much as on a unique XDG_CONFIG_HOME (the latter now only affects
// wasi-sdk lookup, which most tests do not touch).
func isolatedXDG(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	chdirTemp(t)
	return dir
}

// initTestOutDir writes wasmify.json into the current working directory
// pointing at the given upstream project. Every subcommand except init now
// looks for wasmify.json in cwd (go.mod-style project discovery), so tests
// that exercise those commands must plant one first. Call after isolatedXDG
// so cwd is the tempdir where artifacts land.
func initTestOutDir(t *testing.T, project string) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Save(cwd, state.New(project)); err != nil {
		t.Fatalf("state.Save: %v", err)
	}
}

// chdirTemp chdirs into a fresh temp dir for the test duration. Used when a
// command writes into the current working directory (e.g. cmdInit scaffolding
// the skill into cwd/.claude/skills) so the project tree stays clean.
func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	return dir
}

// gitInit creates a git repo in a temp dir with one commit and returns the dir.
func gitInitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# readme\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// --- pure-helper tests ------------------------------------------------------

func TestParseOutputDirFlag(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantOutput  string
		wantRestIDs []string
	}{
		{
			name:        "no flag",
			args:        []string{"/path", "--other", "x"},
			wantOutput:  "",
			wantRestIDs: []string{"/path", "--other", "x"},
		},
		{
			name:        "flag in middle",
			args:        []string{"/path", "--output-dir", "/out", "--foo"},
			wantOutput:  "/out",
			wantRestIDs: []string{"/path", "--foo"},
		},
		{
			name:        "flag at end with value",
			args:        []string{"/path", "--output-dir", "/out"},
			wantOutput:  "/out",
			wantRestIDs: []string{"/path"},
		},
		{
			name:        "dangling flag (no value) is preserved as-is",
			args:        []string{"/path", "--output-dir"},
			wantOutput:  "",
			wantRestIDs: []string{"/path", "--output-dir"},
		},
		{
			name:        "empty",
			args:        []string{},
			wantOutput:  "",
			wantRestIDs: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotOut, gotRest := parseOutputDirFlag(tc.args)
			if gotOut != tc.wantOutput {
				t.Errorf("output = %q, want %q", gotOut, tc.wantOutput)
			}
			if !reflect.DeepEqual(gotRest, tc.wantRestIDs) {
				t.Errorf("rest = %v, want %v", gotRest, tc.wantRestIDs)
			}
		})
	}
}

func TestSplitBazelBuildCommand(t *testing.T) {
	cases := []struct {
		name        string
		cmd         []string
		wantFlags   []string
		wantTargets []string
	}{
		{
			name:        "single string shell form",
			cmd:         []string{"bazel build -c opt //googlesql/public:analyzer"},
			wantFlags:   []string{"-c", "opt"},
			wantTargets: []string{"//googlesql/public:analyzer"},
		},
		{
			name:        "already split argv",
			cmd:         []string{"bazel", "build", "//foo:bar"},
			wantFlags:   nil,
			wantTargets: []string{"//foo:bar"},
		},
		{
			name:        "bazelisk + external @ label",
			cmd:         []string{"bazelisk build @rules_cc//:all"},
			wantFlags:   nil,
			wantTargets: []string{"@rules_cc//:all"},
		},
		{
			name: "multiple targets (analyzer + sql_formatter)",
			cmd: []string{
				"bazel build -c opt //googlesql/public:analyzer //googlesql/public:sql_formatter",
			},
			wantFlags:   []string{"-c", "opt"},
			wantTargets: []string{"//googlesql/public:analyzer", "//googlesql/public:sql_formatter"},
		},
		{
			name:        "no target found falls back to last positional",
			cmd:         []string{"bazel build -c opt"},
			wantFlags:   []string{"-c"},
			wantTargets: []string{"opt"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotFlags, gotTargets := splitBazelBuildCommand(tc.cmd)
			if !reflect.DeepEqual(gotTargets, tc.wantTargets) {
				t.Errorf("targets = %v, want %v", gotTargets, tc.wantTargets)
			}
			if !reflect.DeepEqual(gotFlags, tc.wantFlags) {
				t.Errorf("flags = %v, want %v", gotFlags, tc.wantFlags)
			}
		})
	}
}

func TestClassifyChanges(t *testing.T) {
	tests := []struct {
		name   string
		files  []string
		expect []string // sorted
	}{
		{
			name:   "empty",
			files:  nil,
			expect: nil,
		},
		{
			name:   "header file triggers parse-headers, gen-proto, wasm-build",
			files:  []string{"src/foo.h"},
			expect: []string{"gen-proto", "parse-headers", "wasm-build"},
		},
		{
			name:   "cpp file triggers wasm-build only",
			files:  []string{"src/foo.cc"},
			expect: []string{"wasm-build"},
		},
		{
			name:   "BUILD triggers full chain",
			files:  []string{"pkg/BUILD"},
			expect: []string{"build", "gen-proto", "generate-build", "parse-headers", "wasm-build"},
		},
		{
			name:   "MODULE.bazel triggers full chain",
			files:  []string{"MODULE.bazel"},
			expect: []string{"build", "gen-proto", "generate-build", "parse-headers", "wasm-build"},
		},
		{
			name:   ".bazelrc triggers full chain",
			files:  []string{".bazelrc"},
			expect: []string{"build", "gen-proto", "generate-build", "parse-headers", "wasm-build"},
		},
		{
			name:   "wasmify.json triggers gen-proto + wasm-build",
			files:  []string{"path/to/wasmify.json"},
			expect: []string{"gen-proto", "wasm-build"},
		},
		{
			name:   "irrelevant file produces no phase",
			files:  []string{"docs/README.md", "misc/LICENSE"},
			expect: nil,
		},
		{
			name:   "mixed: header + cpp",
			files:  []string{"a.h", "b.cc"},
			expect: []string{"gen-proto", "parse-headers", "wasm-build"},
		},
		{
			name:   "hpp hxx c extensions",
			files:  []string{"a.hpp", "b.hxx", "c.c", "d.cxx", "e.cpp"},
			expect: []string{"gen-proto", "parse-headers", "wasm-build"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			phases := classifyChanges(tc.files)
			var got []string
			for p := range phases {
				got = append(got, p)
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, tc.expect) {
				t.Errorf("got %v, want %v", got, tc.expect)
			}
		})
	}
}

func TestIsHeaderFile(t *testing.T) {
	cases := map[string]bool{
		"foo.h":                true,
		"foo.hpp":              true,
		"foo.hxx":              true,
		"foo.H":                true, // case-insensitive via ToLower
		"foo.HPP":              true,
		"foo.cc":               false,
		"foo.c":                false,
		"foo.cxx":              false,
		"foo.cpp":              false,
		"foo":                  false,
		"":                     false,
		"/abs/path/header.hpp": true,
	}
	for path, want := range cases {
		if got := isHeaderFile(path); got != want {
			t.Errorf("isHeaderFile(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestFindDepFile(t *testing.T) {
	step := &buildjson.BuildStep{
		Args: []string{"gcc", "-c", "foo.cc", "-MF", "foo.d", "-o", "foo.o"},
	}
	if got := findDepFile(step); got != "foo.d" {
		t.Errorf("findDepFile = %q, want %q", got, "foo.d")
	}

	step2 := &buildjson.BuildStep{
		Args: []string{"gcc", "-c", "foo.cc"},
	}
	if got := findDepFile(step2); got != "" {
		t.Errorf("findDepFile empty = %q, want %q", got, "")
	}

	// -MF with no value at end should return ""
	step3 := &buildjson.BuildStep{
		Args: []string{"gcc", "-c", "foo.cc", "-MF"},
	}
	if got := findDepFile(step3); got != "" {
		t.Errorf("findDepFile trailing-MF = %q, want empty", got)
	}
}

func TestParseDependencyFile(t *testing.T) {
	dir := t.TempDir()
	depPath := filepath.Join(dir, "foo.d")
	content := "foo.o: foo.cc \\\n" +
		"  /usr/include/stdio.h \\\n" +
		"  /abs/header.h\n"
	if err := os.WriteFile(depPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	deps := parseDependencyFile(depPath)
	want := []string{"foo.cc", "/usr/include/stdio.h", "/abs/header.h"}
	if !reflect.DeepEqual(deps, want) {
		t.Errorf("deps = %v, want %v", deps, want)
	}

	// missing file returns nil
	if got := parseDependencyFile(filepath.Join(dir, "missing.d")); got != nil {
		t.Errorf("missing file got %v, want nil", got)
	}
}

func TestResolveIncludeFlag(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "inc")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	// Joined form: -Iinc → -I<dir>/inc (when dir exists)
	got := resolveIncludeFlag("-Iinc", dir)
	if got != "-I"+sub {
		t.Errorf("joined -Iinc = %q, want %q", got, "-I"+sub)
	}

	// Joined form with absolute path → unchanged
	got = resolveIncludeFlag("-I/abs/path", dir)
	if got != "-I/abs/path" {
		t.Errorf("absolute -I = %q", got)
	}

	// Non-include flag passes through
	got = resolveIncludeFlag("-DFOO=1", dir)
	if got != "-DFOO=1" {
		t.Errorf("non-include flag = %q", got)
	}

	// -isystem prefix
	got = resolveIncludeFlag("-isysteminc", dir)
	if got != "-isystem"+sub {
		t.Errorf("-isystem = %q", got)
	}

	// -iquote prefix
	got = resolveIncludeFlag("-iquoteinc", dir)
	if got != "-iquote"+sub {
		t.Errorf("-iquote = %q", got)
	}
}

func TestResolveIncludePath(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "real")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	// Absolute path returns as-is
	if got := resolveIncludePath("/abs/path", dir); got != "/abs/path" {
		t.Errorf("abs: %q", got)
	}

	// Empty workDir returns as-is
	if got := resolveIncludePath("relative", ""); got != "relative" {
		t.Errorf("no workDir: %q", got)
	}

	// Existing relative dir resolves
	if got := resolveIncludePath("real", dir); got != sub {
		t.Errorf("existing rel: got %q, want %q", got, sub)
	}

	// Non-existent relative returns as-is
	if got := resolveIncludePath("nothere", dir); got != "nothere" {
		t.Errorf("missing rel: %q", got)
	}
}

func TestGenerateUmbrellaHeader(t *testing.T) {
	headers := []string{"/a/x.h", "/b/y.h"}
	path, err := generateUmbrellaHeader(headers)
	if err != nil {
		t.Fatalf("generateUmbrellaHeader: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, h := range headers {
		if !strings.Contains(s, "#include \""+h+"\"") {
			t.Errorf("umbrella missing include for %q:\n%s", h, s)
		}
	}
	if !strings.Contains(s, "Auto-generated") {
		t.Errorf("expected generated-comment marker")
	}
}

func TestDeduplicatorMergeAPISpec(t *testing.T) {
	d := newDeduplicator()

	dst := &apispec.APISpec{}
	src1 := &apispec.APISpec{
		Namespace: "ns1",
		Functions: []apispec.Function{{Name: "f", QualName: "ns1::f"}},
		Classes:   []apispec.Class{{Name: "C", QualName: "ns1::C"}},
		Enums:     []apispec.Enum{{Name: "E", QualName: "ns1::E"}},
	}
	d.mergeAPISpec(dst, src1)

	// Duplicate should not be re-added
	src2 := &apispec.APISpec{
		Namespace: "ns2", // dst already has ns1, so this is ignored
		Functions: []apispec.Function{
			{Name: "f", QualName: "ns1::f"}, // duplicate
			{Name: "g", QualName: "ns1::g"},
		},
		Classes: []apispec.Class{
			{Name: "C", QualName: "ns1::C"}, // duplicate
			{Name: "D", QualName: "ns1::D"},
		},
		Enums: []apispec.Enum{
			{Name: "E", QualName: "ns1::E"}, // duplicate
			{Name: "F", QualName: "ns1::F"},
		},
	}
	d.mergeAPISpec(dst, src2)

	if dst.Namespace != "ns1" {
		t.Errorf("namespace = %q, want ns1", dst.Namespace)
	}
	if len(dst.Functions) != 2 {
		t.Errorf("functions = %d, want 2", len(dst.Functions))
	}
	if len(dst.Classes) != 2 {
		t.Errorf("classes = %d, want 2", len(dst.Classes))
	}
	if len(dst.Enums) != 2 {
		t.Errorf("enums = %d, want 2", len(dst.Enums))
	}

	// dst starting without namespace takes src's
	dst2 := &apispec.APISpec{}
	d2 := newDeduplicator()
	d2.mergeAPISpec(dst2, &apispec.APISpec{Namespace: "taken"})
	if dst2.Namespace != "taken" {
		t.Errorf("namespace not propagated: %q", dst2.Namespace)
	}
}

func TestCollectCompileFlagsFromBuild(t *testing.T) {
	dir := t.TempDir()
	inc := filepath.Join(dir, "inc")
	inc2 := filepath.Join(dir, "inc2")
	if err := os.MkdirAll(inc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(inc2, 0o755); err != nil {
		t.Fatal(err)
	}

	b := &buildjson.BuildJSON{
		Steps: []buildjson.BuildStep{
			{
				Type:    buildjson.StepCompile,
				WorkDir: dir,
				// Joined -I and separate -I <path>
				Args: []string{"clang++", "-c", "foo.cc", "-Iinc", "-I", "inc2", "-DFOO=1"},
			},
			{
				// Duplicate flag should be deduplicated
				Type:    buildjson.StepCompile,
				WorkDir: dir,
				Args:    []string{"clang++", "-c", "bar.cc", "-Iinc", "-DFOO=1", "-I", "inc2"},
			},
			{
				// Non-compile step is ignored
				Type: buildjson.StepLink,
				Args: []string{"clang++", "-o", "foo", "foo.o"},
			},
		},
	}
	flags := collectCompileFlagsFromBuild(b)
	// Should contain resolved -I and -D
	found := map[string]bool{}
	for _, f := range flags {
		found[f] = true
	}
	if !found["-I"+inc] {
		t.Errorf("expected resolved -I<abs/inc> in flags, got %v", flags)
	}
	if !found["-DFOO=1"] {
		t.Errorf("expected -DFOO=1 in flags, got %v", flags)
	}
	// -I inc appears only once (deduplicated)
	count := 0
	for _, f := range flags {
		if f == "-I"+inc {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected -I deduplicated, count=%d", count)
	}
}

// --- git helpers ------------------------------------------------------------

func TestGitHeadCommitAndBranch(t *testing.T) {
	dir := gitInitRepo(t)

	commit, err := gitHeadCommit(dir)
	if err != nil {
		t.Fatalf("gitHeadCommit: %v", err)
	}
	if len(commit) != 40 { // SHA-1 hex
		t.Errorf("commit hash length = %d, want 40: %q", len(commit), commit)
	}

	branch, err := gitCurrentBranch(dir)
	if err != nil {
		t.Fatalf("gitCurrentBranch: %v", err)
	}
	if branch != "main" {
		t.Errorf("branch = %q, want main", branch)
	}

	// Non-repo returns error
	nodir := t.TempDir()
	if _, err := gitHeadCommit(nodir); err == nil {
		t.Errorf("expected error from non-repo")
	}
	if _, err := gitCurrentBranch(nodir); err == nil {
		t.Errorf("expected error from non-repo for branch")
	}
}

func TestGitDiffFiles(t *testing.T) {
	dir := gitInitRepo(t)
	first, err := gitHeadCommit(dir)
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}

	// second commit
	if err := os.WriteFile(filepath.Join(dir, "foo.h"), []byte("// foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bar.cc"), []byte("// bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "add")
	second, err := gitHeadCommit(dir)
	if err != nil {
		t.Fatalf("second commit: %v", err)
	}

	files, err := gitDiffFiles(dir, first, second)
	if err != nil {
		t.Fatalf("gitDiffFiles: %v", err)
	}
	sort.Strings(files)
	want := []string{"bar.cc", "foo.h"}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("diff files = %v, want %v", files, want)
	}

	// Non-repo returns error
	if _, err := gitDiffFiles(t.TempDir(), first, second); err == nil {
		t.Errorf("expected error for non-repo")
	}
}

// --- resolveProject / resolveOutputDir --------------------------------------

func TestResolveProject(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()

	abs, dataDir, err := resolveProject(project)
	if err != nil {
		t.Fatalf("resolveProject: %v", err)
	}
	if abs != project {
		// EvalSymlinks may differ; just ensure it's absolute and exists
		if !filepath.IsAbs(abs) {
			t.Errorf("abs not absolute: %q", abs)
		}
	}
	if !filepath.IsAbs(dataDir) {
		t.Errorf("dataDir not absolute: %q", dataDir)
	}
	if _, err := os.Stat(dataDir); err != nil {
		t.Errorf("dataDir should exist: %v", err)
	}

	// Non-existent path returns error
	if _, _, err := resolveProject(filepath.Join(project, "does-not-exist")); err == nil {
		t.Errorf("expected error for missing path")
	}

	// File (not dir) returns error
	file := filepath.Join(project, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveProject(file); err == nil {
		t.Errorf("expected error for file, not dir")
	}
}

func TestResolveOutputDir(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()

	// No output-dir: outDir defaults to cwd, dataDir = <cwd>/.wasmify.
	abs, dataDir, outDir, err := resolveOutputDir(project, "")
	if err != nil {
		t.Fatalf("resolveOutputDir: %v", err)
	}
	cwd, _ := os.Getwd()
	if outDir != cwd {
		t.Errorf("outDir = %q, want cwd %q", outDir, cwd)
	}
	if want := filepath.Join(cwd, ".wasmify"); dataDir != want {
		t.Errorf("dataDir = %q, want %q", dataDir, want)
	}
	if !filepath.IsAbs(abs) {
		t.Errorf("abs not absolute: %q", abs)
	}

	// With output-dir: separate
	out := filepath.Join(t.TempDir(), "gen")
	_, _, outDir2, err := resolveOutputDir(project, out)
	if err != nil {
		t.Fatalf("resolveOutputDir out: %v", err)
	}
	if outDir2 != out {
		t.Errorf("outDir = %q, want %q", outDir2, out)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("output-dir should be created: %v", err)
	}

	// Bad project path
	if _, _, _, err := resolveOutputDir("/does/not/exist/xyz", ""); err == nil {
		t.Errorf("expected error for missing project path")
	}

	// output-dir creation fails (parent is a file)
	fileParent := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(fileParent, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	badOut := filepath.Join(fileParent, "cant-create")
	if _, _, _, err := resolveOutputDir(project, badOut); err == nil {
		t.Errorf("expected MkdirAll error")
	}
}

// --- cmdInit ----------------------------------------------------------------

func TestCmdInit_NoOutputDir(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	chdirTemp(t) // avoid dropping SKILL.md into the package dir

	out, err := captureStdout(t, func() error {
		_, err := captureStderr(t, func() error {
			return cmdInit([]string{project})
		})
		return err
	})
	if err != nil {
		t.Fatalf("cmdInit: %v", err)
	}
	var result map[string]string
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("parse JSON: %v (%q)", err, out)
	}
	if result["data_dir"] == "" {
		t.Errorf("data_dir missing")
	}
	cwd, _ := os.Getwd()
	if result["output_dir"] != cwd {
		t.Errorf("output_dir = %q, want cwd %q", result["output_dir"], cwd)
	}
	// SKILL.md should be installed in the cwd (= output-dir).
	if result["skill_path"] == "" {
		t.Error("skill_path missing")
	} else if _, err := os.Stat(result["skill_path"]); err != nil {
		t.Errorf("skill file not found: %v", err)
	}
}

func TestCmdInit_WithOutputDir(t *testing.T) {
	isolatedXDG(t)
	project := gitInitRepo(t)
	outDir := t.TempDir()

	out, err := captureStdout(t, func() error {
		_, err := captureStderr(t, func() error {
			return cmdInit([]string{project, "--output-dir", outDir})
		})
		return err
	})
	if err != nil {
		t.Fatalf("cmdInit: %v", err)
	}
	var result map[string]string
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("parse JSON: %v (%q)", err, out)
	}
	if result["output_dir"] != outDir {
		t.Errorf("output_dir = %q, want %q", result["output_dir"], outDir)
	}
	// wasmify.json should exist
	if _, err := os.Stat(filepath.Join(outDir, "wasmify.json")); err != nil {
		t.Errorf("wasmify.json should exist: %v", err)
	}
	// SKILL.md should land under <outDir>/.claude/skills/wasmify/
	skillPath := filepath.Join(outDir, ".claude", "skills", "wasmify", "SKILL.md")
	if _, err := os.Stat(skillPath); err != nil {
		t.Errorf("SKILL.md should exist at %s: %v", skillPath, err)
	}
}

func TestCmdInit_Errors(t *testing.T) {
	isolatedXDG(t)
	if err := cmdInit(nil); err == nil {
		t.Error("expected usage error")
	}
	// Non-existent path
	if err := cmdInit([]string{"/does/not/exist/xyz"}); err == nil {
		t.Error("expected error for missing project path")
	}
}

// --- cmdStatus --------------------------------------------------------------

func TestCmdStatus_Empty(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)

	out, err := captureStdout(t, func() error {
		return cmdStatus(nil)
	})
	if err != nil {
		t.Fatalf("cmdStatus: %v", err)
	}
	var status map[string]any
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if status["data_dir"] == nil {
		t.Errorf("data_dir missing")
	}
	// All subfields should be nil initially
	for _, k := range []string{"cache", "arch", "build_json"} {
		if status[k] != nil {
			t.Errorf("%s should be nil (got %v)", k, status[k])
		}
	}
}

func TestCmdStatus_WithData(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()
	dataDir := filepath.Join(outDir, ".wasmify")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Pre-populate arch/cache/build.json.
	a := &arch.Arch{
		Version: "1.0",
		Project: arch.Project{Name: "p", RootDir: project, Language: "c++"},
	}
	if err := arch.Save(outDir, a); err != nil {
		t.Fatal(err)
	}
	c := cache.New()
	c.CompletePhase(cache.PhaseAnalyze)
	if err := c.Save(dataDir); err != nil {
		t.Fatal(err)
	}
	b := &buildjson.BuildJSON{
		Version:    "1.0",
		CapturedAt: "2025-01-01T00:00:00Z",
		Steps:      []buildjson.BuildStep{{ID: 1, Type: buildjson.StepCompile}},
	}
	if err := buildjson.Save(outDir, b); err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(t, func() error {
		return cmdStatus(nil)
	})
	if err != nil {
		t.Fatalf("cmdStatus: %v", err)
	}
	var status map[string]any
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}
	if status["cache"] == nil || status["arch"] == nil || status["build_json"] == nil {
		t.Errorf("expected populated fields: %v", status)
	}
}

func TestCmdStatus_NoArg(t *testing.T) {
	isolatedXDG(t)
	// No wasmify.json in the isolated cwd → loadProjectFromConfig fails.
	if err := cmdStatus(nil); err == nil {
		t.Error("expected error when wasmify.json missing")
	}
}

// --- cmdSaveArch ------------------------------------------------------------

func TestCmdSaveArch(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()
	dataDir := filepath.Join(outDir, ".wasmify")

	validArch := arch.Arch{
		Project: arch.Project{Name: "p", RootDir: ".", Language: "c++"},
		BuildSystem: arch.BuildSystem{
			Type:  "bazel",
			Files: []string{"WORKSPACE"},
		},
		Targets: []arch.Target{
			{Name: "mylib", Type: "library"},
		},
		Commands: arch.BuildCommands{Build: "bazel build"},
	}
	data, err := json.Marshal(validArch)
	if err != nil {
		t.Fatal(err)
	}

	err = withStdin(t, string(data), func() error {
		_, innerErr := captureStdout(t, func() error {
			return cmdSaveArch(nil)
		})
		return innerErr
	})
	if err != nil {
		t.Fatalf("cmdSaveArch: %v", err)
	}

	// Verify arch.json persisted. arch.json is saved to outDir (= cwd);
	// cache lives in dataDir (= <outDir>/.wasmify).
	a, err := arch.Load(outDir)
	if err != nil {
		t.Fatal(err)
	}
	if a == nil {
		t.Fatal("arch.json not saved")
	}
	if a.Version != "1.0" {
		t.Errorf("version = %q, want 1.0", a.Version)
	}
	if a.AnalyzedAt == "" {
		t.Errorf("AnalyzedAt should be set")
	}
	c, err := cache.Load(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if c == nil || !c.IsCompleted(cache.PhaseAnalyze) {
		t.Errorf("PhaseAnalyze should be completed")
	}
}

func TestCmdSaveArch_MissingFields(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)

	// Missing everything → expect "missing required fields" error
	err := withStdin(t, "{}", func() error {
		return cmdSaveArch(nil)
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "missing required fields") {
		t.Errorf("got %v", err)
	}
}

func TestCmdSaveArch_InvalidJSON(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	err := withStdin(t, "{not json", func() error {
		return cmdSaveArch(nil)
	})
	if err == nil {
		t.Fatal("expected invalid JSON error")
	}
}

func TestCmdSaveArch_NoArg(t *testing.T) {
	isolatedXDG(t)
	// No wasmify.json in cwd → loadProjectFromConfig fails.
	if err := cmdSaveArch(nil); err == nil {
		t.Error("expected error when wasmify.json missing")
	}
}

// TestCmdSaveArch_RejectsAbsolutePaths ensures arch.json cannot embed
// developer-machine absolute paths. arch.json is always committed to git
// (it lives in outDir = cwd), so paths must be portable.
func TestCmdSaveArch_RejectsAbsolutePaths(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	mk := func(root string, srcDirs []string, headers []string, files []string) arch.Arch {
		return arch.Arch{
			Project: arch.Project{Name: "p", RootDir: root, Language: "c++"},
			BuildSystem: arch.BuildSystem{
				Type:  "bazel",
				Files: files,
			},
			Targets: []arch.Target{
				{Name: "t", Type: "library", SourceDirs: srcDirs, PublicHeaders: headers},
			},
			Commands: arch.BuildCommands{Build: "bazel build"},
		}
	}
	cases := []struct {
		name string
		a    arch.Arch
	}{
		{"root_dir", mk("/abs/root", nil, nil, []string{"BUILD"})},
		{"source_dirs", mk(".", []string{"/abs/src"}, nil, []string{"BUILD"})},
		{"public_headers", mk(".", nil, []string{"/abs/h.h"}, []string{"BUILD"})},
		{"build_system.files", mk(".", nil, nil, []string{"/abs/BUILD"})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, _ := json.Marshal(tc.a)
			err := withStdin(t, string(data), func() error {
				return cmdSaveArch(nil)
			})
			if err == nil {
				t.Fatal("expected save-arch to reject absolute path")
			}
			if !strings.Contains(err.Error(), "relative") {
				t.Errorf("error should mention 'relative', got: %v", err)
			}
		})
	}
}


// --- cmdClassify ------------------------------------------------------------

func TestCmdClassify_WithTarget(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()
	dataDir := filepath.Join(outDir, ".wasmify")
	a := &arch.Arch{
		Version: "1.0",
		Project: arch.Project{Name: "p", RootDir: project, Language: "c++"},
		Targets: []arch.Target{
			{Name: "libfoo", Type: "library"},
			{Name: "libbar", Type: "library"},
		},
	}
	if err := arch.Save(outDir, a); err != nil {
		t.Fatal(err)
	}

	_, err := captureStderr(t, func() error {
		return cmdClassify([]string{"--target", "libbar"})
	})
	if err != nil {
		t.Fatalf("cmdClassify: %v", err)
	}
	loaded, err := arch.Load(outDir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Selection == nil || loaded.Selection.TargetName != "libbar" {
		t.Errorf("selection not saved: %+v", loaded.Selection)
	}
	c, _ := cache.Load(dataDir)
	if c == nil || !c.IsCompleted(cache.PhaseClassify) {
		t.Errorf("PhaseClassify not recorded")
	}
}

func TestCmdClassify_Errors(t *testing.T) {
	isolatedXDG(t)

	// missing wasmify.json → loadProjectFromConfig fails
	if err := cmdClassify(nil); err == nil {
		t.Error("expected error when wasmify.json missing")
	}

	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()
	// arch.json missing
	if err := cmdClassify(nil); err == nil {
		t.Error("expected arch.json missing error")
	}

	// no targets
	a := &arch.Arch{Project: arch.Project{Name: "p", RootDir: project, Language: "c++"}}
	if err := arch.Save(outDir, a); err != nil {
		t.Fatal(err)
	}
	if err := cmdClassify(nil); err == nil {
		t.Error("expected no targets error")
	}

	// unknown target
	a.Targets = []arch.Target{{Name: "a", Type: "library"}}
	if err := arch.Save(outDir, a); err != nil {
		t.Fatal(err)
	}
	if err := cmdClassify([]string{"--target", "nope"}); err == nil {
		t.Error("expected unknown target error")
	}
}

// --- cmdGenerateBuild -------------------------------------------------------

func TestCmdGenerateBuild(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()
	dataDir := filepath.Join(outDir, ".wasmify")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a minimal build.log in JSON Lines format. build.log lives in
	// dataDir (under .wasmify/); the generated build.json is saved to outDir
	// (the git-managed root).
	entry := map[string]any{
		"timestamp":  "2025-01-01T00:00:00Z",
		"tool":       "clang",
		"executable": "/usr/bin/clang",
		"args":       []string{"clang", "-c", "foo.cc", "-o", "foo.o"},
		"work_dir":   project,
	}
	line, _ := json.Marshal(entry)
	if err := os.WriteFile(filepath.Join(dataDir, "build.log"), append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(t, func() error {
		return cmdGenerateBuild(nil)
	})
	if err != nil {
		t.Fatalf("cmdGenerateBuild: %v", err)
	}
	if !strings.Contains(out, "build.json generated") {
		t.Errorf("output missing summary: %q", out)
	}
	b, err := buildjson.Load(outDir)
	if err != nil {
		t.Fatal(err)
	}
	if b == nil || len(b.Steps) == 0 {
		t.Errorf("build.json empty")
	}
	c, _ := cache.Load(dataDir)
	if c == nil || !c.IsCompleted(cache.PhaseOutput) {
		t.Errorf("PhaseOutput not completed")
	}
}

func TestCmdGenerateBuild_Errors(t *testing.T) {
	isolatedXDG(t)

	// no wasmify.json in cwd → loadProjectFromConfig fails
	if err := cmdGenerateBuild(nil); err == nil {
		t.Error("expected error when wasmify.json missing")
	}

	// missing build.log
	project := t.TempDir()
	initTestOutDir(t, project)
	if err := cmdGenerateBuild(nil); err == nil {
		t.Error("expected error: no captures")
	}
}

// TestCmdGenerateBuild_WithArch covers the branch where arch.json populates
// the ProjectInfo in build.json.
func TestCmdGenerateBuild_WithArch(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()
	dataDir := filepath.Join(outDir, ".wasmify")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	a := &arch.Arch{
		Version:     "1.0",
		Project:     arch.Project{Name: "myproj", RootDir: ".", Language: "c++"},
		BuildSystem: arch.BuildSystem{Type: "bazel", Files: []string{"WORKSPACE"}},
		Targets:     []arch.Target{{Name: "tgt", Type: "library"}},
		Commands:    arch.BuildCommands{Build: "bazel build"},
		Selection:   &arch.Selection{TargetName: "tgt", BuildType: "library"},
	}
	// arch.json is committed alongside the project root (outDir), not inside
	// the ephemeral .wasmify/ data dir.
	if err := arch.Save(outDir, a); err != nil {
		t.Fatal(err)
	}

	entry := map[string]any{
		"timestamp":  "2025-01-01T00:00:00Z",
		"tool":       "clang",
		"executable": "/usr/bin/clang",
		"args":       []string{"clang", "-c", "foo.cc", "-o", "foo.o"},
		"work_dir":   project,
	}
	line, _ := json.Marshal(entry)
	if err := os.WriteFile(filepath.Join(dataDir, "build.log"), append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := captureStdout(t, func() error {
		return cmdGenerateBuild(nil)
	})
	if err != nil {
		t.Fatalf("cmdGenerateBuild: %v", err)
	}
	b, _ := buildjson.Load(outDir)
	if b == nil || b.Project.Name != "myproj" {
		t.Errorf("project info not populated: %+v", b)
	}
	if b.Project.BuildType != "library" {
		t.Errorf("build_type not set: %+v", b.Project)
	}
}

// --- cmdValidateBuild / cmdBuild / cmdWasmBuild / cmdInstallSDK / cmdGenProto
//     Only early error paths are covered (subprocess paths need environment).

func TestCmdValidateBuild_NoArg(t *testing.T) {
	isolatedXDG(t)
	// No wasmify.json → loadProjectFromConfig fails.
	if err := cmdValidateBuild(nil); err == nil {
		t.Error("expected error when wasmify.json missing")
	}
}

func TestCmdValidateBuild_MissingBuildJSON(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	if err := cmdValidateBuild(nil); err == nil {
		t.Error("expected error for missing build.json")
	}
}

func TestCmdBuild_NoArg(t *testing.T) {
	isolatedXDG(t)
	// No wasmify.json → loadProjectFromConfig fails.
	if err := cmdBuild(nil); err == nil {
		t.Error("expected error when wasmify.json missing")
	}
}

func TestCmdBuild_NoArchJSON(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	// cmdBuild without "--" and no arch.json should fail at deriveBuildCommand
	if err := cmdBuild(nil); err == nil {
		t.Error("expected error: no build command and no arch.json")
	}
}

func TestCmdWasmBuild_NoArg(t *testing.T) {
	isolatedXDG(t)
	// No wasmify.json → loadProjectFromConfig fails.
	if err := cmdWasmBuild(nil); err == nil {
		t.Error("expected error when wasmify.json missing")
	}
}

func TestCmdParseHeaders_NoArg(t *testing.T) {
	isolatedXDG(t)
	// No wasmify.json → loadProjectFromConfig fails.
	if err := cmdParseHeaders(nil); err == nil {
		t.Error("expected error when wasmify.json missing")
	}
}

func TestCmdParseHeaders_MissingBuildJSON(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	// No --header, no build.json → error
	err := cmdParseHeaders(nil)
	if err == nil {
		t.Error("expected error: no headers and no build.json")
	}
}

func TestCmdGenProto_NoArg(t *testing.T) {
	isolatedXDG(t)
	// No wasmify.json → loadProjectFromConfig fails.
	if err := cmdGenProto(nil); err == nil {
		t.Error("expected error when wasmify.json missing")
	}
}

func TestCmdGenProto_MissingAPISpec(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	err := cmdGenProto(nil)
	if err == nil {
		t.Error("expected error: no api-spec.json")
	}
}

// --- cmdUpdate --------------------------------------------------------------

func TestCmdUpdate_NoArg(t *testing.T) {
	isolatedXDG(t)
	// No wasmify.json → loadProjectFromConfig fails.
	if err := cmdUpdate(nil); err == nil {
		t.Error("expected error when wasmify.json missing")
	}
}

func TestCmdUpdate_UpToDate(t *testing.T) {
	isolatedXDG(t)
	project := gitInitRepo(t)
	out, _ := os.Getwd() // isolatedXDG already chdirs into a fresh tempdir

	// Pre-populate wasmify.json with the current HEAD so update sees no change
	commit, err := gitHeadCommit(project)
	if err != nil {
		t.Fatal(err)
	}
	s := state.New(project)
	s.UpdateUpstream(commit, "main")
	if err := state.Save(out, s); err != nil {
		t.Fatal(err)
	}

	stderr, err := captureStderr(t, func() error {
		return cmdUpdate(nil)
	})
	if err != nil {
		t.Fatalf("cmdUpdate: %v", err)
	}
	if !strings.Contains(stderr, "up to date") {
		t.Errorf("expected up-to-date message: %q", stderr)
	}
}

func TestCmdUpdate_NoRelevantChanges(t *testing.T) {
	isolatedXDG(t)
	project := gitInitRepo(t)
	out, _ := os.Getwd()

	// First commit has README.md only; save state pointing at HEAD
	first, err := gitHeadCommit(project)
	if err != nil {
		t.Fatal(err)
	}
	s := state.New(project)
	s.UpdateUpstream(first, "main")
	if err := state.Save(out, s); err != nil {
		t.Fatal(err)
	}

	// Make a non-relevant change (e.g., docs/NEWFILE)
	_ = os.MkdirAll(filepath.Join(project, "docs"), 0o755)
	if err := os.WriteFile(filepath.Join(project, "docs/NOTES.txt"), []byte("irrelevant\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, project, "add", ".")
	runGit(t, project, "commit", "-m", "docs")

	stderr, err := captureStderr(t, func() error {
		return cmdUpdate(nil)
	})
	if err != nil {
		t.Fatalf("cmdUpdate: %v", err)
	}
	if !strings.Contains(stderr, "No relevant changes") {
		t.Errorf("expected 'No relevant changes': %q", stderr)
	}

	// state commit should be bumped
	loaded, _ := state.Load(out)
	if loaded == nil {
		t.Fatal("state missing")
	}
	if loaded.Upstream.Commit == first {
		t.Errorf("state commit not updated")
	}
}

func TestCmdUpdate_NoGitRepo(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir() // not a git repo
	initTestOutDir(t, project)
	err := cmdUpdate(nil)
	if err == nil {
		t.Error("expected error: not a git repo")
	}
}

// --- deriveBuildCommand -----------------------------------------------------

func TestDeriveBuildCommand(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	_, dataDir, _ := resolveProject(project)

	// No arch.json
	if _, err := deriveBuildCommand(project, ""); err == nil {
		t.Error("expected error: no arch.json")
	}

	// arch.json with no selection
	a := &arch.Arch{
		Project:     arch.Project{Name: "p", RootDir: project, Language: "c++"},
		BuildSystem: arch.BuildSystem{Type: "bazel"},
		Targets:     []arch.Target{{Name: "t1", Type: "library", BuildTarget: "//pkg:t1"}},
		Commands:    arch.BuildCommands{Build: "bazel build"},
	}
	if err := arch.Save(dataDir, a); err != nil {
		t.Fatal(err)
	}
	if _, err := deriveBuildCommand(project, ""); err == nil {
		t.Error("expected error: no selection")
	}

	// With selection and build target
	a.Selection = &arch.Selection{TargetName: "t1", BuildType: "library"}
	if err := arch.Save(dataDir, a); err != nil {
		t.Fatal(err)
	}
	cmd, err := deriveBuildCommand(project, "")
	if err != nil {
		t.Fatalf("deriveBuildCommand: %v", err)
	}
	if cmd != "bazel build //pkg:t1" {
		t.Errorf("cmd = %q, want %q", cmd, "bazel build //pkg:t1")
	}

	// No build command set
	a.Commands.Build = ""
	if err := arch.Save(dataDir, a); err != nil {
		t.Fatal(err)
	}
	if _, err := deriveBuildCommand(project, ""); err == nil {
		t.Error("expected error: no build command")
	}
}

// --- updateState ------------------------------------------------------------

func TestUpdateState(t *testing.T) {
	// Non-git project: gitHeadCommit returns error, but updateState logs and proceeds.
	project := t.TempDir()
	out := t.TempDir()

	updateState(out, project, "gen-proto", []string{"a", "b"})

	s, err := state.Load(out)
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("state not saved")
	}
	if s.Upstream.Path != project {
		t.Errorf("upstream path = %q, want %q", s.Upstream.Path, project)
	}
	if p, ok := s.Phases["gen-proto"]; !ok || p == nil {
		t.Errorf("phase gen-proto missing")
	} else if !reflect.DeepEqual(p.OutputFiles, []string{"a", "b"}) {
		t.Errorf("output files = %v", p.OutputFiles)
	}
}

// --- cmdInstallSDK ---------------------------------------------------------

func TestCmdInstallSDK_ArgParse(t *testing.T) {
	// When no path is given it will try to detect/install.
	// We can at least exercise the arg-parse branch with --path by pointing
	// to a temp dir that's clearly not an SDK (causing install attempt).
	// To avoid triggering a real download in CI, we don't invoke without
	// a path. Instead we exercise --path parsing with an existing installed
	// SDK detection being unlikely — just ensure it doesn't panic and
	// returns something (nil or error both acceptable here).
	//
	// The underlying InstallWasiSDK may try to hit the network; we skip
	// this test unless an env var enables it.
	if testing.Short() {
		t.Skip("skipping install-sdk in short mode")
	}
	if os.Getenv("WASMIFY_TEST_INSTALL_SDK") == "" {
		t.Skip("set WASMIFY_TEST_INSTALL_SDK=1 to enable install-sdk test")
	}
	_ = cmdInstallSDK([]string{"--path", t.TempDir()})
}

// --- main dispatcher smoke test --------------------------------------------

func TestPrintUsage(t *testing.T) {
	// printUsage writes to os.Stderr; just make sure it doesn't panic.
	_, _ = captureStderr(t, func() error {
		printUsage()
		return nil
	})
}
