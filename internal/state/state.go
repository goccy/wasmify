// Package state defines the wasmify.json schema. Every persistent piece
// of decisional configuration that gets committed to a wasmify-generated
// project lives here: upstream pin, project metadata, build-system
// description, target list, dependency / tool tables, build commands,
// user selection, bridge generation knobs, declarative skip rules, and
// per-phase progress tracking.
//
// Wasmify writes a single committed file (wasmify.json). Earlier versions
// of the tool split this across arch.json + bridge-config.json + a thin
// wasmify.json; the unified shape lives here so consumers only have to
// commit one file.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SchemaVersion is the current wasmify.json schema version. Bump in lock-
// step with breaking changes to the on-disk shape.
const SchemaVersion = "1"

// State is the on-disk wasmify.json shape. Each section is independently
// optional so partially-initialised projects (mid-init, mid-analyze)
// round-trip cleanly through Load/Save.
type State struct {
	Version     string            `json:"version"`
	Upstream    UpstreamInfo      `json:"upstream,omitempty"`
	Project     Project           `json:"project,omitempty"`
	BuildSystem BuildSystem       `json:"build_system,omitempty"`
	Targets     []Target          `json:"targets,omitempty"`
	Deps        []Dependency      `json:"dependencies,omitempty"`
	Tools       []Tool            `json:"required_tools,omitempty"`
	Commands    BuildCommands     `json:"build_commands,omitempty"`
	Selection   *Selection        `json:"user_selection,omitempty"`
	Bridge      *BridgeConfig     `json:"bridge,omitempty"`
	Skip        *SkipConfig       `json:"skip,omitempty"`
	Phases      map[string]*Phase `json:"phases,omitempty"`
	AnalyzedAt  string            `json:"analyzed_at,omitempty"`
}

// UpstreamInfo records the upstream project location and last known commit.
type UpstreamInfo struct {
	Path   string `json:"path"`
	Commit string `json:"commit,omitempty"`
	Branch string `json:"branch,omitempty"`
}

// Project holds the human-decisional metadata about the upstream codebase.
type Project struct {
	Name     string `json:"name"`
	RootDir  string `json:"root_dir"`
	Language string `json:"language"`
	Standard string `json:"language_standard,omitempty"`
}

// BuildSystem describes how the upstream natively builds.
type BuildSystem struct {
	Type    string   `json:"type"`
	Version string   `json:"version,omitempty"`
	Files   []string `json:"files,omitempty"`
}

// Target is one buildable artefact discovered in the upstream project.
type Target struct {
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	BuildTarget   string   `json:"build_target,omitempty"`
	SourceDirs    []string `json:"source_dirs,omitempty"`
	PublicHeaders []string `json:"public_headers,omitempty"`
	Description   string   `json:"description,omitempty"`
}

// Dependency is an external library or tool the upstream depends on.
type Dependency struct {
	Name     string `json:"name"`
	Type     string `json:"type,omitempty"`
	Required bool   `json:"required,omitempty"`
}

// Tool is a build-time tool requirement (e.g. cmake, bazel) with
// per-OS install recipes consumed by `wasmify ensure-tools`.
type Tool struct {
	Name      string                    `json:"name"`
	Installed bool                      `json:"installed"`
	Path      string                    `json:"path,omitempty"`
	Version   string                    `json:"version,omitempty"`
	DetectCmd string                    `json:"detect_cmd,omitempty"`
	Install   map[string]ToolInstallCmd `json:"install,omitempty"`
}

// ToolInstallCmd is the per-OS install recipe. Keys in Tool.Install are
// "darwin" and "debian" (Debian/Ubuntu family Linux).
type ToolInstallCmd struct {
	Commands []string `json:"commands"`
}

// BuildCommands records the configure / build invocations the upstream
// expects.
type BuildCommands struct {
	Configure *string `json:"configure"`
	Build     string  `json:"build"`
}

// Selection records the user's choice of which target(s) to expose.
type Selection struct {
	// TargetNames is the canonical list of build targets selected by the
	// user. Multiple targets are merged into a single wasm and a single
	// proto schema (see cmdWasmBuild + cmdGenProto), so two libraries
	// that the user wants exposed via the same Go module (e.g. googlesql
	// analyzer + sql_formatter) can be selected together.
	TargetNames []string `json:"target_names,omitempty"`

	// TargetName is the legacy single-target field. Older configs written
	// before the multi-target switch still set this; UnmarshalJSON
	// normalises it into TargetNames so the rest of the pipeline only
	// has to consult Names().
	TargetName string `json:"target_name,omitempty"`

	BuildType string `json:"build_type"`
}

// Names returns the selected build-target names. Reading code should
// prefer this helper over poking at the struct fields directly so the
// legacy `target_name` and the new `target_names` array stay
// interchangeable.
func (s *Selection) Names() []string {
	if s == nil {
		return nil
	}
	if len(s.TargetNames) > 0 {
		return s.TargetNames
	}
	if s.TargetName != "" {
		return []string{s.TargetName}
	}
	return nil
}

// UnmarshalJSON folds the legacy `target_name` form into TargetNames so
// downstream code only has to read TargetNames.
func (s *Selection) UnmarshalJSON(data []byte) error {
	type raw Selection
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	*s = Selection(r)
	if len(s.TargetNames) == 0 && s.TargetName != "" {
		s.TargetNames = []string{s.TargetName}
	}
	if s.TargetName == "" && len(s.TargetNames) == 1 {
		s.TargetName = s.TargetNames[0]
	}
	return nil
}

// MarshalJSON emits the canonical TargetNames form. Existing integration
// tests assert on `target_name` for single-target setups, so we keep
// emitting it when there's exactly one target.
func (s Selection) MarshalJSON() ([]byte, error) {
	type out struct {
		TargetNames []string `json:"target_names,omitempty"`
		TargetName  string   `json:"target_name,omitempty"`
		BuildType   string   `json:"build_type"`
	}
	o := out{BuildType: s.BuildType}
	switch len(s.TargetNames) {
	case 0:
		if s.TargetName != "" {
			o.TargetName = s.TargetName
		}
	case 1:
		o.TargetName = s.TargetNames[0]
	default:
		o.TargetNames = s.TargetNames
	}
	return json.Marshal(o)
}

// BridgeConfig holds project-specific configuration for bridge
// generation. Library-specific handling (which external types to allow,
// which represent errors, which collection prefixes are map-like, etc.)
// is injected via this struct so the generator core stays library-
// agnostic.
//
// Previously serialised as bridge-config.json; now lives under the
// `bridge` key of wasmify.json.
type BridgeConfig struct {
	// ExternalTypes lists fully-qualified type names from external
	// libraries (not in the project's api-spec) that should be allowed
	// in function signatures. The bridge uses `auto` to capture these
	// and falls back to handle or TODO serialization.
	// Example: ["absl::Status", "absl::StatusOr", "absl::string_view"]
	ExternalTypes []string `json:"ExternalTypes,omitempty"`

	// ErrorTypes maps fully-qualified type names to their error-checking
	// pattern. When a function returns one of these types, the bridge
	// emits the specified C++ snippet to check for errors and write them
	// to proto field 15. The snippet uses {result} as a placeholder for
	// the result variable name.
	ErrorTypes map[string]string `json:"ErrorTypes,omitempty"`

	// SkipClasses lists fully-qualified class names to exclude from
	// bridge generation due to project-specific constraints.
	SkipClasses []string `json:"SkipClasses,omitempty"`

	// SkipHeaders lists header file paths to exclude from bridge
	// includes.
	SkipHeaders []string `json:"SkipHeaders,omitempty"`

	// GoPackage overrides the `option go_package = "..."` line written to
	// the generated .proto. Defaults to
	// "github.com/goccy/wasmify/gen/<pkg>" when unset.
	GoPackage string `json:"GoPackage,omitempty"`

	// ExportFunctions lists fully-qualified function names to export.
	// When set, only these functions and their transitive type
	// dependencies are included in the proto and bridge. When empty,
	// all APIs are exported.
	ExportFunctions []string `json:"ExportFunctions,omitempty"`

	// ExportEnumPrefixes lists fully-qualified enum name prefixes to
	// keep unconditionally.
	ExportEnumPrefixes []string `json:"ExportEnumPrefixes,omitempty"`

	// ExportDependentLibraries controls whether dependent library types
	// are exposed in the generated bridge API. Set to false to hide a
	// library's classes/methods.
	ExportDependentLibraries map[string]bool `json:"ExportDependentLibraries,omitempty"`

	// SkipStaticMethods lists method names that should not be treated
	// as static factory methods even if they match the static-factory
	// heuristic.
	SkipStaticMethods []string `json:"SkipStaticMethods,omitempty"`

	// ValueViewTypes lists qual-type prefixes of non-owning view types
	// (e.g. absl::Span) whose by-value / by-const-ref occurrences in
	// method parameters should be materialised as std::vector<Elem> in
	// the bridge.
	ValueViewTypes []string `json:"ValueViewTypes,omitempty"`

	// ErrorOnlyReturnTypes lists fully-qualified type names that should
	// be serialised on the wire as "void with optional error field".
	ErrorOnlyReturnTypes []string `json:"ErrorOnlyReturnTypes,omitempty"`

	// ErrorReconstruct describes how a trampoline override recreates an
	// error-typed return value from a wire error field. Keyed by fully-
	// qualified type name.
	ErrorReconstruct map[string]ErrorReturnSpec `json:"ErrorReconstruct,omitempty"`

	// UnsupportedStringTypes lists substrings that, when present in a
	// string-like qual_type, mark the type as not bridgeable.
	UnsupportedStringTypes []string `json:"UnsupportedStringTypes,omitempty"`

	// ExtraStringTypes lists additional fully-qualified names that
	// should be treated as string-like for proto mapping.
	ExtraStringTypes []string `json:"ExtraStringTypes,omitempty"`

	// MapLikeTypePrefixes lists template prefixes (without the opening
	// `<`) treated as proto `map<K, V>`.
	MapLikeTypePrefixes []string `json:"MapLikeTypePrefixes,omitempty"`

	// SetLikeTypePrefixes is the same, for set-like types.
	SetLikeTypePrefixes []string `json:"SetLikeTypePrefixes,omitempty"`
}

// ErrorReturnSpec describes how to reconstruct an error-typed return
// from a wire error message. See BridgeConfig.ErrorReconstruct.
type ErrorReturnSpec struct {
	OkExpr    string `json:"OkExpr"`    // e.g. "absl::OkStatus()"
	ErrorExpr string `json:"ErrorExpr"` // e.g. "absl::InternalError({err_msg})"
}

// SkipConfig captures declarative skip decisions consumed by
// `wasmify wasm-build`. Used in place of interactive y/N prompts so a
// non-interactive run (e.g. CI) can complete deterministically.
type SkipConfig struct {
	// Files lists source files that the wasm compile step should skip
	// without prompting. Each entry pairs the path (matched against the
	// step's Args) with a human-readable reason captured in the wasm-
	// build log for traceability.
	Files []SkipFile `json:"files,omitempty"`

	// DeployStubHeaders lists header paths the wasm build should auto-
	// deploy as empty stubs when the compiler reports them missing,
	// without prompting.
	DeployStubHeaders []string `json:"deploy_stub_headers,omitempty"`
}

// SkipFile is one declarative skip rule for a wasm compile step.
type SkipFile struct {
	Path   string `json:"path"`
	Reason string `json:"reason,omitempty"`
}

// Phase records the completion state of a single wasmify phase.
type Phase struct {
	CompletedAt    string   `json:"completed_at"`
	UpstreamCommit string   `json:"upstream_commit,omitempty"`
	OutputFiles    []string `json:"output_files,omitempty"`
}

const stateFile = "wasmify.json"

// Filename returns the on-disk filename for the wasmify state file.
func Filename() string { return stateFile }

// Load reads wasmify.json from the given directory.
// Returns nil, nil if the file does not exist.
func Load(dir string) (*State, error) {
	path := filepath.Join(dir, stateFile)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", stateFile, err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", stateFile, err)
	}
	return &s, nil
}

// Save writes wasmify.json to the given directory.
func Save(dir string, s *State) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}
	if s.Version == "" {
		s.Version = SchemaVersion
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal %s: %w", stateFile, err)
	}
	path := filepath.Join(dir, stateFile)
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// Update loads wasmify.json (or creates a fresh State if absent), passes
// it to mutate, then saves the result. Use this whenever a subcommand
// touches one section of state without overwriting the others — the
// merge happens for you so independent fields (Upstream, Phases, Bridge,
// Skip, etc.) are preserved across calls.
func Update(dir string, mutate func(*State) error) error {
	s, err := Load(dir)
	if err != nil {
		return err
	}
	if s == nil {
		s = &State{Version: SchemaVersion}
	}
	if err := mutate(s); err != nil {
		return err
	}
	return Save(dir, s)
}

// New creates a new State with the given upstream project path.
func New(projectPath string) *State {
	return &State{
		Version: SchemaVersion,
		Upstream: UpstreamInfo{
			Path: projectPath,
		},
		Phases: make(map[string]*Phase),
	}
}

// CompletePhase marks a phase as completed with the current timestamp
// and the given upstream commit hash.
func (s *State) CompletePhase(phaseName, upstreamCommit string, outputFiles []string) {
	if s.Phases == nil {
		s.Phases = make(map[string]*Phase)
	}
	s.Phases[phaseName] = &Phase{
		CompletedAt:    time.Now().UTC().Format(time.RFC3339),
		UpstreamCommit: upstreamCommit,
		OutputFiles:    outputFiles,
	}
}

// UpdateUpstream sets the commit and branch on the upstream info.
func (s *State) UpdateUpstream(commit, branch string) {
	s.Upstream.Commit = commit
	s.Upstream.Branch = branch
}

// UpstreamAbs returns the absolute upstream project path. When
// Upstream.Path is relative, it is resolved against configDir (the
// directory containing wasmify.json).
func (s *State) UpstreamAbs(configDir string) string {
	p := s.Upstream.Path
	if p == "" {
		return configDir
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(configDir, p))
}

// FindConfig walks up from startDir looking for wasmify.json. Returns
// the directory holding the file and the loaded State.
func FindConfig(startDir string) (configDir string, s *State, err error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", nil, err
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, stateFile)); statErr == nil {
			loaded, err := Load(dir)
			if err != nil {
				return "", nil, err
			}
			return dir, loaded, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil, fmt.Errorf("%s not found in %s or any parent; run 'wasmify init' first", stateFile, startDir)
		}
		dir = parent
	}
}

// HasArchData reports whether the state contains analyzer-output
// sections. Project.Name is the minimum signal — fresh `wasmify init`
// leaves it empty, while every populated arch save sets it.
func (s *State) HasArchData() bool {
	if s == nil {
		return false
	}
	return s.Project.Name != ""
}

// JSONSchema returns the JSON Schema for wasmify.json as a string.
// Embedded into the agent's system prompt for save-arch validation.
func JSONSchema() string {
	return `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "required": ["version"],
  "properties": {
    "version": { "type": "string", "const": "1" },
    "upstream": {
      "type": "object",
      "properties": {
        "path":   { "type": "string" },
        "commit": { "type": "string" },
        "branch": { "type": "string" }
      }
    },
    "project": {
      "type": "object",
      "required": ["name", "root_dir", "language"],
      "properties": {
        "name":     { "type": "string" },
        "root_dir": { "type": "string", "description": "Project root relative to the directory holding wasmify.json. Absolute paths are rejected — wasmify.json is committed to git and must be portable." },
        "language": { "type": "string", "enum": ["c", "c++", "mixed"] },
        "language_standard": { "type": "string" }
      }
    },
    "build_system": {
      "type": "object",
      "required": ["type"],
      "properties": {
        "type":    { "type": "string", "enum": ["cmake", "make", "autotools", "bazel", "meson", "other"] },
        "version": { "type": "string" },
        "files":   { "type": "array", "items": { "type": "string" } }
      }
    },
    "targets": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["name", "type"],
        "properties": {
          "name":         { "type": "string" },
          "type":         { "type": "string", "enum": ["library", "executable"] },
          "build_target": { "type": "string" },
          "source_dirs":  { "type": "array", "items": { "type": "string" } },
          "public_headers": { "type": "array", "items": { "type": "string" } },
          "description":  { "type": "string" }
        }
      }
    },
    "dependencies": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["name"],
        "properties": {
          "name":     { "type": "string" },
          "type":     { "type": "string", "enum": ["library", "tool", "system"] },
          "required": { "type": "boolean" }
        }
      }
    },
    "required_tools": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["name", "installed"],
        "properties": {
          "name":       { "type": "string" },
          "installed":  { "type": "boolean" },
          "path":       { "type": "string" },
          "version":    { "type": "string" },
          "detect_cmd": { "type": "string" },
          "install": {
            "type": "object",
            "additionalProperties": {
              "type": "object",
              "required": ["commands"],
              "properties": {
                "commands": { "type": "array", "items": { "type": "string" } }
              }
            }
          }
        }
      }
    },
    "build_commands": {
      "type": "object",
      "required": ["build"],
      "properties": {
        "configure": { "type": ["string", "null"] },
        "build":     { "type": "string" }
      }
    },
    "user_selection": {
      "type": "object",
      "properties": {
        "target_names": { "type": "array", "items": { "type": "string" } },
        "target_name":  { "type": "string" },
        "build_type":   { "type": "string", "enum": ["library", "executable"] }
      }
    },
    "bridge": {
      "type": "object",
      "description": "Project-specific bridge generation knobs. See internal/state/state.go::BridgeConfig for the full field list."
    },
    "skip": {
      "type": "object",
      "properties": {
        "files": {
          "type": "array",
          "items": {
            "type": "object",
            "required": ["path"],
            "properties": {
              "path":   { "type": "string" },
              "reason": { "type": "string" }
            }
          }
        },
        "deploy_stub_headers": { "type": "array", "items": { "type": "string" } }
      }
    },
    "phases": {
      "type": "object",
      "additionalProperties": {
        "type": "object",
        "required": ["completed_at"],
        "properties": {
          "completed_at":    { "type": "string", "format": "date-time" },
          "upstream_commit": { "type": "string" },
          "output_files":    { "type": "array", "items": { "type": "string" } }
        }
      }
    },
    "analyzed_at": { "type": "string", "format": "date-time" }
  }
}`
}
