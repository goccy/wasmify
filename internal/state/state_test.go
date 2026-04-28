package state

import (
	"os"
	"path/filepath"
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
