package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigFilePath_WithXDG(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	path, err := ConfigFilePath()
	if err != nil {
		t.Fatalf("ConfigFilePath() error: %v", err)
	}
	want := filepath.Join(tmp, "wasmify", "config.json")
	if path != want {
		t.Errorf("ConfigFilePath() = %q, want %q", path, want)
	}
}

func TestConfigFilePath_WithoutXDG(t *testing.T) {
	// Force XDG_CONFIG_HOME to empty so we exercise the home dir fallback.
	t.Setenv("XDG_CONFIG_HOME", "")

	path, err := ConfigFilePath()
	if err != nil {
		t.Fatalf("ConfigFilePath() error: %v", err)
	}
	if !strings.HasSuffix(path, filepath.Join(".config", "wasmify", "config.json")) {
		t.Errorf("ConfigFilePath() = %q, want suffix .config/wasmify/config.json", path)
	}
}

func TestLoad_NonExistent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load() returned nil config for non-existent file")
	}
	if len(cfg.Projects) != 0 {
		t.Errorf("expected empty config, got %d projects", len(cfg.Projects))
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	original := &Config{
		Projects: []ProjectEntry{
			{Path: "/path/to/project1", Hash: "abc123"},
			{Path: "/path/to/project2", Hash: "def456"},
		},
	}
	if err := original.Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Verify file exists
	path, _ := ConfigFilePath()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config file not created: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(loaded.Projects) != len(original.Projects) {
		t.Fatalf("expected %d projects, got %d", len(original.Projects), len(loaded.Projects))
	}
	for i, p := range loaded.Projects {
		if p.Path != original.Projects[i].Path || p.Hash != original.Projects[i].Hash {
			t.Errorf("project[%d] = %+v, want %+v", i, p, original.Projects[i])
		}
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	path, _ := ConfigFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestProjectDir_NewProject(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	projectPath := filepath.Join(tmp, "some", "project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{}
	dir, err := cfg.ProjectDir(projectPath)
	if err != nil {
		t.Fatalf("ProjectDir() error: %v", err)
	}
	if dir == "" {
		t.Fatal("ProjectDir() returned empty path")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("project directory not created: %v", err)
	}
	// Should have been registered in config
	if len(cfg.Projects) != 1 {
		t.Fatalf("expected 1 registered project, got %d", len(cfg.Projects))
	}
	absPath, _ := filepath.Abs(projectPath)
	if cfg.Projects[0].Path != absPath {
		t.Errorf("registered path = %q, want %q", cfg.Projects[0].Path, absPath)
	}
	if cfg.Projects[0].Hash == "" {
		t.Error("registered hash is empty")
	}

	// Verify config was persisted
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Projects) != 1 {
		t.Errorf("persisted config has %d projects, want 1", len(loaded.Projects))
	}
}

func TestProjectDir_ExistingProject(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	projectPath := filepath.Join(tmp, "existing")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{}
	dir1, err := cfg.ProjectDir(projectPath)
	if err != nil {
		t.Fatal(err)
	}

	// Call again — should return the same dir and not duplicate the entry.
	dir2, err := cfg.ProjectDir(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if dir1 != dir2 {
		t.Errorf("second call returned different dir: %q vs %q", dir1, dir2)
	}
	if len(cfg.Projects) != 1 {
		t.Errorf("expected 1 project, got %d", len(cfg.Projects))
	}
}

func TestProjectDir_DifferentPathsProduceDifferentHashes(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	p1 := filepath.Join(tmp, "proj1")
	p2 := filepath.Join(tmp, "proj2")
	if err := os.MkdirAll(p1, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p2, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{}
	dir1, err := cfg.ProjectDir(p1)
	if err != nil {
		t.Fatal(err)
	}
	dir2, err := cfg.ProjectDir(p2)
	if err != nil {
		t.Fatal(err)
	}
	if dir1 == dir2 {
		t.Error("different project paths should produce different dirs")
	}
	if len(cfg.Projects) != 2 {
		t.Errorf("expected 2 projects, got %d", len(cfg.Projects))
	}
}

func TestConfigFilePath_HomeDirError(t *testing.T) {
	// When XDG_CONFIG_HOME is empty AND HOME is empty,
	// os.UserHomeDir returns an error on Unix. Force that here.
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	// Also clear the fallback user.Current-resolved var on macOS.
	_, err := ConfigFilePath()
	if err == nil {
		// Some platforms can still resolve home (e.g., via getpwuid).
		// In that case we simply can't trigger the error path, so skip.
		t.Skip("cannot trigger home-dir error on this platform")
	}
}

func TestLoad_ReadError(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	// Create config.json as a directory, causing os.ReadFile to fail with
	// something other than IsNotExist.
	path, _ := ConfigFilePath()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Error("expected read error when config.json is a directory")
	}
}

func TestSave_NestedDirCreation(t *testing.T) {
	tmp := t.TempDir()
	// Point XDG_CONFIG_HOME at a nested, not-yet-existing directory to
	// exercise the MkdirAll branch inside Save.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "a", "b", "c"))
	cfg := &Config{Projects: []ProjectEntry{{Path: "/p", Hash: "h"}}}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	p, _ := ConfigFilePath()
	if _, err := os.Stat(p); err != nil {
		t.Errorf("config file not created: %v", err)
	}
}

func TestProjectDir_RelativePathResolved(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	sub := filepath.Join(tmp, "relproj")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(tmp)

	cfg := &Config{}
	if _, err := cfg.ProjectDir("relproj"); err != nil {
		t.Fatalf("ProjectDir(relative) error: %v", err)
	}
	if len(cfg.Projects) != 1 {
		t.Fatalf("expected 1 project registered, got %d", len(cfg.Projects))
	}
	if !filepath.IsAbs(cfg.Projects[0].Path) {
		t.Errorf("registered path %q is not absolute", cfg.Projects[0].Path)
	}
}

func TestProjectDir_MkdirFails(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	projectPath := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatal(err)
	}

	// Pre-create a file where the project data dir should be, forcing
	// os.MkdirAll inside ProjectDir to fail.
	absPath, _ := filepath.Abs(projectPath)
	hash := hashPath(absPath)
	projectsParent := filepath.Join(tmp, "wasmify", "projects")
	if err := os.MkdirAll(projectsParent, 0o755); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(projectsParent, hash)
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{}
	if _, err := cfg.ProjectDir(projectPath); err == nil {
		t.Error("expected ProjectDir error when data dir path is occupied by a file")
	}
}

func TestProjectDir_ExistingMkdirFails(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	projectPath := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatal(err)
	}
	absPath, _ := filepath.Abs(projectPath)
	hash := hashPath(absPath)

	// Register the project in the cfg manually, then block the data dir.
	cfg := &Config{Projects: []ProjectEntry{{Path: absPath, Hash: hash}}}

	projectsParent := filepath.Join(tmp, "wasmify", "projects")
	if err := os.MkdirAll(projectsParent, 0o755); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(projectsParent, hash)
	if err := os.WriteFile(blocker, []byte("blocker"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := cfg.ProjectDir(projectPath); err == nil {
		t.Error("expected ProjectDir error on existing-entry MkdirAll failure")
	}
}

func TestHashPath_Deterministic(t *testing.T) {
	h1 := hashPath("/some/path")
	h2 := hashPath("/some/path")
	if h1 != h2 {
		t.Errorf("hashPath not deterministic: %q vs %q", h1, h2)
	}
	if len(h1) != 64 {
		t.Errorf("expected 64-char sha256 hex, got %d", len(h1))
	}
	if h1 == hashPath("/different/path") {
		t.Error("different paths should produce different hashes")
	}
}
