package wasmbuild

// DefaultStackSize is the default wasm stack size in bytes (32 MB).
// Large C++ projects with deeply-nested template instantiations may need
// a bigger stack than the wasi-sdk default (64 KB) to avoid stack overflow.
const DefaultStackSize = 33554432

// WasmConfig holds configuration for wasm build transformation and execution.
type WasmConfig struct {
	WasiSDKPath         string   // Path to wasi-sdk installation
	Target              string   // Target triple (default: "wasm32-wasi")
	BuildDir            string   // Output directory for wasm build artifacts
	NoEntry             bool     // Library mode (no _start entry point)
	AllowUndefined      bool     // Allow undefined symbols (for host-provided imports)
	DryRun              bool     // Only generate wasm-build.json without executing
	NoCache             bool     // Disable build cache
	PosixCompatDir      string   // Path to POSIX compatibility headers (injected via -isystem)
	HostIncludeDir      string   // Path to build-local host-capability stub headers (spawn.h/sys/wait.h), injected via -I on every compile when HostSubprocess is on
	ProjectRoot         string   // Absolute project root; added to the bridge compile -I so generated api_bridge.cc's project-relative includes (e.g. "embed/foo.h") resolve
	CustomBridgeSources []string // Absolute paths to the project's hand-written bridge implementation sources (declared in wasmify.json bridge.CustomBridgeSources); each is compiled and linked, with -I of its own dir so its sibling-relative includes resolve
	StackSize           int      // Wasm stack size in bytes (default: DefaultStackSize)
	HostSockets         bool     // Opt-in: define WASMIFY_HOST_SOCKETS for every wasm-build compile (host-provided outbound sockets)
	HostSubprocess      bool     // Opt-in: define WASMIFY_HOST_SUBPROCESS for every wasm-build compile + add HostIncludeDir to -I (host-provided process spawn)
	KeepSymbols         bool     // Opt-in (wasmify.json wasm_build.keep_symbols): skip -Wl,--strip-all so the final wasm keeps its name section
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
