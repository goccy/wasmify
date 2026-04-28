package cache

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNew(t *testing.T) {
	c := New()
	if c == nil {
		t.Fatal("New() returned nil")
	}
	if c.Phase != PhaseAnalyze {
		t.Errorf("initial phase = %q, want %q", c.Phase, PhaseAnalyze)
	}
	if len(c.CompletedPhases) != 0 {
		t.Errorf("expected no completed phases, got %d", len(c.CompletedPhases))
	}
	if c.UpdatedAt == "" {
		t.Error("expected UpdatedAt to be set")
	}
}

func TestIsCompleted(t *testing.T) {
	c := New()
	if c.IsCompleted(PhaseAnalyze) {
		t.Error("new cache should not report any phase as completed")
	}
	c.CompletedPhases = []Phase{PhaseAnalyze, PhaseClassify}
	if !c.IsCompleted(PhaseAnalyze) {
		t.Error("IsCompleted(PhaseAnalyze) = false, want true")
	}
	if !c.IsCompleted(PhaseClassify) {
		t.Error("IsCompleted(PhaseClassify) = false, want true")
	}
	if c.IsCompleted(PhaseBuild) {
		t.Error("IsCompleted(PhaseBuild) = true, want false")
	}
}

func TestCompletePhase_AdvancesPhase(t *testing.T) {
	c := New()
	c.CompletePhase(PhaseAnalyze)
	if !c.IsCompleted(PhaseAnalyze) {
		t.Error("expected PhaseAnalyze to be completed")
	}
	if c.Phase != PhaseClassify {
		t.Errorf("Phase = %q, want %q", c.Phase, PhaseClassify)
	}

	c.CompletePhase(PhaseClassify)
	if c.Phase != PhasePrepare {
		t.Errorf("Phase = %q, want %q", c.Phase, PhasePrepare)
	}
}

func TestCompletePhase_LastPhaseDoesNotAdvance(t *testing.T) {
	c := New()
	c.Phase = PhaseWasmBuild
	c.CompletePhase(PhaseWasmBuild)
	if !c.IsCompleted(PhaseWasmBuild) {
		t.Error("expected PhaseWasmBuild to be completed")
	}
	// Still points at PhaseWasmBuild because there's nothing after it.
	if c.Phase != PhaseWasmBuild {
		t.Errorf("Phase = %q, want %q", c.Phase, PhaseWasmBuild)
	}
}

func TestCompletePhase_Idempotent(t *testing.T) {
	c := New()
	c.CompletePhase(PhaseAnalyze)
	c.CompletePhase(PhaseAnalyze)
	count := 0
	for _, p := range c.CompletedPhases {
		if p == PhaseAnalyze {
			count++
		}
	}
	if count != 1 {
		t.Errorf("PhaseAnalyze appears %d times in CompletedPhases, want 1", count)
	}
}

func TestCompletePhase_UnknownPhase(t *testing.T) {
	c := New()
	// Completing a phase that isn't part of phaseOrder records it but does not advance.
	c.CompletePhase(Phase("ghost"))
	if !c.IsCompleted(Phase("ghost")) {
		t.Error("expected ghost phase to be recorded as completed")
	}
	if c.Phase != PhaseAnalyze {
		t.Errorf("Phase advanced unexpectedly: %q", c.Phase)
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	c := New()
	c.CompletePhase(PhaseAnalyze)
	c.CompletePhase(PhaseClassify)

	if err := c.Save(dir); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// File must exist
	if _, err := os.Stat(filepath.Join(dir, "cache.json")); err != nil {
		t.Fatalf("cache.json not created: %v", err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if loaded == nil {
		t.Fatal("Load() returned nil after Save")
	}
	if loaded.Phase != c.Phase {
		t.Errorf("loaded Phase = %q, want %q", loaded.Phase, c.Phase)
	}
	if len(loaded.CompletedPhases) != len(c.CompletedPhases) {
		t.Errorf("loaded %d completed phases, want %d", len(loaded.CompletedPhases), len(c.CompletedPhases))
	}
	if loaded.UpdatedAt == "" {
		t.Error("loaded cache has empty UpdatedAt")
	}
}

func TestLoad_NonExistent(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if c != nil {
		t.Errorf("Load() returned %+v for non-existent file, want nil", c)
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cache.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestSave_UpdatesTimestamp(t *testing.T) {
	dir := t.TempDir()
	c := &Cache{
		Phase:           PhaseAnalyze,
		CompletedPhases: []Phase{},
		UpdatedAt:       "old",
	}
	if err := c.Save(dir); err != nil {
		t.Fatal(err)
	}
	if c.UpdatedAt == "old" {
		t.Error("Save did not update UpdatedAt")
	}
}
