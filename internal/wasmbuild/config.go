package wasmbuild

import (
	"os"
	"strings"
)

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
	NoPosixCompat       bool     // Skip the POSIX-compat stub headers (wasi-native projects whose code the bare sysroot already backs); mirrors WASMIFY_NO_POSIX_COMPAT
	NoEmscriptenDefine  bool     // Skip the implicit -D__EMSCRIPTEN__ (wasi-native projects with real #ifdef __EMSCRIPTEN__ branches); mirrors WASMIFY_NO_EMSCRIPTEN_DEFINE
	ExtraLDFlags        []string // Extra linker flags appended to every (non-skipped) link step, e.g. -Wl,--wrap=connect; mirrors WASMIFY_EXTRA_LDFLAGS
	BridgeExtraIncludes []string // Extra -I directories for compiling the generated/custom bridge sources; mirrors WASMIFY_BRIDGE_EXTRA_INCLUDES
}

// envSet reports whether an environment variable carries a value. Presence —
// not a particular "1"/"true" spelling — is what enables the option, so any
// non-empty value (after trimming surrounding spaces) counts.
func envSet(key string) bool {
	return strings.TrimSpace(os.Getenv(key)) != ""
}

// ApplyEnvOverrides folds the recognised WASMIFY_* environment variables into
// cfg. Environment variables are only ever an alternative way to set a build
// option: every build code path branches on the WasmConfig fields and never
// reads the environment directly, so an option set through wasmify.json and the
// same option set through the environment drive identical behaviour. Call this
// once, right after cfg has been populated from wasmify.json / state, so the
// resolved options flow everywhere downstream.
func (c *WasmConfig) ApplyEnvOverrides() {
	if envSet("WASMIFY_HOST_SOCKETS") {
		c.HostSockets = true
	}
	if envSet("WASMIFY_HOST_SUBPROCESS") {
		c.HostSubprocess = true
	}
	if envSet("WASMIFY_NO_POSIX_COMPAT") {
		c.NoPosixCompat = true
	}
	if envSet("WASMIFY_NO_EMSCRIPTEN_DEFINE") {
		c.NoEmscriptenDefine = true
	}
	// Space-separated linker flags, appended so an env override adds to (rather
	// than replaces) anything already configured.
	c.ExtraLDFlags = append(c.ExtraLDFlags, strings.Fields(os.Getenv("WASMIFY_EXTRA_LDFLAGS"))...)
	// Colon-separated include directories; empty segments are dropped.
	for _, dir := range strings.Split(os.Getenv("WASMIFY_BRIDGE_EXTRA_INCLUDES"), ":") {
		if dir = strings.TrimSpace(dir); dir != "" {
			c.BridgeExtraIncludes = append(c.BridgeExtraIncludes, dir)
		}
	}
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
