package arch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/wasmify/internal/state"
)

func TestSaveLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := "cmake .."
	original := &Arch{
		Project: Project{
			Name:     "demo",
			RootDir:  "./demo",
			Language: "c++",
			Standard: "c++20",
		},
		BuildSystem: BuildSystem{
			Type:  "cmake",
			Files: []string{"CMakeLists.txt"},
		},
		Targets: []Target{
			{Name: "libdemo", Type: "library", SourceDirs: []string{"src"}},
		},
		Deps: []Dependency{
			{Name: "zlib", Type: "library", Required: true},
		},
		Tools: []Tool{
			{Name: "cmake", Installed: true, Path: "/usr/bin/cmake", Version: "3.22"},
		},
		Commands: BuildCommands{
			Configure: &cfg,
			Build:     "cmake --build build",
		},
		Selection: &Selection{
			TargetName: "libdemo",
			BuildType:  "library",
		},
		AnalyzedAt: "2025-01-01T00:00:00Z",
	}

	if err := Save(dir, original); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Arch data is now persisted in wasmify.json (the unified state file);
	// arch.json no longer exists.
	if _, err := os.Stat(filepath.Join(dir, state.Filename())); err != nil {
		t.Fatalf("wasmify.json not created: %v", err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if loaded == nil {
		t.Fatal("Load() returned nil after Save")
	}
	if loaded.Project.Name != original.Project.Name {
		t.Errorf("Project.Name = %q, want %q", loaded.Project.Name, original.Project.Name)
	}
	if loaded.BuildSystem.Type != original.BuildSystem.Type {
		t.Errorf("BuildSystem.Type = %q, want %q", loaded.BuildSystem.Type, original.BuildSystem.Type)
	}
	if len(loaded.Targets) != 1 || loaded.Targets[0].Name != "libdemo" {
		t.Errorf("Targets mismatch: %+v", loaded.Targets)
	}
	if loaded.Selection == nil || loaded.Selection.TargetName != "libdemo" {
		t.Errorf("Selection mismatch: %+v", loaded.Selection)
	}
	if loaded.Commands.Configure == nil || *loaded.Commands.Configure != cfg {
		t.Errorf("Commands.Configure mismatch: %+v", loaded.Commands.Configure)
	}
}

func TestSaveLoad_PreservesOtherSections(t *testing.T) {
	dir := t.TempDir()
	// Pre-seed wasmify.json with upstream + phases via the state package.
	// arch.Save must merge its fields without clobbering these.
	pre := state.New("./demo")
	pre.UpdateUpstream("deadbeef", "main")
	pre.CompletePhase("analyze", "deadbeef", []string{"wasmify.json"})
	if err := state.Save(dir, pre); err != nil {
		t.Fatalf("state.Save: %v", err)
	}

	a := &Arch{
		Project:     Project{Name: "demo", RootDir: "./demo", Language: "c++"},
		BuildSystem: BuildSystem{Type: "cmake", Files: []string{"CMakeLists.txt"}},
		Targets:     []Target{{Name: "libdemo", Type: "library"}},
		Commands:    BuildCommands{Build: "cmake --build build"},
	}
	if err := Save(dir, a); err != nil {
		t.Fatalf("arch.Save: %v", err)
	}

	post, err := state.Load(dir)
	if err != nil {
		t.Fatalf("state.Load: %v", err)
	}
	if post.Upstream.Commit != "deadbeef" {
		t.Errorf("Upstream.Commit clobbered: got %q want deadbeef", post.Upstream.Commit)
	}
	if post.Phases["analyze"] == nil {
		t.Error("Phases[analyze] clobbered")
	}
	if post.Project.Name != "demo" {
		t.Errorf("Project.Name = %q, want demo", post.Project.Name)
	}
}

func TestLoad_NonExistent(t *testing.T) {
	dir := t.TempDir()
	a, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if a != nil {
		t.Errorf("Load() returned %+v for non-existent file, want nil", a)
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, state.Filename()), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestSave_WriteError(t *testing.T) {
	// Save to a path where wasmify.json is occupied by a directory.
	dir := t.TempDir()
	statePath := filepath.Join(dir, state.Filename())
	if err := os.MkdirAll(statePath, 0o755); err != nil {
		t.Fatal(err)
	}
	a := &Arch{Project: Project{Name: "x", RootDir: "./x", Language: "c"}}
	if err := Save(dir, a); err == nil {
		t.Error("expected Save error when wasmify.json is a directory")
	}
}

func TestLoad_ReadError(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, state.Filename())
	if err := os.MkdirAll(statePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Error("expected Load error when wasmify.json is a directory")
	}
}

func TestJSONSchema_IsValidJSON(t *testing.T) {
	schema := JSONSchema()
	if schema == "" {
		t.Fatal("JSONSchema() returned empty string")
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(schema), &out); err != nil {
		t.Fatalf("JSONSchema() is not valid JSON: %v", err)
	}
	if out["$schema"] == nil {
		t.Error("schema missing $schema property")
	}
	if out["type"] != "object" {
		t.Errorf("schema type = %v, want object", out["type"])
	}
	required, ok := out["required"].([]any)
	if !ok {
		t.Fatal("required is not an array")
	}
	if len(required) == 0 {
		t.Error("required fields list is empty")
	}
}
