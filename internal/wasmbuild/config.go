package wasmbuild

import (
	"os"
	"strings"
)

// DefaultStackSize is the default wasm stack size in bytes (32 MB).
// Large C++ projects with deeply-nested template instantiations may need
// a bigger stack than the wasi-sdk default (64 KB) to avoid stack overflow.
const DefaultStackSize = 33554432

// DefaultMaxMemoryPages is the maximum a THREADS build declares for its shared
// memory, in 64 KiB pages (1024 pages = 64 MiB). The WebAssembly threads
// proposal requires a shared memory to declare a bounded maximum, and
// wasm-ld --shared-memory will not link without --max-memory, so a threads
// build must carry one. It is a property of the wasm binary, enforced by
// whatever runs it — a conventional VM caps memory.grow at this value.
//
// The wasm2go backend is the exception: it transpiles the wasm to Go and
// enforces the EMBEDDING HOST's own runtime cap instead of the baked value, so
// under wasm2go this bounds nothing the host does not re-choose (it only seeds
// a default the host may override per instance). Raise it (wasmify.json
// wasm_build.max_memory_pages) for VM backends whose guest heap needs more, or
// to lift the default ceiling a wasm2go embedder starts from.
const DefaultMaxMemoryPages = 1024

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
	HostThreads         bool     // Opt-in: build for wasm32-wasi-threads — -pthread + shared memory + atomics, define WASMIFY_HOST_THREADS (host-provided threads: wasm2go runs each guest thread on a goroutine)
	MaxMemoryPages      int      // Threads only: the shared memory's declared maximum in the wasm binary, in 64 KiB pages (default DefaultMaxMemoryPages). Mandatory for a threads build (the WebAssembly threads proposal requires a bounded shared memory). A conventional VM enforces it as the memory.grow cap; the wasm2go backend instead honors the embedding host's own runtime cap, which overrides this baked value.
	KeepSymbols         bool     // Opt-in (wasmify.json wasm_build.keep_symbols): skip -Wl,--strip-all so the final wasm keeps its name section
	NoPosixCompat       bool     // Skip the POSIX-compat stub headers (wasi-native projects whose code the bare sysroot already backs); mirrors WASMIFY_NO_POSIX_COMPAT
	NoEmscriptenDefine  bool     // Skip the implicit -D__EMSCRIPTEN__ (wasi-native projects with real #ifdef __EMSCRIPTEN__ branches); mirrors WASMIFY_NO_EMSCRIPTEN_DEFINE
	ExtraLDFlags        []string // Extra linker flags appended to every (non-skipped) link step, e.g. -Wl,--wrap=connect; mirrors WASMIFY_EXTRA_LDFLAGS
	BridgeExtraIncludes []string // Extra -I directories for compiling the generated/custom bridge sources; mirrors WASMIFY_BRIDGE_EXTRA_INCLUDES
	ExtraCXXFlags       []string // Extra compile flags for the bridge sources, appended last so they override wasmify's defaults (wasmify.json wasm_build.extra_cxxflags); mirrors WASMIFY_EXTRA_CXXFLAGS
	PrebuiltArchives    []string // Absolute paths to already-compiled wasm32-wasi .a files linked after the build tree's own archives (wasmify.json wasm_build.prebuilt_archives); mirrors WASMIFY_PREBUILT_ARCHIVES
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
	if envSet("WASMIFY_HOST_THREADS") {
		c.HostThreads = true
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
	// Space-separated bridge compile flags. Appended for the same reason, and
	// because clang resolves a repeated option to its last occurrence, an env
	// override also beats the same flag set in wasmify.json.
	c.ExtraCXXFlags = append(c.ExtraCXXFlags, strings.Fields(os.Getenv("WASMIFY_EXTRA_CXXFLAGS"))...)
	// Colon-separated include directories; empty segments are dropped.
	for _, dir := range strings.Split(os.Getenv("WASMIFY_BRIDGE_EXTRA_INCLUDES"), ":") {
		if dir = strings.TrimSpace(dir); dir != "" {
			c.BridgeExtraIncludes = append(c.BridgeExtraIncludes, dir)
		}
	}
	// Colon-separated archive paths; empty segments are dropped.
	for _, ar := range strings.Split(os.Getenv("WASMIFY_PREBUILT_ARCHIVES"), ":") {
		if ar = strings.TrimSpace(ar); ar != "" {
			c.PrebuiltArchives = append(c.PrebuiltArchives, ar)
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
