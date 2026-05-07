# wasmify

Convert a C/C++ project into a self-contained Go (or other language) package by compiling it to WebAssembly and auto-generating a typed, idiomatic binding — no wasm or protobuf knowledge required by the end user.

```
C/C++ project
    ↓ wasmify (analyze, build, parse headers, generate proto & bridge)
Intermediate artefacts (wasmify.json, proto, bridge.cc) + wasm binary
    ↓ protoc-gen-wasmify-go / protoc-gen-wasmify-ts / ...
Language-native package (e.g., "go-mylib")
    ↓ import
User code — calls pure Go/TS/etc. functions, no wasm details exposed
```

## Features

- **Analyze** any C/C++ build system (CMake, Make, Autotools, Bazel, Meson) by capturing real compiler invocations.
- **Transform** native builds to wasm32-wasip1 automatically; the captured bazel/cmake/make recording is replayed under wasi-sdk.
- **Parse** public headers via clang AST to extract APIs (functions, classes with methods + fields, enums, virtual hierarchies, doc comments).
- **Generate** Protobuf service definitions plus a C++ bridge that exposes every exported method as its own `WASM_EXPORT` — wasm-opt can DCE everything the host doesn't actually call.
- **Generate** language-native bindings (Go via `protoc-gen-wasmify-go`): singleton module, GC-based cleanup, type-safe enums, abstract type dispatch, callback factories for user-implementable interfaces (automatic for abstract classes, opt-in for concrete classes — see [docs/callback-services.md](docs/callback-services.md)), zero protobuf leakage.
- **Containerised pipeline**: a single `ghcr.io/<owner>/wasmify` image bakes wasi-sdk + binaryen + bazelisk + buf + the wasmify CLI + `protoc-gen-wasmify-go`, so CI and local builds use exactly the same toolchain.
- **One committed file** per project: `wasmify.json` carries project metadata, target selection, bridge config, and declarative skip rules. `arch.json` and `bridge-config.json` are gone.
- **Non-interactive mode** (`--non-interactive` / `WASMIFY_NON_INTERACTIVE`) plus declarative `skip` rules so CI never hangs on a y/N prompt.
- **Incremental updates** via `wasmify update` — detects upstream git changes and re-runs only the affected phases.
- **Idempotent output** — deterministic generation for clean git diffs.

## Installation

```bash
go install github.com/goccy/wasmify/cmd/wasmify@latest
```

`wasmify` auto-detects [wasi-sdk](https://github.com/WebAssembly/wasi-sdk) at `$WASI_SDK_PATH`, `/opt/wasi-sdk`, or `~/.config/wasmify/bin/wasi-sdk`, and pulls a pinned release into the latter on first use. Binaryen is fetched the same way under `~/.config/wasmify/bin/binaryen`.

For projects that prefer "no host installs at all", use the toolchain image:

```bash
docker pull ghcr.io/goccy/wasmify:edge
docker run --rm -v "$PWD:/work" -w /work ghcr.io/goccy/wasmify:edge wasmify --help
```

## Quick start

The recommended path is to let an AI agent drive the analysis once and capture the decisions in `wasmify.json`; subsequent runs are fully scripted.

### 1. Initialise the project

```bash
wasmify init /path/to/mylib --output-dir ./mylib-wasm
```

This creates `./mylib-wasm/wasmify.json` (the only file you commit), plus a `.wasmify/` cache.

### 2. Let an AI agent drive the analysis (recommended)

The repository ships an agent spec at [`agents/wasmify.md`](agents/wasmify.md) (Claude Code `/wasmify` slash command, etc.):

```
/wasmify /path/to/mylib
```

The agent:
- runs `wasmify save-arch` to fill in the project / build_system / targets sections,
- runs `wasmify classify` to lock in `user_selection`,
- runs `wasmify build` + `generate-build` against the upstream so `build.json` reflects a real native build,
- runs `parse-headers` + `gen-proto` and walks you through the `bridge` section of `wasmify.json` interactively (ExportFunctions, ExternalTypes, ErrorTypes, etc.),
- finishes with `wasmify wasm-build --optimize` to produce the wasm.

### 3. Manual workflow

If you prefer to run each step yourself:

```bash
cd ./mylib-wasm

# Phase 1 — describe the project (fills wasmify.json's analyzer-output sections).
echo '<arch-json>' | wasmify save-arch

# Phase 2 — pick which target(s) to build into one wasm.
wasmify classify --target mylib_main,mylib_extras

# Phase 3 — capture the native build (compiler-wrapper interposition + bazel aquery).
wasmify build --non-interactive
wasmify generate-build

# Phase 4 — parse headers + generate proto + bridge.
wasmify parse-headers
wasmify gen-proto

# Phase 5 — build wasm and chain wasm-opt.
wasmify wasm-build --optimize --non-interactive

# Phase 6 — generate the language binding.
buf generate
```

After this, `.wasmify/wasm-build/output/<project>.wasm` and `<package>.go` (next to the proto) are the artefacts to ship.

### `wasmify.json` — the only committed file

Project decisions live under typed sections:

```jsonc
{
  "version": "1",
  "upstream": { "path": "./mylib", "commit": "...", "branch": "main" },
  "project":     { "name": "mylib", "root_dir": "./mylib", "language": "c++" },
  "build_system": { "type": "bazel", "files": ["MODULE.bazel"] },
  "targets":      [ { "name": "mylib_main", "type": "library", "build_target": "//mylib:main" } ],
  "user_selection": { "target_names": ["mylib_main"], "build_type": "library" },
  "build_commands": { "configure": null, "build": "bazel build -c opt" },
  "bridge": {
    "ExportFunctions":    [ "mylib::ParseStatement", "mylib::Analyzer" ],
    "ExternalTypes":      [ "absl::Status", "absl::string_view" ],
    "ErrorTypes":         { "absl::Status": "if (!{result}.ok()) { _pw.write_error(std::string({result}.message())); }" },
    "ExportEnumPrefixes": [ "mylib::" ],
    // Concrete classes the user wants to subclass from Go. Abstract
    // classes are picked up automatically; concrete classes with
    // virtuals require this opt-in because C++ has no language-level
    // way to say "this concrete class is a customisation hook" — the
    // same syntactic shape is used for data-carrier classes (every
    // AST/Resolved node, etc.) and would balloon the generated
    // surface if auto-picked. See docs/callback-services.md for the
    // distinction.
    "CallbackClasses":    [ "mylib::TableValuedFunction" ]
  },
  "skip": {
    "files": [ { "path": "external/abseil-cpp~/absl/debugging/stacktrace.cc", "reason": "uses host-only stack unwinder" } ]
  }
}
```

Generated artefacts (`build.json`, `api-spec.json`, `proto/`, `bridge/`, the `.wasm` file, the `.go` file) all go in `.gitignore` and are reproduced by re-running the pipeline.

## Using the generated Go package

End users never see wasm or protobuf:

```go
package main

import "github.com/example/go-mylib"

func main() {
    mylib.Init()
    defer mylib.Close()

    opts, _ := mylib.NewParserOptions()
    output, _ := mylib.ParseStatement("SELECT 1", opts)
    stmt, _ := output.Statement()
    // No Free() calls — GC releases C++ memory automatically.
}
```

See [`protoc-plugins/protoc-gen-wasmify-go/README.md`](protoc-plugins/protoc-gen-wasmify-go/README.md) for the full Go API design.

## Containerised build (CI-friendly)

Per project, ship a tiny `Makefile` that calls the wasmify image; CI pipelines and local rebuilds then use the same toolchain. A typical `Makefile`:

```makefile
IMAGE  ?= ghcr.io/goccy/wasmify:edge
MEMORY ?= 14g
CPUS   ?= 8

wasm:
	docker run --rm -v $(CURDIR):/work -w /work \
	    --memory=$(MEMORY) --cpus=$(CPUS) $(IMAGE) \
	    bash -c 'wasmify build --non-interactive && \
	             wasmify generate-build && \
	             wasmify parse-headers && \
	             wasmify gen-proto && \
	             wasmify wasm-build --optimize --non-interactive && \
	             buf generate'
```

GitHub Actions consumes the same image via `jobs.<id>.container:`. See `googlesql-wasm`'s [`build.yml`](https://github.com/goccy/googlesql-wasm/blob/wasm/.github/workflows/build.yml) for a worked example that uploads the produced `.wasm` + `.go` and attaches an `actions/attest-build-provenance` SLSA attestation.

## 3-layer architecture (recommended for libraries)

For a library that will be distributed:

```
upstream-project/             # C/C++ source (unchanged)

mylib-wasm/                   # Intermediate artefacts (git-managed)
├── wasmify.json              # ← the only committed config
├── buf.yaml / buf.gen.yaml   # buf wiring for protoc-gen-wasmify-go
├── Makefile                  # `make wasm` runs the container pipeline
└── .github/workflows/build.yml  # CI: produces wasm + .go, signs with SLSA

go-mylib/                     # Go bindings (the .go file is generated, the .wasm is //go:embed'd)
├── mylib.go
└── mylib.wasm
```

### Incremental updates

When the upstream project changes, run:

```bash
wasmify update
```

It diffs the upstream commit and re-runs only the affected phases:
- `.h` changes → re-parse headers, regenerate proto, rebuild wasm.
- `.cc` changes → rebuild wasm only.
- Build-config changes → full rebuild.

## Commands reference

| Command                                | Purpose |
|----------------------------------------|---------|
| `wasmify init <project>`               | Initialise the project (creates `wasmify.json`). |
| `wasmify status`                       | Show cache state. |
| `wasmify save-arch`                    | Persist analyzer-output sections of `wasmify.json` (reads JSON from stdin). |
| `wasmify classify [--target <name>]`   | Pick build target(s); writes `user_selection`. Multi-target via comma list. |
| `wasmify build [-- <cmd>]`             | Run the native build under compiler-wrapper interposition. |
| `wasmify generate-build`               | Parse the captured build log → `build.json`. |
| `wasmify ensure-tools`                 | Install host tools listed under `required_tools` (CI-friendly). |
| `wasmify install-sdk`                  | Install wasi-sdk only. |
| `wasmify parse-headers`                | clang AST dump → `api-spec.json`. |
| `wasmify gen-proto`                    | Emit `.proto` + C++ bridge from `api-spec.json` + the `bridge` section of `wasmify.json`. |
| `wasmify wasm-build [--optimize]`      | Replay the build under wasi-sdk; produce `<project>.wasm`. `--optimize` chains `wasmify optimize`. |
| `wasmify optimize`                     | Run binaryen `wasm-opt -Oz --converge` (auto-installs binaryen). |
| `wasmify update`                       | Detect upstream changes + re-run affected phases. |

Global flag: `--non-interactive` (or env `WASMIFY_NON_INTERACTIVE=1`) — fails fast on every prompt path; declarative entries under `wasmify.json`'s `skip` section cover decisions that used to require y/N.

## Documentation

- [`agents/wasmify.md`](agents/wasmify.md) — AI agent workflow (Claude Code `/wasmify` etc.).
- [`protoc-plugins/README.md`](protoc-plugins/README.md) — wasm binary contract + proto options (plugin authors).
- [`protoc-plugins/protoc-gen-wasmify-go/README.md`](protoc-plugins/protoc-gen-wasmify-go/README.md) — Go plugin design.
