package wrapper

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func lookPathOrSkip(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not found on PATH: %v", name, err)
	}
	return p
}

func TestNormalizeEnvKey(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"cc", "CC"},
		{"c++", "CPLUSPLUS"},
		{"g++", "GPLUSPLUS"},
		{"clang++", "CLANGPLUSPLUS"},
		{"ar", "AR"},
		{"ld", "LD"},
	}
	for _, tc := range tests {
		if got := normalizeEnvKey(tc.in); got != tc.want {
			t.Errorf("normalizeEnvKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsWrapperInvocation(t *testing.T) {
	origArgs := os.Args
	defer func() { os.Args = origArgs }()

	cases := []struct {
		argv0 string
		want  bool
	}{
		{"/tmp/wrapper/cc", true},
		{"cc", true},
		{"/path/c++", true},
		{"clang", true},
		{"clang++", true},
		{"ar", true},
		{"ld", true},
		{"ranlib", true},
		{"strip", true},
		{"/usr/bin/wasmify", false},
		{"go", false},
	}
	for _, tc := range cases {
		os.Args = []string{tc.argv0}
		if got := IsWrapperInvocation(); got != tc.want {
			t.Errorf("IsWrapperInvocation(argv0=%q) = %v, want %v", tc.argv0, got, tc.want)
		}
	}
}

func TestEnvForBuild_OverridesAndResolves(t *testing.T) {
	tmp := t.TempDir()
	wrapperDir := filepath.Join(tmp, "wrap")
	if err := os.MkdirAll(wrapperDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(tmp, "build.log")

	// Put a fake 'cc' in a dir on PATH so exec.LookPath succeeds.
	fakeBin := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeCC := filepath.Join(fakeBin, "cc")
	if err := os.WriteFile(fakeCC, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)

	input := []string{
		"CC=/should/be/replaced",
		"CXX=/should/be/replaced",
		"AR=/should/be/replaced",
		"LD=/should/be/replaced",
		"RANLIB=/should/be/replaced",
		"STRIP=/should/be/replaced",
		"KEEP_ME=value",
	}
	env, err := EnvForBuild(wrapperDir, logFile, input)
	if err != nil {
		t.Fatal(err)
	}
	m := envToMap(env)

	// Overridden entries point into wrapperDir
	for _, key := range []string{"CC", "CXX", "AR", "LD", "RANLIB", "STRIP"} {
		v, ok := m[key]
		if !ok {
			t.Errorf("missing %s", key)
			continue
		}
		if !strings.HasPrefix(v, wrapperDir) {
			t.Errorf("%s = %q, want prefix %q", key, v, wrapperDir)
		}
	}
	// Log file env var set
	if m["WASMIFY_LOG_FILE"] != logFile {
		t.Errorf("WASMIFY_LOG_FILE = %q, want %q", m["WASMIFY_LOG_FILE"], logFile)
	}
	// Preserved env var
	if m["KEEP_ME"] != "value" {
		t.Errorf("KEEP_ME = %q, want value", m["KEEP_ME"])
	}
	// Should resolve real cc via our fake path
	if m["WASMIFY_REAL_CC"] != fakeCC {
		t.Errorf("WASMIFY_REAL_CC = %q, want %q", m["WASMIFY_REAL_CC"], fakeCC)
	}
	// Tools not present on PATH are silently skipped (e.g. ranlib won't be found).
}

func TestEnvForBuild_SkipsUnavailableTools(t *testing.T) {
	tmp := t.TempDir()
	// PATH contains only an empty directory, so no tools will be found.
	t.Setenv("PATH", tmp)
	env, err := EnvForBuild(tmp, filepath.Join(tmp, "build.log"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range env {
		if strings.HasPrefix(e, "WASMIFY_REAL_") {
			t.Errorf("unexpected resolved real tool: %s", e)
		}
	}
}

func TestParseLog_NonExistent(t *testing.T) {
	tmp := t.TempDir()
	entries, err := ParseLog(filepath.Join(tmp, "missing.log"))
	if err != nil {
		t.Fatalf("ParseLog() error: %v", err)
	}
	if entries != nil {
		t.Errorf("ParseLog(missing) = %v, want nil", entries)
	}
}

func TestParseLog_ValidAndMalformedLines(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "build.log")

	e1 := LogEntry{
		Timestamp:  "2025-01-01T00:00:00Z",
		Tool:       "clang",
		Executable: "/usr/bin/clang",
		Args:       []string{"-c", "foo.c"},
		WorkDir:    "/tmp",
	}
	e2 := LogEntry{
		Timestamp:  "2025-01-01T00:00:01Z",
		Tool:       "ld",
		Executable: "/usr/bin/ld",
		Args:       []string{"-o", "bin"},
		WorkDir:    "/tmp",
	}

	b1, _ := json.Marshal(e1)
	b2, _ := json.Marshal(e2)
	content := string(b1) + "\n" + "not valid json\n" + string(b2) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := ParseLog(path)
	if err != nil {
		t.Fatalf("ParseLog() error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (skipping malformed), got %d", len(entries))
	}
	if entries[0].Tool != "clang" || entries[1].Tool != "ld" {
		t.Errorf("entries = %+v", entries)
	}
}

func TestParseLog_OpenError(t *testing.T) {
	tmp := t.TempDir()
	// A directory named like a log file causes Open to return an error
	// distinct from IsNotExist.
	dir := filepath.Join(tmp, "build.log")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseLog(dir); err == nil {
		t.Error("expected error when log path is a directory")
	}
}

func TestSetup_CreatesWrapperStructure(t *testing.T) {
	wrapperDir, logFile, cleanup, err := Setup()
	if err != nil {
		t.Fatalf("Setup() error: %v", err)
	}
	defer cleanup()

	// Wrapper directory should exist
	if _, err := os.Stat(wrapperDir); err != nil {
		t.Fatalf("wrapperDir not created: %v", err)
	}
	// Log file path should live inside wrapperDir
	if filepath.Dir(logFile) != wrapperDir {
		t.Errorf("logFile %q not inside wrapperDir %q", logFile, wrapperDir)
	}

	// The main wrapper binary should exist.
	wrapperBin := filepath.Join(wrapperDir, "wasmify-wrapper")
	if _, err := os.Stat(wrapperBin); err != nil {
		t.Errorf("wrapper binary missing: %v", err)
	}

	// Every tool name should have a symlink pointing at the wrapper binary.
	for _, name := range toolNames {
		link := filepath.Join(wrapperDir, name)
		info, err := os.Lstat(link)
		if err != nil {
			t.Errorf("missing symlink for %s: %v", name, err)
			continue
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s is not a symlink", name)
		}
	}

	// Cleanup should remove the dir.
	cleanup()
	if _, err := os.Stat(wrapperDir); !os.IsNotExist(err) {
		t.Errorf("cleanup did not remove wrapperDir: err=%v", err)
	}
}

func TestRunAsWrapper_MissingLogFileEnv(t *testing.T) {
	origArgs := os.Args
	defer func() { os.Args = origArgs }()
	os.Args = []string{"cc"}
	t.Setenv("WASMIFY_LOG_FILE", "")
	err := RunAsWrapper()
	if err == nil {
		t.Error("expected error when WASMIFY_LOG_FILE unset")
	}
}

func TestRunAsWrapper_MissingRealToolEnv(t *testing.T) {
	tmp := t.TempDir()
	logFile := filepath.Join(tmp, "build.log")
	origArgs := os.Args
	defer func() { os.Args = origArgs }()
	os.Args = []string{"cc"}
	t.Setenv("WASMIFY_LOG_FILE", logFile)
	t.Setenv("WASMIFY_REAL_CC", "")
	err := RunAsWrapper()
	if err == nil {
		t.Error("expected error when WASMIFY_REAL_CC unset")
	}
}

func TestRunAsWrapper_LogsAndExecs(t *testing.T) {
	tmp := t.TempDir()
	logFile := filepath.Join(tmp, "build.log")
	origArgs := os.Args
	defer func() { os.Args = origArgs }()
	os.Args = []string{"cc", "--help-fake", "other"}

	trueBin := lookPathOrSkip(t, "true")
	t.Setenv("WASMIFY_LOG_FILE", logFile)
	t.Setenv("WASMIFY_REAL_CC", trueBin) // succeeds immediately

	if err := RunAsWrapper(); err != nil {
		t.Fatalf("RunAsWrapper() error: %v", err)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("log file not written: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 log line, got %d: %q", len(lines), string(data))
	}
	var entry LogEntry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("invalid log JSON: %v", err)
	}
	if entry.Tool != "cc" {
		t.Errorf("Tool = %q, want cc", entry.Tool)
	}
	if entry.Executable != trueBin {
		t.Errorf("Executable = %q, want %q", entry.Executable, trueBin)
	}
	wantArgs := []string{"--help-fake", "other"}
	if len(entry.Args) != 2 || entry.Args[0] != wantArgs[0] || entry.Args[1] != wantArgs[1] {
		t.Errorf("Args = %v, want %v", entry.Args, wantArgs)
	}
	if entry.Timestamp == "" {
		t.Error("Timestamp is empty")
	}
}

func envToMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, e := range env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			m[parts[0]] = parts[1]
		}
	}
	return m
}
