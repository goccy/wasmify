# protoc-gen-wasmify-go

Go-native client code generator for wasmify's `.proto` files.

## Overview

This plugin generates a self-contained Go package from wasmify's proto definitions. It runs as a `buf generate` plugin (the standalone `gen-go` subcommand has been retired). The generated code hides all wasm and protobuf details — users interact with pure Go types.

## Generated API Design

### Singleton Module

The wasm module is initialized once via `Init()` and accessed globally. The plugin always emits a `//go:embed <package>.wasm` directive, so callers just call `Init()` after dropping the matching wasm binary next to the generated sources. Users never see the `Module` type:

```go
package mylib

func Init(opts ...Option) error     // Initialize from the embedded wasm
func Close() error                  // Shutdown
```

Place the wasm artefact (`<package>.wasm`, e.g. `mylib.wasm`) alongside the generated Go files so `go build` can resolve the embed.

### Compilation Mode

```go
mylib.Init(mylib.WithCompilationMode(mylib.CompilationModeCompiler))
```

| Mode | Description |
|------|-------------|
| `CompilationModeInterpreter` | Default. Slower execution, faster startup. |
| `CompilationModeCompiler` | Faster execution, slower startup. Uses wazero's compiler backend. |

### Handle Types

C++ classes become Go structs with methods. No `Module` reference stored:

```go
type Calculator struct {
    ptr uint64  // unexported
}

func NewCalculator(a, b float64) (*Calculator, error)
func (h *Calculator) Compute(op Operation, a, b float64) (float64, error)
func (h *Calculator) Name() (string, error)
```

### Automatic Resource Management

No public `Free()` method. C++ objects are released automatically when the Go handle is garbage collected:

```go
// Internal: called by GC via runtime.SetFinalizer
func (h *Calculator) free() {
    if h.ptr != 0 {
        module().invoke(serviceID, methodID, encodeHandle(h.ptr))
        h.ptr = 0  // Prevent double-free
    }
}
```

The finalizer is registered when a handle is created:

```go
func newCalculator(ptr uint64) *Calculator {
    h := &Calculator{ptr: ptr}
    runtime.SetFinalizer(h, (*Calculator).free)
    return h
}
```

### Package-Level Functions

Free functions become package-level functions (not methods on Module):

```go
// C++: ns::ParseStatement(sql, opts) → ParserOutput
// Go:
func ParseStatement(sql string, opts *ParserOptions) (*ParserOutput, error)
```

### Type-Safe Enums

Proto enums become named Go types with constants:

```go
type ParameterMode int32

const (
    ParameterModeParameterNamed      ParameterMode = 1
    ParameterModeParameterPositional ParameterMode = 2
    ParameterModeParameterNone       ParameterMode = 3
)

func (h *AnalyzerOptions) SetParameterMode(mode ParameterMode) error
```

### Abstract Type Dispatch

Abstract C++ classes become Go interfaces. Runtime type resolution uses C++ `typeid`:

```go
// Interface for abstract C++ class
type ASTStatementNode interface {
    ASTNodeNode
    rawPtr() uint64
    isASTStatement()
}

// Concrete type returned via typeid dispatch
output, _ := ParseStatement("SELECT 1", opts)
stmt, _ := output.Statement()  // Returns ASTStatementNode interface
queryStmt := stmt.(*ASTQueryStatement)  // Type assertion to concrete type
```

Internally, the generated code calls the dedicated `wasmify_get_type_name` wasm export to get the C++ class name, then looks up the Go type in `cppTypeToGoType` map. Each (service, method) pair is dispatched through its own `w_<svc>_<mid>` export — there is no aggregate `wasm_invoke` wrapper.

### Callback Trampoline

Go interfaces can be passed to C++ via the callback mechanism:

```go
type CallbackHandler interface {
    HandleCallback(methodID int32, req []byte) ([]byte, error)
}

func RegisterCallback(handler CallbackHandler) int32
func UnregisterCallback(id int32)
```

### Protobuf Wire Format

The generated code does **not** depend on `google.golang.org/protobuf`. It uses hand-written encode/decode helpers (`pbAppendVarint`, `pbReader`, etc.) for the protobuf wire format, keeping the only external dependency as `github.com/tetratelabs/wazero`.

### Wasm Runtime Stubs

The generated `Module` automatically handles:
- **WASI**: `wasi_snapshot_preview1` via wazero
- **C++ runtime stubs**: `__cxa_allocate_exception`, `__cxa_throw` (pattern-matched)
- **Thread stubs**: Functions matching `SemWait`/`sem_wait` return success
- **Unresolved env imports**: Stub functions for any `env` module import
- **Filesystem**: Root filesystem mounted for timezone data, etc.
- **Reactor mode**: `_initialize` called for C++ global constructors

## File Structure

The plugin emits a **single Go file** named after the proto package (e.g. `mylib.go`). It contains, in order:

- `Module` struct + `Init()` / `Close()` / option functions
- protobuf wire-format helpers (no dependency on `google.golang.org/protobuf`)
- named enum types and constants
- abstract type interfaces + `cppTypeToGoType` map + `resolveAbstractHandle()`
- concrete handle types and methods
- package-level free functions
- env-import stubs and callback dispatch

The package's wazero dependency is the only external import. The wasm binary is `//go:embed`-ed by filename matching the package (e.g. `mylib.wasm`).

## Usage

The plugin runs through `buf generate`, driven by `buf.gen.yaml` in the project's proto directory. After running it, drop the wasm binary next to the generated sources:

```
proto/
  buf.gen.yaml
  mylib.proto
mylib.wasm        <- name must match the Go package
mylib.go          <- generated by the plugin (//go:embed mylib.wasm)
```

`Init()` is the only entry point: there is no runtime override for the embed path. Test fixtures and CI must publish the wasm artefact under the correct filename before the package is built.

## Example

```go
package main

import "github.com/example/mylib"

func main() {
    mylib.Init()
    defer mylib.Close()

    opts, _ := mylib.NewParserOptions()
    output, _ := mylib.ParseStatement("SELECT 1", opts)
    stmt, _ := output.Statement()

    queryStmt := stmt.(*mylib.ASTQueryStatement)
    query, _ := queryStmt.Query()
    // ... navigate AST ...
    // No Free() calls needed — GC handles cleanup
}
```
