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
	Version     string        `json:"version"`
	Upstream    UpstreamInfo  `json:"upstream,omitempty"`
	Project     Project       `json:"project,omitempty"`
	BuildSystem BuildSystem   `json:"build_system,omitempty"`
	Targets     []Target      `json:"targets,omitempty"`
	Deps        []Dependency  `json:"dependencies,omitempty"`
	Tools       []Tool        `json:"required_tools,omitempty"`
	Commands    BuildCommands `json:"build_commands,omitempty"`
	// Output configures where wasmify's user-facing artifacts are
	// written. Each per-artifact path is independently optional;
	// relative paths resolve against the directory holding wasmify.json.
	Output     *Output           `json:"output,omitempty"`
	Selection  *Selection        `json:"user_selection,omitempty"`
	Bridge     *BridgeConfig     `json:"bridge,omitempty"`
	WasmBuild  *WasmBuildConfig  `json:"wasm_build,omitempty"`
	Skip       *SkipConfig       `json:"skip,omitempty"`
	Phases     map[string]*Phase `json:"phases,omitempty"`
	AnalyzedAt string            `json:"analyzed_at,omitempty"`
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

// Target is one buildable artifact discovered in the upstream project.
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

// Output declares user-facing destination paths for wasmify's
// artifacts. Each field is independently optional. A relative path
// resolves against the directory holding wasmify.json; an absolute
// path is used unchanged. Without a path set for an artifact, the
// internal `.wasmify/` build-tree copy is the only output.
type Output struct {
	// Wasm is where `wasmify wasm-build` writes the final linked
	// (and optionally optimised) wasm artifact, in addition to the
	// internal build-tree copy.
	Wasm string `json:"wasm,omitempty"`
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
// WasmBuildConfig holds wasm-target build knobs that must be declared per
// project (as opposed to flags wasmify applies unconditionally because they
// are harmless when unused, e.g. -lsetjmp / -fno-strict-aliasing). It lives
// under the `wasm_build` key of wasmify.json.
type WasmBuildConfig struct {
	// KeepSymbols, when true, omits `-Wl,--strip-all` from the final link so
	// the wasm keeps its name section. The default (false) strips, producing a
	// smaller shipping wasm; enable this when a function-name ↔ symbol mapping
	// is needed for debugging or symbolication.
	KeepSymbols bool `json:"keep_symbols,omitempty"`

	// PrebuiltArchives lists static libraries that are NOT produced by
	// replaying the captured upstream build, and that the library link must
	// nevertheless pull from. Each entry is a path to a wasm32-wasi `.a`
	// (plain objects or LLVM bitcode); relative paths resolve against the
	// directory holding wasmify.json.
	//
	// This exists for upstreams that ship, or publish, an already-compiled
	// wasm static library instead of sources wasmify can compile — a project
	// whose native build is too heavy to reproduce, and which therefore
	// releases the wasm32-wasi archive as a build artifact. Such a project has
	// an empty build.json (nothing to replay), so the archive would otherwise
	// never reach the link.
	//
	// The archives are appended AFTER the ones discovered under the build
	// tree's lib/, so a symbol the replayed build defines wins over the same
	// symbol in a prebuilt archive.
	PrebuiltArchives []string `json:"prebuilt_archives,omitempty"`

	// ExtraCXXFlags are appended to the compile of every bridge source (the
	// generated api_bridge.cc, custom_bridge.cc, and each
	// bridge.CustomBridgeSources entry). They land after wasmify's own flags,
	// so a repeated option overrides wasmify's default — `-std=gnu++23`
	// overrides the built-in `-std=c++20`.
	//
	// A bridge that includes a prebuilt library's headers generally has to
	// match the flags that library was compiled with, because several of them
	// change ABI rather than just diagnostics: `-fno-rtti`, `-fno-exceptions`,
	// `-fno-sized-deallocation` and `-fno-aligned-new` all alter which
	// `operator new`/`operator delete` overloads are emitted and called.
	ExtraCXXFlags []string `json:"extra_cxxflags,omitempty"`

	// BridgeExtraIncludes are extra `-I` directories for the bridge compiles.
	// Relative paths resolve against the directory holding wasmify.json.
	//
	// The bridge otherwise only sees include paths harvested from the captured
	// upstream compile steps, so a prebuilt library's headers — which no
	// captured step ever referenced — are unreachable without this. Mirrors the
	// WASMIFY_BRIDGE_EXTRA_INCLUDES environment variable.
	BridgeExtraIncludes []string `json:"bridge_extra_includes,omitempty"`

	// ExtraLDFlags are appended to every link step, including the library link
	// that LinkLibrary performs. Use it for flags the captured build never
	// carried: `-Wl,--wrap=connect` to route libc calls to host shims,
	// `-Wl,--stack-first` for a library that assumes the C stack grows down
	// toward address zero, an extra `-l` for a sysroot emulation library.
	// Mirrors the WASMIFY_EXTRA_LDFLAGS environment variable.
	ExtraLDFlags []string `json:"extra_ldflags,omitempty"`
}

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

	// CustomBridgeSources lists the project's hand-written bridge
	// implementation source files (paths relative to the project root),
	// for projects whose exported API is a thin embedding layer rather
	// than the upstream library itself (e.g. a py.c defining
	// py_new/py_eval on top of libpython). wasm-build compiles and links
	// each one — with -I of its own directory so its sibling-relative
	// includes resolve — so the project never copies sources into the
	// generated bridge dir. Each is compiled as C++ (clang++ -x c++): a
	// custom bridge is the C++ counterpart of api_bridge.cc, and a project
	// may name it `.c` while it uses C++ constructs. Example: ["py.c"].
	CustomBridgeSources []string `json:"CustomBridgeSources,omitempty"`

	// IncludeExternalHeaders lists header files outside the project
	// root that the parser should still walk. Used when the project
	// exposes types from an external library (typically declared
	// also in `ExternalTypes`) and the user wants those classes'
	// methods bridged too — e.g. `google::protobuf::DescriptorPool`
	// from protobuf's own header so a Go consumer can construct
	// proto-driven schemas. Each entry is matched as a path
	// substring against the include-directory-resolved absolute
	// header path that landed in the build's .d files.
	IncludeExternalHeaders []string `json:"IncludeExternalHeaders,omitempty"`

	// IncludeExternalClasses lists fully-qualified class names that
	// the bridge generator must accept even when the class's source
	// file is outside the project root. Pair with
	// `IncludeExternalHeaders` to surface the parsed methods of
	// those classes.
	IncludeExternalClasses []string `json:"IncludeExternalClasses,omitempty"`

	// GoPackage overrides the `option go_package = "..."` line written to
	// the generated .proto. Defaults to
	// "github.com/goccy/wasmify/gen/<pkg>" when unset.
	GoPackage string `json:"GoPackage,omitempty"`

	// Wasm2GoImportPath overrides the Go import path embedded into
	// the wasm2go-transpiled output (chunks, alias.go,
	// //go:linkname directives, asm cross-chunk JMP targets) when
	// runtime=wasm2go is in effect. The disk layout on
	// `<bridge>/internal/wasm2go/...` is unchanged; only the
	// embedded import path that lets the bridge import the package
	// shifts. Integrators set this to a hyphen-free module path
	// (e.g. `github.com/goccy/googlesqlwasm2go`) to publish the
	// wasm2go output as a separate module and unlock the wasm2go
	// codegen's asm-only cross-chunk trampoline mode, which trades
	// one Go-body frame for an asm tail-JMP per cross-chunk hop —
	// the optimization is path-gated because plan 9 asm's operand
	// scanner only accepts identifier-rune-safe characters
	// (letters, digits, "_", U+00B7, U+2215) in cross-package
	// symbol references, so hyphens / plus / etc. in the bridge's
	// own module path break it. Unset (default) keeps the bridge's
	// `internal/wasm2go` subpath; the wasm2go codegen then falls
	// back to the Go-body wrapper-pair trampoline. Wired through to
	// protoc-gen-wasmify-go via the `wasm2go_import_path=<...>`
	// buf option.
	Wasm2GoImportPath string `json:"Wasm2GoImportPath,omitempty"`

	// HostSockets opts into host-provided outbound sockets. WASI preview1
	// has no socket()/connect()/getaddrinfo(), so by default the generated
	// wasm imports only standard wasi_snapshot_preview1 and is portable to
	// any wasi runtime. When this is true, the bridge compile is given
	// -DWASMIFY_HOST_SOCKETS, which activates the project's socket shim
	// (libc socket()/connect()/getaddrinfo() backed by sock_socket/
	// sock_connect/sock_getaddrinfo host imports). The resulting wasm then
	// requires a host that implements those imports (e.g. the wasm2go
	// runtime). Off by default to keep the wasm portable.
	HostSockets bool `json:"HostSockets,omitempty"`

	// HostSubprocess opts into host-provided process spawning. WASI preview1
	// cannot spawn processes; when this is true the bridge compile is given
	// -DWASMIFY_HOST_SUBPROCESS, activating the project's posix_spawn/waitpid
	// shim (backed by proc_spawn/proc_wait host imports). The resulting wasm
	// then requires a host that implements those imports and EXECUTES HOST
	// BINARIES, so it is off by default to keep the wasm portable and sandboxed.
	HostSubprocess bool `json:"HostSubprocess,omitempty"`

	// HostThreads opts into threads. The wasm is built for wasi-threads
	// (-pthread, shared memory, atomics), which makes it import
	// wasi_thread_spawn and export wasi_thread_start — and wasm2go runs each
	// guest thread on a GOROUTINE, so unlike sockets or subprocess this
	// capability needs no host implementation at all: a Go embedder gets real
	// concurrency for free. Guests that never spawn pay nothing.
	HostThreads bool `json:"HostThreads,omitempty"`

	// MaxMemoryPages declares the shared memory's maximum in the wasm binary, in
	// 64 KiB pages (threads only; the WebAssembly threads proposal requires a
	// shared memory to declare a bounded maximum, and wasm-ld --shared-memory
	// will not link without it). It is a property of the binary, enforced by
	// whatever runs the wasm — a conventional VM caps memory.grow here.
	//
	// The wasm2go backend is the exception. It transpiles the wasm to Go and
	// enforces the embedding host's own runtime cap instead: an embedder can set
	// a per-instance memory cap that overrides this baked maximum in either
	// direction. Under wasm2go this value only seeds a default the host may
	// replace; it bounds nothing the host re-chooses. (Reserving the ceiling up
	// front as virtual, lazily-resident address space is what lets a shared
	// memory's growth stay a single atomic store rather than a relocation that
	// would invalidate the pointers other agents hold.)
	//
	// Zero means wasmbuild.DefaultMaxMemoryPages (64 MiB).
	MaxMemoryPages int `json:"MaxMemoryPages,omitempty"`

	// StackSize overrides the wasm linker stack size (bytes) for the final
	// link, i.e. the `-Wl,-z,stack-size=` value. The wasi-sdk default (64 KB)
	// is too small for C++ with deeply-nested template instantiations, so
	// wasmify links with a generous default (DefaultStackSize, 32 MB). That is
	// far larger than some interpreters need; setting a smaller value here
	// shrinks the initial linear memory baked into the module (the stack region
	// precedes the data segment under --stack-first). Keep it well above the
	// guest runtime's real C-stack high-water mark plus its overflow-guard
	// reservation. 0 (unset) keeps DefaultStackSize.
	StackSize int `json:"StackSize,omitempty"`

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

	// CallbackClasses lists fully-qualified class names that the user
	// wants to subclass from Go even though the class is concrete in
	// the C++ sense (no unimplemented pure virtuals — `T t;`
	// compiles). Abstract classes are picked up automatically, so
	// they do NOT need to appear here; listing one is harmless but
	// redundant.
	//
	// Concrete classes need an explicit signal because C++ has no
	// language-level distinction between "concrete + virtuals as
	// customisation hooks" (e.g. TableValuedFunction.Resolve has a
	// default implementation that subclasses are expected to
	// override) and "concrete + virtuals as visitor dispatch" (every
	// AST/Resolved node has Accept inherited from an abstract base).
	// Auto-picking every concrete class with virtuals would balloon
	// the generated surface; the user names the small set that
	// genuinely needs subclassing instead. See
	// docs/callback-services.md for the full rationale.
	CallbackClasses []string `json:"CallbackClasses,omitempty"`

	// OwnershipTransferMethods lists C++ methods whose raw `T*`
	// parameter is consumed (ownership transferred to the receiver)
	// despite the C++ type signature being a borrowed pointer. This
	// is the explicit-opt-in escape hatch for APIs whose
	// implementation captures the raw pointer into a smart pointer
	// (e.g. `absl::WrapUnique`, `std::unique_ptr<T>(p)`,
	// `std::move`) inside the .cc body — information that is not
	// visible from the header-only api-spec the rest of the
	// generator works from.
	//
	// The generator does NOT attempt to detect this idiom from
	// method names or any other naming pattern: name-based logic
	// is indistinguishable from hardcoding library-specific
	// conventions, and detecting it from the .cc body would
	// require full C++ implementation parsing (out of scope).
	// Listing the methods explicitly is the price of honouring a
	// poorly-typed C++ API.
	//
	// Each entry is identified by the fully-qualified C++ method
	// name plus an optional Signature (list of parameter
	// qual_types) that picks a specific overload. When Signature
	// is empty, every overload of Method matches.
	//
	// Two C++ patterns are covered:
	//
	//  1. Unconditional ownership transfer. The matched overload
	//     has only handle parameters (and possibly value-typed
	//     params). Every handle param gets the
	//     `wasm_take_ownership` proto field option, which drives
	//     the Go-side `clearPtr()` emit so the wrapper's
	//     per-instance finalizer does not double-free memory the
	//     C++ destructor will also reclaim.
	//
	//  2. Runtime-conditional ownership transfer. The matched
	//     overload includes a `bool` parameter that the receiver
	//     consults at runtime to decide whether to adopt
	//     ownership (e.g. `AddColumn(const Column*, bool
	//     is_owned)`). The proto field for the handle gets the
	//     `wasm_take_ownership_when` extension carrying the
	//     bool's proto field name; the plugin emits a runtime
	//     guard `if <bool> { handle.clearPtr() }` after the
	//     invoke. The bool is identified by type, not by the
	//     parameter name.
	//
	// Pattern (2) requires a Signature to be specified — it is
	// the user's commitment that the bool in the matched overload
	// IS the ownership selector. Without a signature, a bool
	// param in the matched overload is treated as a regular value
	// param (no runtime gate emitted).
	OwnershipTransferMethods []OwnershipTransferEntry `json:"OwnershipTransferMethods,omitempty"`
}

// OwnershipTransferEntry identifies a C++ method whose raw `T*`
// parameter transfers ownership in violation of the C++ type
// system. See BridgeConfig.OwnershipTransferMethods.
type OwnershipTransferEntry struct {
	// Method is the fully-qualified C++ method name (e.g.
	// "googlesql::SimpleCatalog::AddOwnedTable").
	Method string `json:"method"`
	// Signature is the optional list of parameter qual_types
	// (e.g. ["const googlesql::Column *", "bool"]) used to
	// disambiguate overloads. Match is exact, position by
	// position. When Signature is empty, every overload of
	// Method matches.
	Signature []string `json:"signature,omitempty"`
	// TransferWhen, when set, marks this as runtime-conditional
	// ownership transfer (Pattern 2). The named parameter on the
	// matched overload is consulted at runtime; the handle param
	// is adopted (Go-side `clearPtr` fires) only when the named
	// parameter equals `Equals` at call time.
	//
	// Required when the matched overload includes a parameter that
	// the C++ implementation consults to decide ownership at
	// runtime — e.g. `SimpleTable::AddColumn(const Column*, bool
	// is_owned)` adopts iff `is_owned == true`. Without this
	// explicit selector, the entry is treated as Pattern 1
	// (unconditional transfer): every handle param is cleared
	// regardless of any other parameter values.
	//
	// Supported parameter primitive types are bool, integer
	// (signed / unsigned of any width), and string-like
	// (`std::string`, `absl::string_view`). For bool params the
	// generator collapses `Equals=true` into `if <param>` and
	// `Equals=false` into `if !<param>`; for int and string params
	// it emits an explicit `==` comparison.
	TransferWhen *TransferWhenSpec `json:"transfer_when,omitempty"`
}

// TransferWhenSpec describes the runtime selector for Pattern 2
// ownership transfer (see OwnershipTransferEntry.TransferWhen).
type TransferWhenSpec struct {
	// Param is the C++ parameter name on the matched overload
	// whose runtime value selects ownership transfer.
	Param string `json:"param"`
	// Equals is the value the parameter must take for ownership
	// to transfer. Type must match the parameter's C++ type:
	//   - bool param   → JSON bool   (`true` / `false`)
	//   - int param    → JSON number (any integer literal)
	//   - string param → JSON string
	// Loaded as `any` so the wasmify generator can validate the
	// type against the matched parameter and reject mismatches
	// at gen-proto time rather than letting them surface as
	// confusing runtime errors.
	Equals any `json:"equals"`
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
