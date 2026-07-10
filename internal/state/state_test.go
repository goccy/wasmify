package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNewState(t *testing.T) {
	s := New("/path/to/project")
	if s.Version != SchemaVersion {
		t.Errorf("Version = %q, want %q", s.Version, SchemaVersion)
	}
	if s.Upstream.Path != "/path/to/project" {
		t.Errorf("Upstream.Path = %q, want %q", s.Upstream.Path, "/path/to/project")
	}
	if s.Phases == nil {
		t.Error("Phases should be initialized")
	}
}

func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()

	s := New("/path/to/project")
	s.UpdateUpstream("abc123", "main")
	s.CompletePhase("gen-proto", "abc123", []string{"proto/api.proto"})

	if err := Save(dir, s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(filepath.Join(dir, "wasmify.json")); err != nil {
		t.Fatalf("wasmify.json not created: %v", err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded == nil {
		t.Fatal("Load returned nil")
	}
	if loaded.Version != SchemaVersion {
		t.Errorf("Version = %q, want %q", loaded.Version, SchemaVersion)
	}
	if loaded.Upstream.Commit != "abc123" {
		t.Errorf("Upstream.Commit = %q, want %q", loaded.Upstream.Commit, "abc123")
	}
	if loaded.Upstream.Branch != "main" {
		t.Errorf("Upstream.Branch = %q, want %q", loaded.Upstream.Branch, "main")
	}
	phase := loaded.Phases["gen-proto"]
	if phase == nil {
		t.Fatal("gen-proto phase not found")
	}
	if phase.UpstreamCommit != "abc123" {
		t.Errorf("Phase.UpstreamCommit = %q, want %q", phase.UpstreamCommit, "abc123")
	}
	if len(phase.OutputFiles) != 1 || phase.OutputFiles[0] != "proto/api.proto" {
		t.Errorf("Phase.OutputFiles = %v, want [proto/api.proto]", phase.OutputFiles)
	}
}

// TestBridgeStackSizeRoundTrip verifies the optional wasm linker stack-size
// override survives Save/Load (it feeds cfg.StackSize at link time).
func TestBridgeStackSizeRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := New("/path/to/project")
	s.Bridge = &BridgeConfig{StackSize: 8 * 1024 * 1024}
	if err := Save(dir, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Bridge == nil {
		t.Fatal("Bridge config lost on round-trip")
	}
	if loaded.Bridge.StackSize != 8*1024*1024 {
		t.Errorf("Bridge.StackSize = %d, want %d", loaded.Bridge.StackSize, 8*1024*1024)
	}

	// Unset StackSize must be omitted (omitempty) and load back as 0 so the
	// link falls through to DefaultStackSize.
	s2 := New("/p")
	s2.Bridge = &BridgeConfig{HostSockets: true}
	if err := Save(dir, s2); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded2, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded2.Bridge.StackSize != 0 {
		t.Errorf("unset Bridge.StackSize = %d, want 0", loaded2.Bridge.StackSize)
	}
}

func TestLoadNonExistent(t *testing.T) {
	dir := t.TempDir()
	s, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s != nil {
		t.Error("Load of non-existent file should return nil")
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wasmify.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(dir)
	if err == nil {
		t.Error("Load should fail on invalid JSON")
	}
	if s != nil {
		t.Error("Load should return nil state on parse error")
	}
}

func TestLoadReadError(t *testing.T) {
	// Create a directory where the file should be -> read will fail
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "wasmify.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil {
		t.Error("Load should fail when path is a directory")
	}
}

func TestSaveMkdirCreatesIntermediate(t *testing.T) {
	// Save should create intermediate directories.
	dir := filepath.Join(t.TempDir(), "nested", "sub", "dir")
	s := New("/p")
	if err := Save(dir, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "wasmify.json")); err != nil {
		t.Errorf("expected wasmify.json to be created: %v", err)
	}
}

func TestSaveMkdirFailure(t *testing.T) {
	// Use a file as a directory -> MkdirAll should fail.
	base := t.TempDir()
	filePath := filepath.Join(base, "notadir")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filePath, "child")
	s := New("/p")
	if err := Save(target, s); err == nil {
		t.Error("Save should fail when parent is a file")
	}
}

func TestCompletePhaseInitializesNil(t *testing.T) {
	s := &State{Version: SchemaVersion}
	// Phases is nil; CompletePhase must initialize it
	s.CompletePhase("analyze", "deadbeef", nil)
	if s.Phases == nil {
		t.Fatal("Phases not initialized")
	}
	p := s.Phases["analyze"]
	if p == nil {
		t.Fatal("phase not stored")
	}
	if p.UpstreamCommit != "deadbeef" {
		t.Errorf("UpstreamCommit = %q, want deadbeef", p.UpstreamCommit)
	}
	if p.CompletedAt == "" {
		t.Error("CompletedAt should be set")
	}
	if len(p.OutputFiles) != 0 {
		t.Errorf("expected empty OutputFiles, got %v", p.OutputFiles)
	}
}

func TestCompletePhaseOverwrite(t *testing.T) {
	s := New("/p")
	s.CompletePhase("build", "v1", []string{"a"})
	first := s.Phases["build"].CompletedAt
	// Overwrite
	s.CompletePhase("build", "v2", []string{"b", "c"})
	p := s.Phases["build"]
	if p.UpstreamCommit != "v2" {
		t.Errorf("want v2, got %q", p.UpstreamCommit)
	}
	if len(p.OutputFiles) != 2 {
		t.Errorf("want 2 files, got %v", p.OutputFiles)
	}
	// CompletedAt should be updated (or at least not broken)
	if p.CompletedAt == "" {
		t.Error("CompletedAt should not be empty")
	}
	_ = first
}

func TestUpdateUpstream(t *testing.T) {
	s := New("/path")
	s.UpdateUpstream("commit1", "dev")
	if s.Upstream.Commit != "commit1" {
		t.Errorf("Commit = %q, want commit1", s.Upstream.Commit)
	}
	if s.Upstream.Branch != "dev" {
		t.Errorf("Branch = %q, want dev", s.Upstream.Branch)
	}
	// Path should be preserved
	if s.Upstream.Path != "/path" {
		t.Errorf("Path lost: %q", s.Upstream.Path)
	}
}

func TestBridgeConfigHostSocketsJSON(t *testing.T) {
	// Opt-in flag round-trips through the wasmify.json bridge config.
	var s State
	if err := json.Unmarshal([]byte(`{"bridge":{"HostSockets":true}}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.Bridge == nil || !s.Bridge.HostSockets {
		t.Fatalf("HostSockets did not parse as true: %+v", s.Bridge)
	}
	// Absent → false (omitempty default), keeping the wasm portable by default.
	var s2 State
	if err := json.Unmarshal([]byte(`{"bridge":{}}`), &s2); err != nil {
		t.Fatal(err)
	}
	if s2.Bridge == nil || s2.Bridge.HostSockets {
		t.Fatalf("HostSockets should default to false, got %+v", s2.Bridge)
	}
}

func TestBridgeConfigHostSubprocessJSON(t *testing.T) {
	// Opt-in flag round-trips through the wasmify.json bridge config.
	var s State
	if err := json.Unmarshal([]byte(`{"bridge":{"HostSubprocess":true}}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.Bridge == nil || !s.Bridge.HostSubprocess {
		t.Fatalf("HostSubprocess did not parse as true: %+v", s.Bridge)
	}
	// Absent → false (omitempty default), keeping the wasm portable and
	// sandboxed by default.
	var s2 State
	if err := json.Unmarshal([]byte(`{"bridge":{}}`), &s2); err != nil {
		t.Fatal(err)
	}
	if s2.Bridge == nil || s2.Bridge.HostSubprocess {
		t.Fatalf("HostSubprocess should default to false, got %+v", s2.Bridge)
	}
}

// The wasm_build knobs a prebuilt-archive project depends on must survive a
// Save/Load round trip: they are the only channel through which such a project
// can reach the link and the bridge compiles, and a silently dropped field would
// surface as an inscrutable linker error rather than a config error.
func TestWasmBuildConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := &WasmBuildConfig{
		KeepSymbols:         true,
		PrebuiltArchives:    []string{"deps/libengine.a", "deps/libengine_support.a"},
		ExtraCXXFlags:       []string{"-std=gnu++20", "-fno-rtti"},
		ExtraLDFlags:        []string{"-Wl,--stack-first"},
		BridgeExtraIncludes: []string{"deps/include"},
	}
	if err := Save(dir, &State{Version: SchemaVersion, WasmBuild: want}); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.WasmBuild == nil {
		t.Fatal("wasm_build section lost")
	}
	if !reflect.DeepEqual(got.WasmBuild, want) {
		t.Errorf("wasm_build = %+v, want %+v", got.WasmBuild, want)
	}
}
