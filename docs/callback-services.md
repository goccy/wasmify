# Callback services: when wasmify generates them

`wasmify` emits a *callback service* for a C++ class when the class is
intended to be **subclassed by a Go user**. The generated artefacts —
a `<Class>Callback` Go interface, a `<Class>CallbackDefaults` zero-impl
struct, a `New<Class>FromImpl(impl, …)` Go constructor, and a C++
trampoline subclass — let a user supply Go code that the C++ side calls
back into via `wasmify_callback_invoke`.

This document explains **why some classes are picked up automatically
and others require explicit opt-in in `wasmify.json`**. The asymmetry
is not an oversight — it is forced by what C++ can express about a
class's intent.

## The two cases

### Case A — abstract class (automatic)

A class is picked up automatically when it is **abstract** in the C++
sense: at least one inherited pure virtual (a `virtual` method
declared with `= 0`) is not overridden in the class itself. The C++
standard refuses to compile `Catalog c;` in that situation, so the
author has *language-level forced* the user into a subclass.

The rule is exactly what `std::is_abstract<T>::value` returns at
compile time. It is **not** about how many of the class's methods are
virtual, nor about whether any are non-pure:

- `Catalog` has many pure virtuals — abstract → automatic.
- `Sequence` has a single pure virtual (`GetNext`) plus many ordinary
  methods — still abstract because of that one pure virtual →
  automatic.
- `GraphElementLabel` has a mix of pure virtuals and plain virtuals
  with default bodies — abstract because at least one pure is
  unimplemented → automatic.

> Abstractness is a *language-level guarantee* that the class is
> meant to be subclassed. wasmify mirrors that guarantee one-for-one.

`wasmify` reads this signal directly: any class with `is_abstract: true`
in the parsed `api-spec.json`, with at least one declarable pure
virtual, becomes a callback candidate. No user configuration is
required.

Examples handled automatically in the googlesql binding:

- `googlesql::Catalog` — abstract, pure virtual `FindTable`,
  `FindFunction`, …
- `googlesql::Connection` — abstract, pure virtual `Name`, `FullName`
- `googlesql::EnumerableCatalog` — abstract, extends `Catalog` with
  enumeration pure virtuals
- `googlesql::Sequence` — abstract, pure virtual `GetNext`
- `googlesql::Model`, `googlesql::Rewriter`, `googlesql::Procedure`*,
  `googlesql::Googlesql_Column`, the parse-tree visitor family, and
  every `Graph*Callback` interface

(*`Procedure` shape may vary across upstream commits; what matters is
its `is_abstract` bit.*)

The author of these classes has signalled "must be subclassed" by
making the class abstract; wasmify mirrors that signal one-for-one.

### Case B — concrete class with virtuals (opt-in)

A class with one or more virtual methods but **no unimplemented pure
virtual** is concrete in the C++ sense — `TableValuedFunction tvf(...)`
compiles, the class is fully usable as-is. The virtual methods exist
because subclasses *may* customise them, not because they *must*.
C++ has no language-level way to distinguish the two intents.

The same syntactic shape (concrete class + virtuals, all virtuals
have default bodies in the base) is used for two very different
design intents:

| Intent | Example |
| --- | --- |
| Customisation hook — every method has a usable default in the base, subclasses override what they need | `googlesql::TableValuedFunction` (override `Resolve`, `CreateEvaluator`) |
| Data carrier — the virtual is part of a visitor pattern; concrete instances are produced by the framework and consumed read-only by the user | `googlesql::ASTAbortBatchStatement`, `googlesql::ResolvedQueryStmt`, every other concrete `AST*` / `Resolved*` node |

Auto-picking every concrete class with virtuals would explode the
generated surface — in the googlesql api-spec it produces roughly
820 callback services, the vast majority of which are AST/Resolved
data carriers nobody intends to override. The schema, the bridge,
and the Go binding each grow proportionally.

There is no structural property in clang's AST output that
reliably separates "customisation hook" from "data carrier" without
falling back to naming heuristics, which the project intentionally
avoids. The intent therefore has to come from the user.

`wasmify.json:bridge.CallbackClasses` is the explicit signal:

```jsonc
{
  "bridge": {
    "ExportFunctions":   [ /* ... */ ],
    "ExternalTypes":     [ /* ... */ ],
    "CallbackClasses":   [
      "mylib::TableValuedFunction",
      "mylib::Procedure"
    ]
  }
}
```

A class listed in `CallbackClasses` follows the same callback
generation pipeline as an abstract class: `<Class>Callback` Go
interface, `<Class>CallbackDefaults`, `New<Class>FromImpl(impl, …)`,
and the C++ trampoline. **No special-case code paths.** The list
acts as a permission gate, not as a separate code path.

Listed classes that are also abstract are accepted unconditionally
(the listing is redundant but harmless). Listed concrete classes
without any virtual method are rejected with a descriptive error —
a callback hook would have nothing to dispatch.

## Why this is not a bug to "fix"

A reasonable instinct is "shouldn't the generator be able to figure
this out automatically?". The answer is "not without making things
worse":

- **Naming-based heuristics** (e.g. "if the class name ends in
  `Visitor` it's a customisation point, otherwise it's data") make
  the generator silently break when an unrelated codebase reuses the
  same naming convention for a different purpose. The project
  forbids these.
- **Structural heuristics** ("concrete class with no parent and a
  direct virtual method", "concrete class with `has_deleted_copy_ctor:
  true`", etc.) catch some common cases but mis-classify others —
  `SimpleCatalog` has the same flags as `TableValuedFunction` but is
  meant to be used directly, not subclassed.
- **Listing per-class behaviour in the generator source** would mean
  shipping a hardcoded library-specific allow-list inside `wasmify`,
  which the project also forbids.

The only library-agnostic, reliable signal is the user. C++'s
abstractness gives wasmify that signal for free in Case A; for Case B
the user supplies it through `wasmify.json`.

## What is *not* user-configurable

`CallbackClasses` only governs the **selection** decision — "is this
class a callback target at all?". Everything downstream of that
selection is automatic and identical for both Case A and Case B:

- The trampoline's C++ constructor is derived from the base class's
  constructor signature, including for classes that have only
  arg-taking constructors (`TableValuedFunction(name_path, group,
  signatures, options)` — wasmify forwards every arg through the
  trampoline ctor, no flag required).
- The Go-side `New<Class>FromImpl(impl, …)` exposes the same
  constructor arguments verbatim.
- Every virtual method of the class — pure or non-pure, declared on
  the class or inherited — is translated into one
  `<Class>Callback` interface method.
- Non-overridden non-pure virtuals fall back to the base class's
  implementation through the trampoline; a Go user who doesn't care
  about a particular virtual leaves the corresponding
  `<Class>CallbackDefaults` method unchanged.

These are all generator responsibilities. If any of them is missing
or incorrect for a class that *was* selected (whether automatically
or via opt-in), it is a generator bug, not something the user can
work around in `wasmify.json`.

## Quick reference

| Class shape | C++ rule | Result | User action |
| --- | --- | --- | --- |
| At least one inherited pure virtual is unimplemented (`std::is_abstract<T>::value == true`) | `T t;` does not compile | Callback service generated automatically | None |
| Has virtuals but every pure virtual is overridden, or there are no pure virtuals | `T t;` compiles | Callback service generated **iff** the class is listed in `bridge.CallbackClasses` | Add the qualified class name to `bridge.CallbackClasses` if subclassing is intended |
| No virtual methods (direct or inherited) | `T t;` compiles, no dispatch hook | No callback service | Listing in `CallbackClasses` is rejected — there would be nothing to override |
