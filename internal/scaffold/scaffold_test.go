package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateMakefile(t *testing.T) {
	dir := t.TempDir()
	if err := GenerateMakefile(dir, "/path/to/project"); err != nil {
		t.Fatalf("GenerateMakefile: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "Makefile"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "/path/to/project") {
		t.Error("Makefile should contain project path")
	}
	if !strings.Contains(content, "update") {
		t.Error("Makefile should contain update target")
	}
	if !strings.Contains(content, "wasm-build") {
		t.Error("Makefile should contain wasm-build target")
	}

	// Should not overwrite
	if err := GenerateMakefile(dir, "/other/path"); err != nil {
		t.Fatalf("GenerateMakefile (second call): %v", err)
	}
	data2, _ := os.ReadFile(filepath.Join(dir, "Makefile"))
	if strings.Contains(string(data2), "/other/path") {
		t.Error("Makefile should not be overwritten")
	}
}

// TestGenerateMakefile_RelativeSubmodule checks that when the upstream is
// inside the output dir (submodule layout), the Makefile records a relative
// PROJECT path so the same file works on CI without overrides.
func TestGenerateMakefile_RelativeSubmodule(t *testing.T) {
	out := t.TempDir()
	upstream := filepath.Join(out, "googlesql")
	if err := os.Mkdir(upstream, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := GenerateMakefile(out, upstream); err != nil {
		t.Fatalf("GenerateMakefile: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(out, "Makefile"))
	content := string(data)
	if !strings.Contains(content, "PROJECT ?= ./googlesql") {
		t.Errorf("expected relative PROJECT=./googlesql, got:\n%s", content)
	}
	if strings.Contains(content, upstream) {
		t.Error("Makefile should not embed absolute path when upstream is under output dir")
	}
}

// TestGenerateMakefile_AbsoluteOutsideOutput keeps the absolute path when the
// upstream lives elsewhere on disk.
func TestGenerateMakefile_AbsoluteOutsideOutput(t *testing.T) {
	out := t.TempDir()
	outside := t.TempDir() // sibling, not a child
	if err := GenerateMakefile(out, outside); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(out, "Makefile"))
	if !strings.Contains(string(data), outside) {
		t.Errorf("expected absolute path %q, got:\n%s", outside, string(data))
	}
}

func TestGenerateReleaseWorkflow(t *testing.T) {
	dir := t.TempDir()
	if err := GenerateReleaseWorkflow(dir); err != nil {
		t.Fatalf("GenerateReleaseWorkflow: %v", err)
	}

	path := filepath.Join(dir, ".github", "workflows", "release.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "Build WASM") {
		t.Error("Workflow should contain 'Build WASM'")
	}
	if !strings.Contains(content, "attest-build-provenance") {
		t.Error("Workflow should contain attestation step")
	}
	if !strings.Contains(content, "actions/cache@v4") {
		t.Error("Workflow should configure actions/cache to speed up rebuilds")
	}
	if !strings.Contains(content, ".wasmify") {
		t.Errorf("Workflow should cache .wasmify/, got:\n%s", content)
	}
	if !strings.Contains(content, "~/.config/wasmify/bin/wasi-sdk") {
		t.Error("Workflow should cache the shared wasi-sdk toolchain")
	}
	if !strings.Contains(content, "hashFiles('wasmify.json'") {
		t.Error("wasmify cache should be keyed on wasmify.json / build.json / proto")
	}
}

func TestGenerateGitignore(t *testing.T) {
	t.Run("creates when missing", func(t *testing.T) {
		dir := t.TempDir()
		if err := GenerateGitignore(dir); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
		if !strings.Contains(string(data), ".wasmify/") {
			t.Errorf(".gitignore should contain .wasmify/, got:\n%s", data)
		}
	})
	t.Run("appends when pattern absent", func(t *testing.T) {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules\n"), 0o644)
		if err := GenerateGitignore(dir); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
		s := string(data)
		if !strings.Contains(s, "node_modules") || !strings.Contains(s, ".wasmify/") {
			t.Errorf(".gitignore should retain existing entries and add .wasmify/, got:\n%s", s)
		}
	})
	t.Run("idempotent", func(t *testing.T) {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".wasmify/\n"), 0o644)
		if err := GenerateGitignore(dir); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
		if strings.Count(string(data), ".wasmify/") != 1 {
			t.Errorf(".gitignore should not duplicate existing entry, got:\n%s", data)
		}
	})
}

func TestGenerate(t *testing.T) {
	dir := t.TempDir()
	if err := Generate(dir, "/path/to/project"); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Makefile")); err != nil {
		t.Error("Makefile should exist")
	}
	if _, err := os.Stat(filepath.Join(dir, ".github", "workflows", "release.yml")); err != nil {
		t.Error("release.yml should exist")
	}
}

func TestGenerateReleaseWorkflowNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	// Seed an existing release.yml with sentinel content
	wfDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(wfDir, "release.yml")
	sentinel := "SENTINEL-DO-NOT-OVERWRITE"
	if err := os.WriteFile(path, []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateReleaseWorkflow(dir); err != nil {
		t.Fatalf("GenerateReleaseWorkflow: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), sentinel) {
		t.Error("existing release.yml should not be overwritten")
	}
}

func TestGenerateReleaseWorkflowMkdirFailure(t *testing.T) {
	// Use a file as output dir's .github path -> MkdirAll should fail
	base := t.TempDir()
	filePath := filepath.Join(base, ".github")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateReleaseWorkflow(base); err == nil {
		t.Error("expected error when .github is a file")
	}
}

func TestGenerateErrorPropagation(t *testing.T) {
	// A file at outputDir prevents MkdirAll for .github, but Makefile still
	// writes successfully (since outputDir is a directory). So we instead
	// make outputDir be a file so Makefile write fails.
	base := t.TempDir()
	outDir := filepath.Join(base, "notadir")
	if err := os.WriteFile(outDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Generate(outDir, "/p"); err == nil {
		t.Error("expected error when outputDir is not a directory")
	}
}

func TestGenerateReleaseWorkflowPropagation(t *testing.T) {
	// outputDir is a valid dir but .github is a file -> Makefile succeeds,
	// then GenerateReleaseWorkflow fails.
	base := t.TempDir()
	filePath := filepath.Join(base, ".github")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Generate(base, "/p"); err == nil {
		t.Error("expected Generate to fail when .github is a file")
	}
}
