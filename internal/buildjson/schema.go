package buildjson

type BuildJSON struct {
	Version    string      `json:"version"`
	Project    ProjectInfo `json:"project"`
	Steps      []BuildStep `json:"steps"`
	CapturedAt string      `json:"captured_at"`
}

type ProjectInfo struct {
	Name        string `json:"name"`
	RootDir     string `json:"root_dir"`
	BuildType   string `json:"build_type"`
	BuildSystem string `json:"build_system"`
}

type BuildStep struct {
	ID         int        `json:"id"`
	Type       StepType   `json:"type"`
	Compiler   string     `json:"compiler,omitempty"`
	Executable string     `json:"executable"`
	Args       []string   `json:"args"`
	WorkDir    string     `json:"work_dir"`
	InputFiles []string   `json:"input_files,omitempty"`
	OutputFile string     `json:"output_file,omitempty"`
	Flags      BuildFlags `json:"flags,omitempty"`
	Language   string     `json:"language,omitempty"`

	// WasmSkip marks this compile step to be skipped during `wasmify
	// wasm-build`. Set manually (or interactively via the "skip this step?"
	// prompt) when a source file fails to compile for wasm32-wasi despite
	// building fine natively — typical for POSIX/OS-specific helpers
	// (symbolize, stacktrace, raw_logging, tzdata libc paths, ...).
	//
	// The field is persisted in build.json and committed to git so CI,
	// which has no TTY to answer the prompt, inherits developer decisions.
	// Archive and link steps that reference a skipped object drop it
	// automatically.
	WasmSkip       bool   `json:"wasm_skip,omitempty"`
	WasmSkipReason string `json:"wasm_skip_reason,omitempty"`
}

type BuildFlags struct {
	IncludePaths []string `json:"include_paths,omitempty"`
	Defines      []string `json:"defines,omitempty"`
	LinkLibs     []string `json:"link_libs,omitempty"`
	LibPaths     []string `json:"lib_paths,omitempty"`
	Standard     string   `json:"standard,omitempty"`
	Optimization string   `json:"optimization,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
	OtherFlags   []string `json:"other_flags,omitempty"`
}

type StepType string

const (
	StepCompile   StepType = "compile"
	StepLink      StepType = "link"
	StepArchive   StepType = "archive"
	StepConfigure StepType = "configure"
	StepOther     StepType = "other"
)
