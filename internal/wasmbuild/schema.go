package wasmbuild

import "github.com/goccy/wasmify/internal/buildjson"

// WasmBuildStep represents a single build step transformed for wasm32-wasi.
type WasmBuildStep struct {
	ID         int                `json:"id"`
	OriginalID int                `json:"original_id"`
	Type       buildjson.StepType `json:"type"`
	Executable string             `json:"executable"`
	Args       []string           `json:"args"`
	WorkDir    string             `json:"work_dir"`
	OutputFile string             `json:"output_file"`
	InputFiles []string           `json:"input_files,omitempty"`
	Skipped    bool               `json:"skipped,omitempty"`
	SkipReason string             `json:"skip_reason,omitempty"`
	Cached     bool               `json:"cached,omitempty"`
}

// WasmBuildJSON is the top-level structure for wasm-build.json.
type WasmBuildJSON struct {
	Version   string          `json:"version"`
	Config    WasmConfig      `json:"config"`
	Steps     []WasmBuildStep `json:"steps"`
	Summary   BuildSummary    `json:"summary"`
	CreatedAt string          `json:"created_at"`
}

// BuildSummary provides counts of each step type.
type BuildSummary struct {
	TotalSteps   int `json:"total_steps"`
	CompileSteps int `json:"compile_steps"`
	ArchiveSteps int `json:"archive_steps"`
	LinkSteps    int `json:"link_steps"`
	SkippedSteps int `json:"skipped_steps"`
	CachedSteps  int `json:"cached_steps"`
}
