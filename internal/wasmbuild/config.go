package wasmbuild

// DefaultStackSize is the default wasm stack size in bytes (32 MB).
// Large C++ projects with deeply-nested template instantiations may need
// a bigger stack than the wasi-sdk default (64 KB) to avoid stack overflow.
const DefaultStackSize = 33554432

// WasmConfig holds configuration for wasm build transformation and execution.
type WasmConfig struct {
	WasiSDKPath    string // Path to wasi-sdk installation
	Target         string // Target triple (default: "wasm32-wasi")
	BuildDir       string // Output directory for wasm build artifacts
	NoEntry        bool   // Library mode (no _start entry point)
	AllowUndefined bool   // Allow undefined symbols (for host-provided imports)
	DryRun         bool   // Only generate wasm-build.json without executing
	NoCache        bool   // Disable build cache
	PosixCompatDir string // Path to POSIX compatibility headers (injected via -isystem)
	StackSize      int    // Wasm stack size in bytes (default: DefaultStackSize)
}

// DefaultConfig returns a WasmConfig with sensible defaults.
func DefaultConfig() WasmConfig {
	return WasmConfig{
		Target:         "wasm32-wasip1",
		NoEntry:        true,
		AllowUndefined: true,
		StackSize:      DefaultStackSize,
	}
}
