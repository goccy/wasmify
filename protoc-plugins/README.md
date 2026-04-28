# wasmify protoc plugins

This directory contains language-specific protoc plugins that generate native client code from wasmify's `.proto` files.

## Architecture

wasmify generates `.proto` files with custom options that describe the wasm bridge interface. Language-specific protoc plugins read these options and generate idiomatic client code for their target language.

```
C/C++ project
    ↓ wasmify gen-proto
.proto files (with wasmify custom options)
    ↓ protoc-gen-wasmify-go / protoc-gen-wasmify-ts / ...
Language-native client code
    ↓ + wasm binary
Self-contained package (no wasm/proto knowledge needed by users)
```

## Wasm Binary Interface

The wasm binary always exports these utility functions:

| Export | Signature | Description |
|--------|-----------|-------------|
| `wasm_alloc` | `(size: i32) → ptr: i32` | Allocate memory in wasm. |
| `wasm_free` | `(ptr: i32)` | Free wasm memory. |
| `wasm_init` | `() → i32` | Initialise the bridge (call after `_initialize`). |
| `_initialize` | `()` | Run C++ global constructors (reactor mode). |
| `wasmify_get_type_name` | `(req_ptr: i32, req_len: i32) → packed: i64` | Runtime type query — returns the demangled C++ class name for a handle pointer. |

In addition, **every (service, method) pair has its own wasm export** named `w_<service_id>_<method_id>` with signature `(req_ptr: i32, req_len: i32) → packed: i64`. The host looks each one up by name (via the runtime's `ExportedFunction(...)` API) and calls it directly — there is no aggregate dispatcher.

The wasm binary imports:

| Module | Name | Signature | Description |
|--------|------|-----------|-------------|
| `wasmify` | `callback_invoke` | `(callback_id: i32, method_id: i32, req_ptr: i32, req_len: i32) → i64` | Host callback. |

### Calling Convention

1. Look up the export by name once: `fn = mod.ExportedFunction("w_<service_id>_<method_id>")`. Cache it.
2. Serialise the request as protobuf wire format bytes.
3. Allocate wasm memory: `ptr = wasm_alloc(len)`.
4. Copy request bytes into wasm memory at `ptr`.
5. Call: `packed = fn(ptr, len)`.
6. Decode packed result: `resp_ptr = packed >> 32`, `resp_len = packed & 0xFFFFFFFF`.
7. Read response bytes from wasm memory at `(resp_ptr, resp_len)`.
8. Free both buffers: `wasm_free(resp_ptr)`, `wasm_free(ptr)`.

### Service ID Assignment

The service IDs encoded in export names (`w_<svc>_<mid>`) follow the same numbering used by `wasm_service_id` in the generated `.proto`:

| service_id | Meaning |
|------------|---------|
| `0` | Free function service (package-level functions, when the project has any). |
| `1+` | Handle class services in alphabetical order of the message name. (When there are no free functions, handle classes start at `0`.) |

For runtime type queries call `wasmify_get_type_name(req_ptr, req_len)` directly — it has no service-id sentinel; the request encodes a handle ptr in field 1 and the response carries the demangled C++ class name in field 1 (string).

## Proto Custom Options

Defined in `proto/wasmify/options.proto`:

### Message Options

| Option | Field # | Type | Description |
|--------|---------|------|-------------|
| `wasm_handle` | 50000 | `bool` | Message represents an opaque C++ pointer (handle type) |
| `wasm_parent` | 50002 | `string` | Parent class name for inheritance hierarchy |
| `wasm_abstract` | 50009 | `bool` | Handle is abstract (no constructor, used as interface) |
| `wasm_source_file` | 50007 | `string` | Path to C++ header defining this class |
| `wasm_list_type` | 50005 | `bool` | Message is a list wrapper |
| `wasm_map_type` | 50006 | `bool` | Message is a map wrapper |

### Service Options

| Option | Field # | Type | Description |
|--------|---------|------|-------------|
| `wasm_service_id` | 50010 | `int32` | Numeric service ID for `wasm_invoke` dispatch |
| `wasm_callback` | 50001 | `bool` | Service is a callback interface (host → wasm) |
| `wasm_service_source_file` | 50008 | `string` | Path to C++ header |

### Method Options

| Option | Field # | Type | Description |
|--------|---------|------|-------------|
| `wasm_method_id` | 50011 | `int32` | Numeric method ID within the service |
| `wasm_method_type` | 50003 | `string` | Method category (see below) |
| `wasm_original_name` | 50004 | `string` | Original C++ name (for overloaded methods) |

## Method Types

The `wasm_method_type` option classifies each RPC:

### `"constructor"`

Creates a new C++ object on the heap.

- **Request**: Parameters only (no handle field)
- **Response**: Handle message with `ptr` of newly created object
- **Plugin should generate**: Factory function (e.g., `NewCalculator(...)`)

### `"free"`

Destroys a C++ object (`delete ptr`).

- **Request**: Handle message only
- **Response**: Empty (just error field 15)
- **Plugin should generate**: Destructor or release method. Ideally tied to GC/destructor in the target language.

### `"getter"`

Reads a property from a handle.

- **Request**: Handle message (same as the service's handle type)
- **Response**: Result field + error field 15
- **Plugin should generate**: Property accessor with no arguments (e.g., `handle.Name()`)

### `"downcast"`

Attempts `dynamic_cast<Derived*>(base)`.

- **Request**: Base handle message
- **Response**: Derived handle message (ptr = 0 if cast fails)
- **Plugin should generate**: Type conversion method returning nullable derived type

### `"static_factory"`

Calls a static factory method (e.g., `ParseResumeLocation::FromString(...)`).

- **Request**: Parameters only (no handle field, like constructor)
- **Response**: Handle message
- **Plugin should generate**: Factory function (like constructor)

### `""` (empty / regular)

Standard method call on a handle.

- **Request**: Field 1 = handle, fields 2+ = parameters
- **Response**: Result fields + error field 15
- **Plugin should generate**: Method on handle type

## Wire Format Details

### Handle Encoding

Handles are proto submessages containing `uint64 ptr = 1`:

```
// Writing a handle in field 2:
tag(field=2, wire_type=2)  // length-delimited
varint(length_of_submessage)
  tag(field=1, wire_type=0)  // varint
  varint(ptr_value)
```

**Important**: Constructor responses write ptr as a direct varint (field 1, wire_type 0), not a submessage. Plugins must handle both patterns:
- Constructor response: `field 1 = varint(ptr)` 
- Getter/method response: `field 1 = submessage { field 1 = varint(ptr) }`

### Error Convention

All responses use **field 15** (string) for error messages. Empty string = success.

```protobuf
message SomeResponse {
  ResultType result = 1;
  string error = 15;     // Always field 15
}
```

### Runtime Type Query

For abstract handle dispatch, call the dedicated `wasmify_get_type_name` export. The request encodes a handle ptr in field 1; the response carries the demangled C++ class name in field 1 (string).

This enables mapping C++ runtime types to language-specific concrete types.

## Building a New Plugin

### Step 1: Read Proto Options

Parse the `.proto` file using your language's protoc plugin framework. Extract wasmify options from messages, services, and methods.

```
For each service:
  service_id = service.options.wasm_service_id
  For each method:
    method_id = method.options.wasm_method_id
    method_type = method.options.wasm_method_type
```

### Step 2: Classify Messages

```
handle_messages = {msg | msg.options.wasm_handle == true}
abstract_messages = {msg | msg.options.wasm_abstract == true}
parent_map = {msg.name → msg.options.wasm_parent}
```

### Step 3: Generate Module Abstraction

Generate a module type that wraps the wasm runtime. The dispatch
helper resolves each `(service_id, method_id)` pair to the matching
`w_<svc>_<mid>` export the first time it is called and caches the
function pointer, so subsequent calls hit a map.

```
class/struct Module:
  runtime: WasmRuntime
  exports: Map<(svc, mid), WasmFunction>

  method invoke(service_id, method_id, request_bytes) → response_bytes:
    fn = exports[(service_id, method_id)]
    if fn == nil:
      fn = runtime.ExportedFunction(format("w_%d_%d", service_id, method_id))
      exports[(service_id, method_id)] = fn

    ptr = wasm_alloc(len(request_bytes))
    write(ptr, request_bytes)
    packed = fn(ptr, len)             // direct call — no central dispatcher
    resp_ptr = packed >> 32
    resp_len = packed & 0xFFFFFFFF
    result = read(resp_ptr, resp_len)
    wasm_free(resp_ptr)
    wasm_free(ptr)
    return result
```

### Step 4: Generate Handle Types

For each handle message:

```
class HandleType:
  ptr: uint64
  
  // For each regular method on the service:
  method MethodName(params...) → result:
    request = encode(handle=self.ptr, params...)
    response = module.invoke(service_id, method_id, request)
    check_error(response, field=15)
    return decode_result(response)
```

### Step 5: Generate Abstract Interfaces

For each abstract handle message, generate an interface/protocol/trait:

```
interface AbstractNode:
  marker_method()  // For type checking

class ConcreteNode implements AbstractNode:
  // Inherits from parent chain
```

Use runtime type query (service_id = -1) to dispatch abstract returns to concrete types.

### Step 6: Generate Enums

For each proto enum, generate a named type with constants in the target language.

### Step 7: Resource Management

Tie the `"free"` RPC to the target language's resource management idiom:

| Language | Mechanism |
|----------|-----------|
| Go | `runtime.SetFinalizer` |
| TypeScript/JavaScript | `FinalizationRegistry` |
| Rust | `Drop` trait |
| Python | `__del__` or context manager (`__enter__`/`__exit__`) |
| C# | `IDisposable` + `Dispose()` |
| Swift | `deinit` |

The goal is to make `Free` invisible to the user — resources are released automatically when the handle goes out of scope or is garbage collected.

### Step 8: Callback Support

If the proto has services with `wasm_callback = true`, generate:
1. An interface for the callback methods
2. Registration mechanism to assign callback IDs
3. Dispatch logic in the host's `callback_invoke` implementation

## Example: Proto → Generated Code

Given this proto:

```protobuf
message Calculator {
  option (wasmify.wasm_handle) = true;
  uint64 ptr = 1;
}

service CalculatorService {
  option (wasmify.wasm_service_id) = 1;
  
  rpc New(CalculatorNewRequest) returns (Calculator) {
    option (wasmify.wasm_method_id) = 0;
    option (wasmify.wasm_method_type) = "constructor";
  }
  rpc Compute(CalculatorComputeRequest) returns (CalculatorComputeResponse) {
    option (wasmify.wasm_method_id) = 1;
  }
  rpc Free(Calculator) returns (Empty) {
    option (wasmify.wasm_method_id) = 2;
    option (wasmify.wasm_method_type) = "free";
  }
}
```

A plugin generates language-idiomatic code. The key elements:

1. **Module wrapper** — Encapsulates `wasm_alloc` / `wasm_free` and a per-`(service_id, method_id)` cache of `w_<svc>_<mid>` exports.
2. **Handle struct/class** — Wraps `uint64 ptr`, provides methods.
3. **Constructor** — Calls the export tagged `method_type = "constructor"`, returns handle.
4. **Methods** — Encode handle + params, call `invoke`, decode response.
5. **Free** — Tied to language's resource management (GC finalizer, destructor, etc.).
6. **Error check** — Always inspect field 15 of response.

See language-specific READMEs for concrete examples:
- [Go plugin](protoc-gen-wasmify-go/README.md)

## Existing Plugins

| Plugin | Language | Status | README |
|--------|----------|--------|--------|
| `protoc-gen-wasmify-go` | Go | Production | [README](protoc-gen-wasmify-go/README.md) |
| `protoc-gen-wasmify-ts` | TypeScript | Planned | — |
