// Package arch is a compatibility shim around internal/state. The
// `arch.json` file used to hold the project's analyzer-output sections;
// it now lives under the same keys inside the unified `wasmify.json`,
// managed by the state package.
//
// Existing callers that construct an `arch.Arch{...}` value or call
// `arch.Load(dir)` / `arch.Save(dir, a)` keep working: types are aliased
// to their state-package counterparts and Load/Save round-trip the
// arch-shaped fields through the state file. The remaining sections
// (Upstream, Phases, Bridge, Skip) are preserved across saves so this
// shim does not clobber state written by other subcommands.
package arch

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/wasmify/internal/state"
)

// Type aliases re-export the underlying state types so existing call
// sites don't need to know whether their data lives in arch.json or
// wasmify.json.
type (
	Project        = state.Project
	BuildSystem    = state.BuildSystem
	Target         = state.Target
	Dependency     = state.Dependency
	Tool           = state.Tool
	ToolInstallCmd = state.ToolInstallCmd
	BuildCommands  = state.BuildCommands
	Selection      = state.Selection
)

// Arch is the analyzer-output portion of wasmify.json. Construction-style
// callers (`&arch.Arch{Project: ..., Targets: ..., Selection: ...}`)
// keep working; on Save the fields are merged into the existing
// wasmify.json so unrelated sections survive.
type Arch struct {
	Version     string         `json:"version"`
	Project     Project        `json:"project"`
	BuildSystem BuildSystem    `json:"build_system"`
	Targets     []Target       `json:"targets"`
	Deps        []Dependency   `json:"dependencies,omitempty"`
	Tools       []Tool         `json:"required_tools,omitempty"`
	Commands    BuildCommands  `json:"build_commands"`
	Selection   *Selection     `json:"user_selection,omitempty"`
	AnalyzedAt  string         `json:"analyzed_at"`
}

// Load reads the arch-shaped sections of wasmify.json from dir.
// Returns nil, nil when no wasmify.json exists or it has not received
// arch data yet (project metadata empty).
func Load(dir string) (*Arch, error) {
	s, err := state.Load(dir)
	if err != nil {
		return nil, rewriteFilenameErr(err)
	}
	if s == nil || !s.HasArchData() {
		return nil, nil
	}
	return fromState(s), nil
}

// Save merges the arch fields into the existing wasmify.json (creating
// it if absent) and persists. Other sections (Upstream, Phases, Bridge,
// Skip) are preserved.
func Save(dir string, a *Arch) error {
	if a == nil {
		return fmt.Errorf("arch.Save: nil Arch")
	}
	// Reject saving when dir is occupied by a file that blocks creation
	// of wasmify.json. Detect the legacy "wasmify.json is a directory"
	// fixture used by tests so we still surface the error.
	if info, err := os.Stat(filepath.Join(dir, state.Filename())); err == nil && info.IsDir() {
		return fmt.Errorf("cannot write %s: path is a directory", state.Filename())
	}
	return state.Update(dir, func(s *state.State) error {
		mergeInto(s, a)
		return nil
	})
}

// JSONSchema returns the JSON schema embedded in the agent's system
// prompt. Delegates to the state package so the schema stays in sync
// with the on-disk shape.
func JSONSchema() string { return state.JSONSchema() }

func fromState(s *state.State) *Arch {
	return &Arch{
		Version:     s.Version,
		Project:     s.Project,
		BuildSystem: s.BuildSystem,
		Targets:     s.Targets,
		Deps:        s.Deps,
		Tools:       s.Tools,
		Commands:    s.Commands,
		Selection:   s.Selection,
		AnalyzedAt:  s.AnalyzedAt,
	}
}

func mergeInto(s *state.State, a *Arch) {
	if a.Version != "" {
		s.Version = a.Version
	}
	s.Project = a.Project
	s.BuildSystem = a.BuildSystem
	s.Targets = a.Targets
	s.Deps = a.Deps
	s.Tools = a.Tools
	s.Commands = a.Commands
	s.Selection = a.Selection
	if a.AnalyzedAt != "" {
		s.AnalyzedAt = a.AnalyzedAt
	}
}

// rewriteFilenameErr keeps existing test assertions ("invalid JSON")
// working — state.Load reports `wasmify.json` in its messages, which is
// the right name post-refactor.
func rewriteFilenameErr(err error) error { return err }
