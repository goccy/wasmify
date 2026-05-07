package main

import (
	"strings"
	"testing"
)

// TestCallbackCaseBody_BorrowedHandleUsesNoFinalizer pins the
// ownership rule for handles flowing INTO a Go callback: the C++
// trampoline still owns the underlying object (it is borrowing the
// pointer for the duration of the dispatch), so the Go-side wrapper
// the adapter materialises MUST NOT acquire its own GC finaliser.
//
// Without this rule the temporary Go wrapper races whatever outer
// Go wrapper the application already holds for the same C++
// address: both finalisers eventually fire Free RPC, double-freeing
// the C++ object and corrupting the wasm allocator's freelist. The
// fault commonly surfaces in a *later* test as
// `wasm error: out of bounds memory access` from an unrelated
// export — making the regression hard to localise without this
// pinning unit test.
//
// The fix is in writeCallbackCaseBody: every non-abstract,
// non-repeated, non-value-message handle input is wrapped via
// `new<Handle>NoFinalizer(_ptr)`. The repeated-handle branch
// follows the same rule. The abstract path resolves through
// `cppTypeFactories` which already point at NoFinalizer
// constructors, so it was already correct.
//
// This test uses a synthetic `Widget` handle so the assertion is
// independent of any real library.
func TestCallbackCaseBody_BorrowedHandleUsesNoFinalizer(t *testing.T) {
	t.Run("singular_handle_input", func(t *testing.T) {
		var b strings.Builder
		writeCallbackCaseBody(&b, "Widget", svcMethodInfo{
			name:     "OnEvent",
			methodID: 7,
			inputFields: []fieldInfo{
				{
					fieldNum:   1,
					fieldName:  "source",
					goName:     "Source",
					isHandle:   true,
					handleName: "Source",
				},
			},
		})
		got := b.String()
		const want = "newSourceNoFinalizer(_ptr)"
		if !strings.Contains(got, want) {
			t.Fatalf("singular handle input must wrap with NoFinalizer\nwant substring: %s\ngot:\n%s", want, got)
		}
		// Belt-and-braces: ensure we are NOT also emitting the
		// finaliser-attaching variant. A naive regex on the test
		// output above would match `newSourceNoFinalizer` as a
		// suffix of `newSource`, but the body string would also
		// contain a bare `newSource(_ptr)` if we regressed; check
		// directly.
		if strings.Contains(got, "newSource(_ptr)") {
			t.Fatalf("singular handle input must NOT use the finaliser-attaching ctor; got:\n%s", got)
		}
	})

	t.Run("repeated_handle_input", func(t *testing.T) {
		var b strings.Builder
		writeCallbackCaseBody(&b, "Widget", svcMethodInfo{
			name:     "OnBatch",
			methodID: 8,
			inputFields: []fieldInfo{
				{
					fieldNum:   2,
					fieldName:  "items",
					goName:     "Items",
					goType:     "[]*Item",
					isHandle:   true,
					isRepeated: true,
					handleName: "Item",
				},
			},
		})
		got := b.String()
		const want = "newItemNoFinalizer(_ptr)"
		if !strings.Contains(got, want) {
			t.Fatalf("repeated handle input must wrap with NoFinalizer\nwant substring: %s\ngot:\n%s", want, got)
		}
		if strings.Contains(got, ", newItem(_ptr))") {
			t.Fatalf("repeated handle input must NOT use the finaliser-attaching ctor; got:\n%s", got)
		}
	})
}

// TestCallbackCaseBody_HandleReturnTransfersOwnership pins the
// owning half of the ownership contract: when the C++ trampoline
// takes ownership of the returned handle (smart-pointer output
// param), the Go wrapper that produced it must drop its `ptr` so
// the per-instance finaliser does not race the C++ destructor and
// double-free.
//
// The bridge marks owning fields with `wasm_take_ownership`, which
// the field parser lifts into `fieldInfo.takesOwnership`. The
// adapter then encodes the clear by calling the runtime helper
// `clearPtrAny(_v)`, which handles both the concrete-pointer
// path (nil-safe via the wrapper's clearPtr method) and the
// abstract-interface path (type-asserts to the clearPtr
// interface internally). The helper-call form keeps gofmt from
// expanding the inline 3-line stanza that earlier generations of
// the plugin emitted.
func TestCallbackCaseBody_HandleReturnTransfersOwnership(t *testing.T) {
	t.Run("concrete_return_calls_clearPtr", func(t *testing.T) {
		var b strings.Builder
		writeCallbackCaseBody(&b, "Widget", svcMethodInfo{
			name:     "Make",
			methodID: 9,
			outputFields: []fieldInfo{
				{
					fieldNum:       1,
					fieldName:      "result",
					goName:         "Result",
					isHandle:       true,
					takesOwnership: true,
					handleName:     "Result",
				},
			},
		})
		got := b.String()
		if !strings.Contains(got, "clearPtrAny(_v)") {
			t.Fatalf("concrete handle return with take_ownership must clear via the clearPtrAny helper\ngot:\n%s", got)
		}
	})

	t.Run("abstract_return_clearPtr_via_interface", func(t *testing.T) {
		var b strings.Builder
		writeCallbackCaseBody(&b, "Widget", svcMethodInfo{
			name:     "MakeAbstract",
			methodID: 10,
			outputFields: []fieldInfo{
				{
					fieldNum:       1,
					fieldName:      "result",
					goName:         "Result",
					isHandle:       true,
					isAbstract:     true,
					takesOwnership: true,
					handleName:     "Result",
				},
			},
		})
		got := b.String()
		// clearPtrAny dispatches to the interface{clearPtr()}
		// type assertion internally; the call site only needs to
		// pass the value. Pin the helper-call form here.
		if !strings.Contains(got, "clearPtrAny(_v)") {
			t.Fatalf("abstract handle return with take_ownership must clear via the clearPtrAny helper\ngot:\n%s", got)
		}
	})
}

// TestCallbackCaseBody_BorrowedHandleReturn pins the borrowed half
// of the ownership contract: when the C++ trampoline does NOT take
// ownership (raw `T**` / `T*&` output param shape), the Go wrapper
// that produced the returned handle MUST retain its finaliser
// obligation. Emitting clearPtr for a borrowed return would orphan
// the allocation: Go finalizer becomes a no-op, the C++ side does
// not delete either, and on the next invoke the same wrapper —
// re-used by the user (e.g. a singleton catalog member) — would
// hand back NULL through `rawPtr() == 0`. Downstream C++ code that
// dereferenced the slot or stored the NULL alongside live entries
// would then trip a hard-to-localise allocator fault several
// iterations later.
//
// This test uses a method-spec WITHOUT `takesOwnership: true` to
// represent the borrowed shape and asserts the body leaves the
// returned wrapper untouched after writing it to the wire.
func TestCallbackCaseBody_BorrowedHandleReturnDoesNotClearPtr(t *testing.T) {
	t.Run("concrete_borrowed_return_omits_clearPtr", func(t *testing.T) {
		var b strings.Builder
		writeCallbackCaseBody(&b, "Catalog", svcMethodInfo{
			name:     "FindBorrowed",
			methodID: 11,
			outputFields: []fieldInfo{
				{
					fieldNum:       1,
					fieldName:      "result",
					goName:         "Result",
					isHandle:       true,
					takesOwnership: false,
					handleName:     "Item",
				},
			},
		})
		got := b.String()
		if strings.Contains(got, "clearPtr()") {
			t.Fatalf("borrowed concrete handle return MUST NOT clearPtr (the Go wrapper retains ownership):\n%s", got)
		}
	})

	t.Run("abstract_borrowed_return_omits_clearPtr", func(t *testing.T) {
		var b strings.Builder
		writeCallbackCaseBody(&b, "Catalog", svcMethodInfo{
			name:     "FindBorrowedAbstract",
			methodID: 12,
			outputFields: []fieldInfo{
				{
					fieldNum:       1,
					fieldName:      "result",
					goName:         "Result",
					isHandle:       true,
					isAbstract:     true,
					takesOwnership: false,
					handleName:     "Item",
				},
			},
		})
		got := b.String()
		if strings.Contains(got, "clearPtr()") {
			t.Fatalf("borrowed abstract handle return MUST NOT clearPtr (the Go wrapper retains ownership):\n%s", got)
		}
	})
}
