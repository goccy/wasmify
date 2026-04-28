package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/wasmify/internal/arch"
	"github.com/goccy/wasmify/internal/state"
	"github.com/goccy/wasmify/internal/tools"
)

// TestCmdEnsureTools_FromOutputDir verifies that ensure-tools reads arch.json
// from --output-dir, detects already-installed tools, and runs install
// recipes for missing ones. The test fakes an install by writing a marker
// file instead of touching the system.
func TestCmdEnsureTools_FromOutputDir(t *testing.T) {
	isolatedXDG(t) // also chdirs into a fresh tempdir → that is our outDir now.
	project := gitInitRepo(t)
	outDir, _ := os.Getwd()

	osID := tools.CurrentOS()
	if osID == "" {
		t.Skip("unsupported OS for ensure-tools")
	}

	// wasmify.json marks outDir as the project's config root; without it
	// loadProjectFromConfig refuses to proceed (go.mod-style discovery).
	if err := state.Save(outDir, state.New(project)); err != nil {
		t.Fatal(err)
	}

	marker := filepath.Join(t.TempDir(), "installed")
	a := &arch.Arch{
		Version: "1.0",
		Project: arch.Project{Name: "t", RootDir: ".", Language: "c++"},
		BuildSystem: arch.BuildSystem{
			Type:  "cmake",
			Files: []string{"CMakeLists.txt"},
		},
		Targets:  []arch.Target{{Name: "t", Type: "library"}},
		Commands: arch.BuildCommands{Build: "echo build"},
		Tools: []arch.Tool{
			{Name: "sh"}, // already installed → skipped
			{
				Name:    "fake-tool",
				Install: map[string]arch.ToolInstallCmd{string(osID): {Commands: []string{"touch " + marker}}},
			},
		},
		AnalyzedAt: "2026-04-19T00:00:00Z",
	}
	if err := arch.Save(outDir, a); err != nil {
		t.Fatalf("arch.Save: %v", err)
	}

	_, err := captureStderr(t, func() error {
		return cmdEnsureTools([]string{"--skip-wasi-sdk"})
	})
	if err != nil {
		t.Fatalf("cmdEnsureTools: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("fake-tool install did not run: %v", err)
	}
}

func TestCmdEnsureTools_MissingArch(t *testing.T) {
	isolatedXDG(t)
	// No wasmify.json in cwd → loadProjectFromConfig rejects.
	if err := cmdEnsureTools([]string{"--skip-wasi-sdk"}); err == nil {
		t.Fatal("expected error when wasmify.json is missing")
	}
}

func TestCmdEnsureTools_Usage(t *testing.T) {
	isolatedXDG(t)
	// cmdEnsureTools no longer has a usage-error path; with no config it
	// just errors on missing wasmify.json. Still a failure, which is fine.
	if err := cmdEnsureTools(nil); err == nil {
		t.Fatal("expected error without wasmify.json")
	}
}
