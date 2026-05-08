package protogen

import (
	"strings"
	"testing"

	"github.com/goccy/wasmify/internal/apispec"
	"github.com/goccy/wasmify/internal/state"
)

// complexAPISpec returns an APISpec exercising value types, vectors,
// constructors, static factories, abstract classes, errors, and more.
func complexAPISpec() *apispec.APISpec {
	src := "mylib/types.h"
	return &apispec.APISpec{
		Namespace: "mylib",
		Functions: []apispec.Function{
			{
				Name:     "MakeResult",
				QualName: "mylib::MakeResult",
				ReturnType: apispec.TypeRef{
					Kind: apispec.TypeValue,
					Name: "mylib::Result",
				},
				SourceFile: src,
			},
			{
				// Vector return
				Name:     "GetNumbers",
				QualName: "mylib::GetNumbers",
				ReturnType: apispec.TypeRef{
					Kind:  apispec.TypeVector,
					Inner: &apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"},
				},
				SourceFile: src,
			},
			{
				// Vector of strings
				Name:     "GetStrings",
				QualName: "mylib::GetStrings",
				ReturnType: apispec.TypeRef{
					Kind:  apispec.TypeVector,
					Inner: &apispec.TypeRef{Kind: apispec.TypeString},
				},
				SourceFile: src,
			},
		},
		Classes: []apispec.Class{
			{
				Name:                 "Widget",
				QualName:             "mylib::Widget",
				Namespace:            "mylib",
				IsHandle:             true,
				HasPublicDefaultCtor: true,
				HasPublicDtor:        true,
				Methods: []apispec.Function{
					{
						Name:          "Widget",
						QualName:      "mylib::Widget::Widget",
						IsConstructor: true,
						ReturnType:    apispec.TypeRef{Kind: apispec.TypeVoid},
						Params: []apispec.Param{
							{Name: "name", Type: apispec.TypeRef{Kind: apispec.TypeString}},
						},
						SourceFile: src,
					},
					{
						Name:     "GetName",
						QualName: "mylib::Widget::GetName",
						ReturnType: apispec.TypeRef{Kind: apispec.TypeString},
						SourceFile: src,
					},
					{
						// Static factory
						Name:     "FromString",
						QualName: "mylib::Widget::FromString",
						IsStatic: true,
						ReturnType: apispec.TypeRef{
							Kind: apispec.TypeHandle,
							Name: "Widget",
						},
						Params: []apispec.Param{
							{Name: "s", Type: apispec.TypeRef{Kind: apispec.TypeString}},
						},
						SourceFile: src,
					},
				},
				SourceFile: src,
			},
			{
				Name:                 "Result",
				QualName:             "mylib::Result",
				Namespace:            "mylib",
				IsHandle:             false,
				HasPublicDefaultCtor: true,
				HasPublicDtor:        true,
				Fields: []apispec.Field{
					{Name: "code", Type: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"}},
					{Name: "msg", Type: apispec.TypeRef{Kind: apispec.TypeString}},
					{Name: "ok", Type: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "bool"}},
				},
				SourceFile: src,
			},
			{
				// Abstract base class
				Name:          "Base",
				QualName:      "mylib::Base",
				Namespace:     "mylib",
				IsHandle:      true,
				IsAbstract:    true,
				HasPublicDtor: true,
				Methods: []apispec.Function{
					{
						Name:     "Describe",
						QualName: "mylib::Base::Describe",
						IsVirtual: true,
						ReturnType: apispec.TypeRef{Kind: apispec.TypeString},
						SourceFile: src,
					},
				},
				SourceFile: src,
			},
			{
				Name:                 "Derived",
				QualName:             "mylib::Derived",
				Namespace:            "mylib",
				IsHandle:             true,
				HasPublicDefaultCtor: true,
				HasPublicDtor:        true,
				Parent:               "mylib::Base",
				Methods: []apispec.Function{
					{
						Name:       "Describe",
						QualName:   "mylib::Derived::Describe",
						IsVirtual:  true,
						ReturnType: apispec.TypeRef{Kind: apispec.TypeString},
						SourceFile: src,
					},
				},
				SourceFile: src,
			},
		},
	}
}

func TestGenerateBridge_Complex(t *testing.T) {
	spec := complexAPISpec()
	output := GenerateBridge(spec, "mylib", "")

	// Contains per-method exports for free functions and each handle
	// service. Service IDs follow proto.go's ordering (free functions
	// first when present, then handle classes alphabetically).
	checks := []string{
		"WASM_EXPORT(w_0_0)", // first free function export
		"Widget",
		"Describe",
		"MakeResult",
		"GetNumbers",
		"FromString", // static factory
		// Result is a value class with fields — check for submessage writes
		"write_submessage",
	}
	for _, want := range checks {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(output, want) {
				t.Errorf("expected %q in output", want)
			}
		})
	}
}

func TestGenerateProto_Complex(t *testing.T) {
	spec := complexAPISpec()
	output := GenerateProto(spec, "mylib")
	// Check for expected services
	checks := []string{
		"service Mylib", // free functions
		"message Widget",
		"message Result",
		"message Base",
		"message Derived",
		"service WidgetService",
		"service BaseService",
		"wasm_abstract",       // Base is abstract
		"wasm_parent",         // Derived has parent
		"rpc FromString",      // static factory
		"repeated int32",      // GetNumbers vector
		"repeated string",     // GetStrings vector
		"MakeResultResponse", // MakeResult returns Result value type
	}
	for _, want := range checks {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(output, want) {
				t.Errorf("expected %q in output", want)
			}
		})
	}
}

// TestGenerateBridge_WithErrorType exercises matchErrorType by configuring
// an error pattern for a value type.
func TestGenerateBridge_WithErrorType(t *testing.T) {
	spec := &apispec.APISpec{
		Namespace: "mylib",
		Functions: []apispec.Function{
			{
				Name:     "DoWork",
				QualName: "mylib::DoWork",
				ReturnType: apispec.TypeRef{
					Kind:     apispec.TypeValue,
					Name:     "absl::Status",
					QualType: "absl::Status",
				},
				SourceFile: "mylib/foo.h",
			},
		},
	}
	cfg := BridgeConfig{
		ExternalTypes: []string{"absl::Status"},
		ErrorTypes: map[string]string{
			"absl::Status": `if (!{result}.ok()) { _pw.write_error("error"); }`,
		},
	}
	output := GenerateBridgeWithConfig(spec, "mylib", "", cfg)
	// The error pattern should be inlined
	if !strings.Contains(output, ".ok()") {
		t.Error("expected error-pattern code (.ok()) in bridge output")
	}
}

// TestGenerateBridge_WithMethodsAndGetters exercises writeCallBody,
// writeGetterBody, and writeConstructorBody paths via a complex class.
func TestGenerateBridge_WithMethodsAndGetters(t *testing.T) {
	src := "mylib/foo.h"
	spec := &apispec.APISpec{
		Namespace: "mylib",
		Classes: []apispec.Class{
			{
				Name:                 "Foo",
				QualName:             "mylib::Foo",
				Namespace:            "mylib",
				IsHandle:             true,
				HasPublicDefaultCtor: true,
				HasPublicDtor:        true,
				Methods: []apispec.Function{
					// Constructor
					{
						Name:          "Foo",
						QualName:      "mylib::Foo::Foo",
						IsConstructor: true,
						Params: []apispec.Param{
							{Name: "x", Type: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"}},
						},
						SourceFile: src,
					},
					// Method returning primitive
					{
						Name:       "Value",
						QualName:   "mylib::Foo::Value",
						ReturnType: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"},
						IsConst:    true,
						SourceFile: src,
					},
					// Method with string return
					{
						Name:       "Name",
						QualName:   "mylib::Foo::Name",
						ReturnType: apispec.TypeRef{Kind: apispec.TypeString},
						SourceFile: src,
					},
					// Method with vector<int> return
					{
						Name:     "Numbers",
						QualName: "mylib::Foo::Numbers",
						ReturnType: apispec.TypeRef{
							Kind:  apispec.TypeVector,
							Inner: &apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"},
						},
						SourceFile: src,
					},
					// Method with params including string
					{
						Name:     "SetName",
						QualName: "mylib::Foo::SetName",
						Params: []apispec.Param{
							{Name: "n", Type: apispec.TypeRef{Kind: apispec.TypeString}},
						},
						ReturnType: apispec.TypeRef{Kind: apispec.TypeVoid},
						SourceFile: src,
					},
				},
				Fields: []apispec.Field{
					// Public field → getter generated
					{Name: "count", Type: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"}, Access: "public"},
					{Name: "label", Type: apispec.TypeRef{Kind: apispec.TypeString}, Access: "public"},
				},
				SourceFile: src,
			},
		},
	}
	output := GenerateBridge(spec, "mylib", "")
	// Check that per-method exports and getter bodies are generated.
	for _, want := range []string{
		"WASM_EXPORT(w_0_", // at least one method/getter export for the Foo service
		"GetCount",         // field getter
		"GetLabel",         // field getter
		"Value",            // method
		"SetName",          // method
		"_self->",          // method calls
	} {
		if !strings.Contains(output, want) {
			t.Errorf("expected %q in bridge output", want)
		}
	}
}

// TestGenerateBridge_WithOutputParams exercises writeFieldExpr for
// output parameters.
func TestGenerateBridge_WithOutputParams(t *testing.T) {
	src := "mylib/foo.h"
	spec := &apispec.APISpec{
		Namespace: "mylib",
		Functions: []apispec.Function{
			{
				Name:     "GetValue",
				QualName: "mylib::GetValue",
				Params: []apispec.Param{
					// Output param: bool*
					{
						Name: "out_valid",
						Type: apispec.TypeRef{
							Kind:      apispec.TypePrimitive,
							Name:      "bool",
							IsPointer: true,
							QualType:  "bool *",
						},
					},
				},
				ReturnType: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"},
				SourceFile: src,
			},
		},
	}
	output := GenerateBridge(spec, "mylib", "")
	// Output param should be in the response
	if !strings.Contains(output, "out_") {
		t.Error("expected out_valid in bridge output")
	}
}

// TestGenerateBridge_AbstractClass tests the abstract class path where
// constructors are skipped and getter-only class members are exposed.
func TestGenerateBridge_AbstractClass(t *testing.T) {
	src := "mylib/foo.h"
	spec := &apispec.APISpec{
		Namespace: "mylib",
		Classes: []apispec.Class{
			{
				Name:          "Shape",
				QualName:      "mylib::Shape",
				Namespace:     "mylib",
				IsHandle:      true,
				IsAbstract:    true,
				HasPublicDtor: true,
				Methods: []apispec.Function{
					// Virtual method
					{
						Name:       "Area",
						QualName:   "mylib::Shape::Area",
						IsVirtual:  true,
						ReturnType: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "double"},
						IsConst:    true,
						SourceFile: src,
					},
				},
				SourceFile: src,
			},
			{
				Name:                 "Circle",
				QualName:             "mylib::Circle",
				Namespace:            "mylib",
				IsHandle:             true,
				HasPublicDefaultCtor: true,
				HasPublicDtor:        true,
				Parent:               "mylib::Shape",
				Methods: []apispec.Function{
					{
						Name:       "Area",
						QualName:   "mylib::Circle::Area",
						IsVirtual:  true,
						ReturnType: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "double"},
						IsConst:    true,
						SourceFile: src,
					},
				},
				SourceFile: src,
			},
		},
	}
	output := GenerateBridge(spec, "mylib", "")
	// Service IDs follow proto.go's alphabetical class order:
	// Circle (0) → Shape (1). Abstract Shape has no constructor
	// export but does emit Area; concrete Circle has constructor +
	// methods + Free.
	if !strings.Contains(output, "// Service 0: CircleService") {
		t.Error("expected Circle service header")
	}
	if !strings.Contains(output, "// Service 1: ShapeService") {
		t.Error("expected Shape service header")
	}
	if !strings.Contains(output, "WASM_EXPORT(w_0_0)") || !strings.Contains(output, "WASM_EXPORT(w_1_0)") {
		t.Error("expected w_0_0 (Circle ctor) and w_1_0 (Shape::Area) exports")
	}
	// Downcast API is intentionally NOT emitted. Go type assertion
	// replaces the ToCircle RPC. The generator must therefore NOT
	// emit a dispatch case that calls dynamic_cast<Circle>.
	if strings.Contains(output, "ToCircle") {
		t.Error("Generator must not emit ToCircle downcast — Go type assertion is the idiom")
	}
	if strings.Contains(output, "dynamic_cast") {
		t.Error("Generator must not emit dynamic_cast in the bridge; downcast is a Go-side assertion")
	}
}

// TestHandleMapType_MapEntry exercises handleMapType's non-native-map
// code path: when the key is not a valid proto map key, a wrapper
// message gets generated.
func TestHandleMapType_MapEntry(t *testing.T) {
	prev := wrapperMessages
	prevMsg := messageNameMap
	wrapperMessages = make(map[string]string)
	messageNameMap = map[string]string{"ns::Foo": "Foo"}
	defer func() {
		wrapperMessages = prev
		messageNameMap = prevMsg
	}()

	// A map<Foo, int> where key Foo is a message type (not a proto key type)
	// → generates a wrapper with wasm_map_type option
	got := handleMapType("", "Foo", "int")
	// Should produce "repeated FooToInt32Entry" (wrapper message)
	if !strings.Contains(got, "repeated") {
		t.Errorf("expected repeated wrapper, got %q", got)
	}
	// Verify wrapper was registered
	foundEntry := false
	for name := range wrapperMessages {
		if strings.Contains(name, "Entry") {
			foundEntry = true
		}
	}
	if !foundEntry {
		t.Error("expected wrapper Entry message registered")
	}
}

// TestGenerateBridge_WithVectorValueReturn exercises writeValueReturnExpr
// for a vector of value types.
// TestGenerateProto_CallbackOutputOwnership pins down a regression-
// prone distinction in the callback-service emitter:
//
//   - C++ output param shaped as `unique_ptr<T>*` / `shared_ptr<T>*`
//     transfers ownership to the C++ side (the trampoline wraps the
//     wire-decoded raw pointer in a fresh smart pointer and writes
//     through to the caller's slot, so its destructor will eventually
//     delete the underlying object). The Go callback adapter must
//     `clearPtr()` the returned handle so its finalizer becomes a
//     no-op and the same address is not double-freed via the wrapper.
//
//   - C++ output param shaped as `T**` is a borrowed handle: the
//     callee just hands back a pointer it does not own. The Go
//     callback retains ownership; calling `clearPtr()` on this
//     wrapper would orphan the allocation (Go finalizer no longer
//     runs Free, and C++ does not delete either).
//
// The bridge communicates the distinction to the plugin via the
// `wasmify.wasm_take_ownership` field option on the response field.
// This test exercises both shapes on the same callback class to
// verify the option is set selectively.
//
// Why this matters: prior to the fix, `clearPtr()` was emitted
// unconditionally for any `case f.isHandle` callback return. The
// borrowed case (e.g. `Catalog::FindPropertyGraph`'s `T**` output)
// would silently lose the Go-side pointer after the first invoke,
// returning a NULL handle on subsequent invokes — eventually
// corrupting the wasm allocator on a different code path that
// dereferenced the now-stale slot. The user-visible symptom was a
// flaky `out of bounds memory access` trap a few iterations into a
// callback-driven test loop, with the crash site disjoint from the
// double-free origin.
func TestGenerateProto_CallbackOutputOwnership(t *testing.T) {
	src := "mylib/cb.h"
	spec := &apispec.APISpec{
		Namespace: "mylib",
		Classes: []apispec.Class{
			{
				Name: "Item", QualName: "mylib::Item",
				Namespace: "mylib", IsHandle: true, HasPublicDtor: true,
				HasPublicDefaultCtor: true, SourceFile: src,
			},
			{
				Name: "Finder", QualName: "mylib::Finder",
				Namespace: "mylib", IsHandle: true, IsAbstract: true,
				HasPublicDefaultCtor: true,
				HasPublicDtor: true, SourceFile: src,
				Methods: []apispec.Function{
					// Borrowed output: const T**.
					{
						Name: "FindBorrowed", QualName: "mylib::Finder::FindBorrowed",
						IsVirtual: true, IsPureVirtual: true,
						SourceFile: src,
						Params: []apispec.Param{{
							Name: "out",
							Type: apispec.TypeRef{
								Kind: apispec.TypeHandle, Name: "mylib::Item",
								IsPointer: true, IsConst: true,
								QualType: "const mylib::Item **",
							},
						}},
						ReturnType: apispec.TypeRef{Kind: apispec.TypeVoid},
					},
					// Owning output: unique_ptr<T>*.
					{
						Name: "FindOwning", QualName: "mylib::Finder::FindOwning",
						IsVirtual: true, IsPureVirtual: true,
						SourceFile: src,
						Params: []apispec.Param{{
							Name: "out",
							Type: apispec.TypeRef{
								Kind: apispec.TypeHandle, Name: "mylib::Item",
								IsPointer: true,
								QualType: "std::unique_ptr<mylib::Item> *",
							},
						}},
						ReturnType: apispec.TypeRef{Kind: apispec.TypeVoid},
					},
				},
			},
		},
	}
	output := GenerateProto(spec, "mylib")
	if !strings.Contains(output, "rpc FindBorrowed") {
		t.Fatalf("expected FindBorrowed RPC in output\n%s", output)
	}
	if !strings.Contains(output, "rpc FindOwning") {
		t.Fatalf("expected FindOwning RPC in output\n%s", output)
	}
	// Locate the response messages for each RPC.
	borrowedResp := extractMessage(t, output, "FinderCallbackFindBorrowedResponse")
	owningResp := extractMessage(t, output, "FinderCallbackFindOwningResponse")
	if strings.Contains(borrowedResp, "wasm_take_ownership") {
		t.Errorf("borrowed (T**) output must NOT carry wasm_take_ownership:\n%s", borrowedResp)
	}
	if !strings.Contains(owningResp, "wasm_take_ownership") {
		t.Errorf("owning (unique_ptr<T>*) output must carry wasm_take_ownership:\n%s", owningResp)
	}
}

// extractMessage returns the body of the proto message named `name`
// from a fully-rendered proto schema string. Fails the test with a
// descriptive error if the message is missing.
func extractMessage(t *testing.T, schema, name string) string {
	t.Helper()
	hdr := "message " + name + " {"
	idx := strings.Index(schema, hdr)
	if idx < 0 {
		t.Fatalf("message %q not in schema", name)
	}
	rest := schema[idx:]
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		t.Fatalf("message %q has no closing brace", name)
	}
	return rest[:end+3]
}

// TestGenerateBridge_ProtoWriterFreesData pins the runtime invariant
// that ProtoWriter cleans up its lazily-allocated heap buffer in
// every usage shape:
//
//   - Wasm export return: the per-method handler ends with
//     `return _pw.finish();`. finish() must transfer ownership of
//     `data_` to the host (the host calls wasm_free after reading).
//     The fix shape is for finish() to NULL out data_ before
//     returning, so the destructor sees nullptr and skips the free.
//
//   - Callback trampoline (C++ -> Go): the trampoline writes a
//     request via _pw, hands `_pw.data_, _pw.size_` to
//     wasmify_callback_invoke (which READS the buffer; host does
//     not take ownership), then `_pw` goes out of scope. Without a
//     destructor that frees data_, every callback dispatch leaks
//     128+ bytes of wasm heap. Across a stress loop of callback-
//     driven analysis, dlmalloc fragments / runs out of arena and
//     subsequent allocations either return NULL or trip an OOB
//     fault inside the allocator's freelist walk — the user-
//     visible "out of bounds memory access" several iterations
//     later in an unrelated path.
//
// This test asserts:
//   1. ProtoWriter's class definition includes a destructor that
//      frees data_.
//   2. finish() releases the buffer (transfers ownership) before
//      returning, evidenced by the data_ NULL-out idiom.
//
// Both are properties of the runtime helper preamble emitted by
// GenerateBridge (independent of any specific class spec), so a
// minimal spec drives the rendering.
func TestGenerateBridge_ProtoWriterFreesData(t *testing.T) {
	spec := &apispec.APISpec{
		Namespace: "mylib",
		Functions: []apispec.Function{
			{Name: "Noop", QualName: "mylib::Noop",
				ReturnType: apispec.TypeRef{Kind: apispec.TypeVoid},
				SourceFile: "mylib/foo.h"},
		},
	}
	output := GenerateBridge(spec, "mylib", "")

	// The destructor must fire on every ProtoWriter going out of
	// scope. A simple `~ProtoWriter()` substring is enough — the
	// preamble has only one ProtoWriter class.
	if !strings.Contains(output, "~ProtoWriter()") {
		t.Errorf("ProtoWriter must declare a destructor to free data_; bridge preamble missing ~ProtoWriter()")
	}
	// Inside the destructor, data_ must be freed.
	dtor := extractClassMember(t, output, "~ProtoWriter()")
	if !strings.Contains(dtor, "free(data_)") {
		t.Errorf("ProtoWriter destructor must call free(data_); got:\n%s", dtor)
	}
	// finish() must NULL out data_ before returning so the dtor
	// does not double-free what the host now owns.
	finishBody := extractClassMember(t, output, "int64_t finish()")
	if !strings.Contains(finishBody, "data_ = nullptr") {
		t.Errorf("finish() must NULL data_ before return so the dtor does not double-free:\n%s", finishBody)
	}
	if !strings.Contains(finishBody, "encode_result(") {
		t.Errorf("finish() must still encode the result; got:\n%s", finishBody)
	}
}

// extractClassMember returns the source from a member-function
// header up to and including the next closing brace at column 4
// (the C++ generator's per-class indentation). Used to bound the
// substring checks above to a single member's body.
func extractClassMember(t *testing.T, src, header string) string {
	t.Helper()
	idx := strings.Index(src, header)
	if idx < 0 {
		t.Fatalf("class member %q not found", header)
	}
	rest := src[idx:]
	// Closing brace at column 4 ends a method indented at column 4.
	end := strings.Index(rest, "\n    }\n")
	if end < 0 {
		t.Fatalf("class member %q has no closing brace", header)
	}
	return rest[:end]
}

// TestGenerateProto_ConstructorTakesOwnershipAnnotation pins the
// rule that a constructor parameter typed `std::unique_ptr<T>` (by
// value) carries the `wasm_take_ownership` field option in the
// generated proto. The ctor-request emission path used to bypass
// the writeRequestField helper that the free-function and method
// paths already routed through, so unique_ptr ctor args silently
// lost the annotation. The plugin then never emitted clearPtr on
// the Go-side wrapper, so its per-instance finalizer raced the
// C++ destructor and double-freed the same address through the
// wasm allocator — surfacing as a delayed `out of bounds memory
// access` from a completely unrelated call path several
// iterations later.
//
// This test asserts the annotation reaches the wire schema; the
// plugin's clearPtr emit is already tested separately
// (emitOwnershipTransfer is unconditional once takesOwnership is
// set on the field).
func TestGenerateProto_ConstructorTakesOwnershipAnnotation(t *testing.T) {
	src := "mylib/foo.h"
	spec := &apispec.APISpec{
		Namespace: "mylib",
		Classes: []apispec.Class{
			{
				Name: "Inner", QualName: "mylib::Inner",
				Namespace: "mylib", IsHandle: true, HasPublicDtor: true,
				HasPublicDefaultCtor: true, SourceFile: src,
			},
			{
				Name: "Outer", QualName: "mylib::Outer",
				Namespace: "mylib", IsHandle: true, HasPublicDtor: true,
				SourceFile: src,
				Methods: []apispec.Function{
					{
						Name: "Outer", QualName: "mylib::Outer::Outer",
						IsConstructor: true, SourceFile: src,
						Params: []apispec.Param{
							{
								Name: "owned",
								Type: apispec.TypeRef{
									Kind: apispec.TypeHandle, Name: "mylib::Inner",
									QualType: "std::unique_ptr<mylib::Inner>",
								},
							},
							{
								// Borrowed: raw pointer, NO ownership transfer.
								Name: "borrowed",
								Type: apispec.TypeRef{
									Kind: apispec.TypeHandle, Name: "mylib::Inner",
									IsPointer: true, IsConst: true,
									QualType: "const mylib::Inner *",
								},
							},
						},
						ReturnType: apispec.TypeRef{Kind: apispec.TypeVoid},
					},
				},
			},
		},
	}
	output := GenerateProto(spec, "mylib")
	req := extractMessage(t, output, "OuterNewRequest")
	// owned (unique_ptr) MUST carry the annotation.
	if !strings.Contains(req, "owned = 1 [(wasmify.wasm_take_ownership) = true]") {
		t.Errorf("unique_ptr ctor param must carry wasm_take_ownership; got:\n%s", req)
	}
	// borrowed (raw pointer) MUST NOT carry the annotation.
	if strings.Contains(req, "borrowed = 2 [(wasmify.wasm_take_ownership)") {
		t.Errorf("raw-pointer ctor param must NOT carry wasm_take_ownership (it would orphan the allocation); got:\n%s", req)
	}
}

// TestGenerateProto_OwnershipTransferMethodsConfig pins the
// project-level escape hatch for C++ APIs that consume a raw `T*`
// parameter into a smart pointer inside the .cc body (e.g. via
// `absl::WrapUnique`). The generator does not detect this from
// names or types alone; the user lists the qualified method in
// `bridge.OwnershipTransferMethods`, and every handle parameter
// then carries `wasm_take_ownership` on the wire schema. The
// plugin's existing emitOwnershipTransfer pass does the rest.
//
// Listing is by FULLY QUALIFIED C++ method name; bare-name match
// would catch unrelated methods that happen to share a short name.
func TestGenerateProto_OwnershipTransferMethodsConfig(t *testing.T) {
	src := "mylib/foo.h"
	spec := &apispec.APISpec{
		Namespace: "mylib",
		Classes: []apispec.Class{
			{
				Name: "Item", QualName: "mylib::Item",
				Namespace: "mylib", IsHandle: true, HasPublicDtor: true,
				HasPublicDefaultCtor: true, SourceFile: src,
			},
			{
				Name: "Catalog", QualName: "mylib::Catalog",
				Namespace: "mylib", IsHandle: true, HasPublicDtor: true,
				HasPublicDefaultCtor: true, SourceFile: src,
				Methods: []apispec.Function{
					{
						Name:     "AddOwnedItem",
						QualName: "mylib::Catalog::AddOwnedItem",
						SourceFile: src,
						Params: []apispec.Param{{
							Name: "item",
							Type: apispec.TypeRef{
								Kind: apispec.TypeHandle, Name: "mylib::Item",
								IsPointer: true,
								QualType:  "mylib::Item *",
							},
						}},
						ReturnType: apispec.TypeRef{Kind: apispec.TypeVoid},
					},
					{
						Name:     "AddBorrowedItem",
						QualName: "mylib::Catalog::AddBorrowedItem",
						SourceFile: src,
						Params: []apispec.Param{{
							Name: "item",
							Type: apispec.TypeRef{
								Kind: apispec.TypeHandle, Name: "mylib::Item",
								IsPointer: true,
								QualType:  "mylib::Item *",
							},
						}},
						ReturnType: apispec.TypeRef{Kind: apispec.TypeVoid},
					},
				},
			},
		},
	}

	cfg := BridgeConfig{
		OwnershipTransferMethods: []state.OwnershipTransferEntry{
			{Method: "mylib::Catalog::AddOwnedItem"},
		},
	}
	output := GenerateProtoWithConfig(spec, "mylib", cfg)

	owned := extractMessage(t, output, "CatalogAddOwnedItemRequest")
	if !strings.Contains(owned, "wasm_take_ownership") {
		t.Errorf("AddOwnedItem listed in OwnershipTransferMethods MUST carry wasm_take_ownership; got:\n%s", owned)
	}
	borrowed := extractMessage(t, output, "CatalogAddBorrowedItemRequest")
	if strings.Contains(borrowed, "wasm_take_ownership") {
		t.Errorf("AddBorrowedItem NOT listed must NOT carry wasm_take_ownership; got:\n%s", borrowed)
	}
}

// TestGenerateProto_ConditionalOwnershipTransferConfig pins the
// runtime-selector variant for the C++ idiom
// `f(T*, bool is_owned)`. The proto schema cannot mark the
// handle field as unconditionally ownership-transferring (passing
// is_owned=false is a legitimate borrowed-pass-through), so the
// plugin emits a runtime guard
// `if <selector> { handle.clearPtr() }` driven by the
// `wasm_take_ownership_when` field option. Project-level opt-in
// via `bridge.ConditionalOwnershipTransferMethods`.
func TestGenerateProto_ConditionalOwnershipTransferConfig(t *testing.T) {
	src := "mylib/foo.h"
	spec := &apispec.APISpec{
		Namespace: "mylib",
		Classes: []apispec.Class{
			{
				Name: "Item", QualName: "mylib::Item",
				Namespace: "mylib", IsHandle: true, HasPublicDtor: true,
				HasPublicDefaultCtor: true, SourceFile: src,
			},
			{
				Name: "Container", QualName: "mylib::Container",
				Namespace: "mylib", IsHandle: true, HasPublicDtor: true,
				HasPublicDefaultCtor: true, SourceFile: src,
				Methods: []apispec.Function{
					{
						Name: "AddItem", QualName: "mylib::Container::AddItem",
						SourceFile: src,
						Params: []apispec.Param{
							{
								Name: "item",
								Type: apispec.TypeRef{
									Kind: apispec.TypeHandle, Name: "mylib::Item",
									IsPointer: true,
									QualType:  "mylib::Item *",
								},
							},
							{
								Name: "is_owned",
								Type: apispec.TypeRef{
									Kind: apispec.TypePrimitive, Name: "bool",
									QualType: "bool",
								},
							},
						},
						ReturnType: apispec.TypeRef{Kind: apispec.TypeVoid},
					},
				},
			},
		},
	}

	cfg := BridgeConfig{
		OwnershipTransferMethods: []state.OwnershipTransferEntry{{
			Method:    "mylib::Container::AddItem",
			Signature: []string{"mylib::Item *", "bool"},
			TransferWhen: &state.TransferWhenSpec{
				Param:  "is_owned",
				Equals: true,
			},
		}},
	}
	output := GenerateProtoWithConfig(spec, "mylib", cfg)
	req := extractMessage(t, output, "ContainerAddItemRequest")
	if !strings.Contains(req, `wasm_take_ownership_when) = "is_owned"`) {
		t.Errorf("conditional ownership marker missing from request:\n%s", req)
	}
	if !strings.Contains(req, `wasm_take_ownership_equals) = "true"`) {
		t.Errorf("conditional ownership equals literal missing from request:\n%s", req)
	}
	if strings.Contains(req, "wasm_take_ownership) = true") {
		t.Errorf("conditional ownership MUST NOT also emit unconditional take_ownership:\n%s", req)
	}
}

// TestGenerateProto_ConditionalOwnershipTransfer_UnqualifiedSignature
// pins the namespace-tolerant signature match. clang emits qual_types
// fully-qualified ("mylib::Item *"); users typically list overloads
// in `wasmify.json::OwnershipTransferMethods` with unqualified type
// names ("Item *"). Before the fix in `qualTypeMatches`, the byte-
// for-byte compare in `signatureMatches` rejected the user's entry
// whenever the namespace prefix was elided, the
// `wasm_take_ownership_when` annotation never reached the proto, and
// the Go-side wrapper silently retained `ptr` after `is_owned=true`
// — surfacing as a double-free at GC time when the parent's
// destructor reclaimed the same wasm-side memory.
//
// This regression test mirrors the
// `googlesql::SimpleTable::AddColumn(const Column*, bool)` case
// downstream.
func TestGenerateProto_ConditionalOwnershipTransfer_UnqualifiedSignature(t *testing.T) {
	src := "mylib/foo.h"
	spec := &apispec.APISpec{
		Namespace: "mylib",
		Classes: []apispec.Class{
			{
				Name: "Item", QualName: "mylib::Item",
				Namespace: "mylib", IsHandle: true, HasPublicDtor: true,
				HasPublicDefaultCtor: true, SourceFile: src,
			},
			{
				Name: "Container", QualName: "mylib::Container",
				Namespace: "mylib", IsHandle: true, HasPublicDtor: true,
				HasPublicDefaultCtor: true, SourceFile: src,
				Methods: []apispec.Function{
					{
						Name: "AddItem", QualName: "mylib::Container::AddItem",
						SourceFile: src,
						Params: []apispec.Param{
							{
								Name: "item",
								Type: apispec.TypeRef{
									Kind: apispec.TypeHandle, Name: "mylib::Item",
									IsPointer: true,
									QualType:  "const mylib::Item *",
								},
							},
							{
								Name: "is_owned",
								Type: apispec.TypeRef{
									Kind: apispec.TypePrimitive, Name: "bool",
									QualType: "bool",
								},
							},
						},
						ReturnType: apispec.TypeRef{Kind: apispec.TypeVoid},
					},
				},
			},
		},
	}

	cfg := BridgeConfig{
		// Unqualified signature — the user's natural spelling.
		// Before the namespace-tolerant fix in qualTypeMatches this
		// failed to match and no annotation was emitted.
		OwnershipTransferMethods: []state.OwnershipTransferEntry{{
			Method:    "mylib::Container::AddItem",
			Signature: []string{"const Item *", "bool"},
			TransferWhen: &state.TransferWhenSpec{
				Param:  "is_owned",
				Equals: true,
			},
		}},
	}
	output := GenerateProtoWithConfig(spec, "mylib", cfg)
	req := extractMessage(t, output, "ContainerAddItemRequest")
	if !strings.Contains(req, `wasm_take_ownership_when) = "is_owned"`) {
		t.Errorf("unqualified signature must still trigger conditional take_ownership annotation; got:\n%s", req)
	}
	if strings.Contains(req, "wasm_take_ownership) = true") {
		t.Errorf("unqualified signature must NOT degrade to unconditional take_ownership:\n%s", req)
	}
}

// TestGenerateProto_VectorOfUniquePtrTakesOwnership pins the rule
// that a `std::vector<std::unique_ptr<T>>` parameter (constructor
// or method) carries `wasm_take_ownership` on the proto field.
// The bridge body wraps each wire-decoded raw pointer in a fresh
// std::unique_ptr at emplace_back time, so the C++ side takes
// ownership of every element. Without the annotation, the plugin
// does not emit the per-element clearPtr loop, the Go-side
// wrappers retain their `ptr`, and each element double-frees
// when its finaliser later runs against memory the C++
// destructor has already reclaimed.
//
// SimpleGraphNodeTable / SimpleGraphEdgeTable's
// `propertyDefinitions` parameter is the load-bearing example;
// dropping the annotation here was the second-largest source of
// the cumulative-leak crash that surfaced as a delayed
// out-of-bounds memory access after a few hundred test runs.
func TestGenerateProto_VectorOfUniquePtrTakesOwnership(t *testing.T) {
	src := "mylib/foo.h"
	spec := &apispec.APISpec{
		Namespace: "mylib",
		Classes: []apispec.Class{
			{
				Name: "Item", QualName: "mylib::Item",
				Namespace: "mylib", IsHandle: true, HasPublicDtor: true,
				HasPublicDefaultCtor: true, SourceFile: src,
			},
			{
				Name: "Container", QualName: "mylib::Container",
				Namespace: "mylib", IsHandle: true, HasPublicDtor: true,
				SourceFile: src,
				Methods: []apispec.Function{
					{
						Name: "Container", QualName: "mylib::Container::Container",
						IsConstructor: true, SourceFile: src,
						Params: []apispec.Param{{
							Name: "items",
							Type: apispec.TypeRef{
								Kind:     apispec.TypeVector,
								QualType: "std::vector<std::unique_ptr<mylib::Item>>",
								Inner: &apispec.TypeRef{
									Kind: apispec.TypeHandle, Name: "mylib::Item",
									QualType: "std::unique_ptr<mylib::Item>",
								},
							},
						}},
						ReturnType: apispec.TypeRef{Kind: apispec.TypeVoid},
					},
				},
			},
		},
	}
	output := GenerateProto(spec, "mylib")
	req := extractMessage(t, output, "ContainerNewRequest")
	if !strings.Contains(req, "items = 1 [(wasmify.wasm_take_ownership) = true]") {
		t.Errorf("vector<unique_ptr<T>> ctor param must carry wasm_take_ownership; got:\n%s", req)
	}
}

func TestGenerateBridge_VectorOfValue(t *testing.T) {
	spec := &apispec.APISpec{
		Namespace: "mylib",
		Functions: []apispec.Function{
			{
				Name:     "GetItems",
				QualName: "mylib::GetItems",
				ReturnType: apispec.TypeRef{
					Kind:  apispec.TypeVector,
					Inner: &apispec.TypeRef{Kind: apispec.TypeValue, Name: "mylib::Item"},
				},
				SourceFile: "mylib/foo.h",
			},
		},
		Classes: []apispec.Class{
			{
				Name:                 "Item",
				QualName:             "mylib::Item",
				Namespace:            "mylib",
				HasPublicDefaultCtor: true,
				HasPublicDtor:        true,
				Fields: []apispec.Field{
					{Name: "id", Type: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"}},
					{Name: "label", Type: apispec.TypeRef{Kind: apispec.TypeString}},
				},
				SourceFile: "mylib/foo.h",
			},
		},
	}
	output := GenerateBridge(spec, "mylib", "")
	// Vector<Item> serializes each item as a submessage
	if !strings.Contains(output, "_subw") {
		t.Error("expected sub writer (_subw) in vector<value> path")
	}
}

// TestGenerateProto_ExternalParentChain_EmitsServiceForParents pins the
// gen-proto level mirror of clangast's transitive parent admission.
//
// The user's `bridge.ExternalTypes` typically lists only the leaf
// classes a project cares about (e.g. `google::protobuf::FileDescriptorProto`).
// The clang-parser stage pulls the leaf's parent chain (`Message`,
// `MessageLite`) into api-spec via
// expandAllowedExternalClassesTransitively. But proto-gen's
// `isBridgeableClass` filter consults `bridgeConfig.ExternalTypes`
// directly, NOT the api-spec admission flag — so without a matching
// expansion at this stage, the parents are dropped at gen-proto time
// and no `service` is emitted for them. The downstream
// protoc-gen-wasmify-go then has nothing to attach as inherited
// methods; the Go base struct stays method-less and the embedding
// chain in the leaf class is decorative only.
//
// expandExternalTypesByParentChain re-applies the parser's parent
// closure at proto-gen entry, so the resulting generated proto file
// has services for the entire admitted parent chain. This test
// verifies that contract end-to-end.
func TestGenerateProto_ExternalParentChain_EmitsServiceForParents(t *testing.T) {
	src := "external/lib/foo.h"
	spec := &apispec.APISpec{
		Namespace: "myproj",
		Classes: []apispec.Class{
			// Project-side class — admitted unconditionally.
			{
				Name: "App", QualName: "myproj::App",
				Namespace: "myproj", IsHandle: true, HasPublicDtor: true,
				HasPublicDefaultCtor: true, SourceFile: "myproj/app.h",
			},
			// External 3-level inheritance: Leaf : Mid : Root.
			{
				Name: "Root", QualName: "lib::Root",
				Namespace: "lib", IsHandle: true, HasPublicDtor: true,
				IsAbstract: true, // mirrors protobuf::MessageLite
				SourceFile: src,
				Methods: []apispec.Function{
					{
						Name:     "ParseFromString",
						QualName: "lib::Root::ParseFromString",
						SourceFile: src,
						Params: []apispec.Param{
							{Name: "data", Type: apispec.TypeRef{Kind: apispec.TypeString, Name: "string", QualType: "const std::string &"}},
						},
						ReturnType: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "bool", QualType: "bool"},
						Access:     "public",
					},
				},
			},
			{
				Name: "Mid", QualName: "lib::Mid",
				Namespace: "lib", IsHandle: true, HasPublicDtor: true,
				IsAbstract: true, // mirrors protobuf::Message
				SourceFile: src,
				Parent:     "lib::Root",
				Parents:    []string{"lib::Root"},
				Methods: []apispec.Function{
					{
						Name:     "Clear",
						QualName: "lib::Mid::Clear",
						SourceFile: src,
						ReturnType: apispec.TypeRef{Kind: apispec.TypeVoid},
						Access:     "public",
					},
				},
			},
			{
				Name: "Leaf", QualName: "lib::Leaf",
				Namespace: "lib", IsHandle: true, HasPublicDtor: true,
				HasPublicDefaultCtor: true, SourceFile: src,
				Parent:     "lib::Mid",
				Parents:    []string{"lib::Mid"},
			},
		},
	}

	// User's wasmify.json admits ONLY the leaf — the parent chain is
	// expected to be admitted transitively at gen-proto entry.
	cfg := BridgeConfig{
		ExternalTypes: []string{"lib::Leaf"},
	}
	output := GenerateProtoWithConfig(spec, "myproj", cfg)

	// All three classes must show up as proto messages and as
	// services — Leaf via direct admission, Mid+Root via the
	// expansion.
	for _, want := range []string{"Leaf", "Mid", "Root"} {
		if !strings.Contains(output, "message "+want+" {") {
			t.Errorf("expected `message %s {` in output (transitive parent admission)", want)
		}
		if !strings.Contains(output, "service "+want+"Service {") {
			t.Errorf("expected `service %sService {` in output — without it, protoc-gen-wasmify-go has no inherited methods to promote", want)
		}
	}
	// Sanity: Root's ParseFromString RPC reaches the proto file. The
	// embedding chain in Go-codegen will surface it on every
	// transitive subclass.
	if !strings.Contains(output, "rpc ParseFromString(") {
		t.Error("Root's ParseFromString RPC must be emitted in the parent service so the embedding chain can promote it")
	}
}

// TestGenerateProto_TransferWhenIntSelector pins the int-typed
// runtime selector path. The bridge should emit
// `wasm_take_ownership_when = "<param>"` plus
// `wasm_take_ownership_equals = "<integer>"` so the plugin
// downstream renders `if <param> == <integer> { clearPtrAny(...) }`.
//
// Mirrors the bool-selector test for type coverage; covers
// hypothetical APIs like `Container::Add(T*, int mode)` where a
// specific int sentinel value (e.g. mode == 1) flips ownership.
func TestGenerateProto_TransferWhenIntSelector(t *testing.T) {
	src := "mylib/foo.h"
	spec := &apispec.APISpec{
		Namespace: "mylib",
		Classes: []apispec.Class{
			{
				Name: "Item", QualName: "mylib::Item",
				Namespace: "mylib", IsHandle: true, HasPublicDtor: true,
				HasPublicDefaultCtor: true, SourceFile: src,
			},
			{
				Name: "Container", QualName: "mylib::Container",
				Namespace: "mylib", IsHandle: true, HasPublicDtor: true,
				HasPublicDefaultCtor: true, SourceFile: src,
				Methods: []apispec.Function{
					{
						Name: "Add", QualName: "mylib::Container::Add",
						SourceFile: src,
						Params: []apispec.Param{
							{
								Name: "item",
								Type: apispec.TypeRef{
									Kind: apispec.TypeHandle, Name: "mylib::Item",
									IsPointer: true, QualType: "const mylib::Item *",
								},
							},
							{
								Name: "mode",
								Type: apispec.TypeRef{
									Kind: apispec.TypePrimitive, Name: "int", QualType: "int",
								},
							},
						},
						ReturnType: apispec.TypeRef{Kind: apispec.TypeVoid},
					},
				},
			},
		},
	}
	cfg := BridgeConfig{
		OwnershipTransferMethods: []state.OwnershipTransferEntry{{
			Method:    "mylib::Container::Add",
			Signature: []string{"const Item *", "int"},
			TransferWhen: &state.TransferWhenSpec{
				Param:  "mode",
				Equals: float64(1), // JSON numbers decode as float64
			},
		}},
	}
	output := GenerateProtoWithConfig(spec, "mylib", cfg)
	req := extractMessage(t, output, "ContainerAddRequest")
	if !strings.Contains(req, `wasm_take_ownership_when) = "mode"`) {
		t.Errorf("expected selector annotation:\n%s", req)
	}
	if !strings.Contains(req, `wasm_take_ownership_equals) = "1"`) {
		t.Errorf("expected int equals=1 annotation:\n%s", req)
	}
}

// TestGenerateProto_TransferWhenStringSelector pins the string-typed
// runtime selector. The equals literal is JSON-quoted so the plugin
// can safely embed it into a Go `==` comparison.
func TestGenerateProto_TransferWhenStringSelector(t *testing.T) {
	src := "mylib/foo.h"
	spec := &apispec.APISpec{
		Namespace: "mylib",
		Classes: []apispec.Class{
			{
				Name: "Item", QualName: "mylib::Item",
				Namespace: "mylib", IsHandle: true, HasPublicDtor: true,
				HasPublicDefaultCtor: true, SourceFile: src,
			},
			{
				Name: "Container", QualName: "mylib::Container",
				Namespace: "mylib", IsHandle: true, HasPublicDtor: true,
				HasPublicDefaultCtor: true, SourceFile: src,
				Methods: []apispec.Function{
					{
						Name: "Insert", QualName: "mylib::Container::Insert",
						SourceFile: src,
						Params: []apispec.Param{
							{
								Name: "item",
								Type: apispec.TypeRef{
									Kind: apispec.TypeHandle, Name: "mylib::Item",
									IsPointer: true, QualType: "const mylib::Item *",
								},
							},
							{
								Name: "policy",
								Type: apispec.TypeRef{
									Kind: apispec.TypeString, Name: "string",
									QualType: "absl::string_view",
								},
							},
						},
						ReturnType: apispec.TypeRef{Kind: apispec.TypeVoid},
					},
				},
			},
		},
	}
	cfg := BridgeConfig{
		OwnershipTransferMethods: []state.OwnershipTransferEntry{{
			Method:    "mylib::Container::Insert",
			Signature: []string{"const Item *", "absl::string_view"},
			TransferWhen: &state.TransferWhenSpec{
				Param:  "policy",
				Equals: "transfer",
			},
		}},
	}
	output := GenerateProtoWithConfig(spec, "mylib", cfg)
	req := extractMessage(t, output, "ContainerInsertRequest")
	if !strings.Contains(req, `wasm_take_ownership_when) = "policy"`) {
		t.Errorf("expected selector annotation:\n%s", req)
	}
	if !strings.Contains(req, `wasm_take_ownership_equals) = "\"transfer\""`) {
		t.Errorf("expected string equals=\"transfer\" annotation:\n%s", req)
	}
}

// TestGenerateProto_TransferWhenBoolFalseEquals pins the
// `bool + equals=false` combination — useful for APIs whose flag is
// inverted from the canonical `is_owned` (e.g. `bool no_adopt`).
// The plugin should render `if !<param>` rather than `if <param>`.
func TestGenerateProto_TransferWhenBoolFalseEquals(t *testing.T) {
	src := "mylib/foo.h"
	spec := &apispec.APISpec{
		Namespace: "mylib",
		Classes: []apispec.Class{
			{
				Name: "Item", QualName: "mylib::Item",
				Namespace: "mylib", IsHandle: true, HasPublicDtor: true,
				HasPublicDefaultCtor: true, SourceFile: src,
			},
			{
				Name: "Container", QualName: "mylib::Container",
				Namespace: "mylib", IsHandle: true, HasPublicDtor: true,
				HasPublicDefaultCtor: true, SourceFile: src,
				Methods: []apispec.Function{
					{
						Name: "Push", QualName: "mylib::Container::Push",
						SourceFile: src,
						Params: []apispec.Param{
							{Name: "item", Type: apispec.TypeRef{Kind: apispec.TypeHandle, Name: "mylib::Item", IsPointer: true, QualType: "const mylib::Item *"}},
							{Name: "no_adopt", Type: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "bool", QualType: "bool"}},
						},
						ReturnType: apispec.TypeRef{Kind: apispec.TypeVoid},
					},
				},
			},
		},
	}
	cfg := BridgeConfig{
		OwnershipTransferMethods: []state.OwnershipTransferEntry{{
			Method:    "mylib::Container::Push",
			Signature: []string{"const Item *", "bool"},
			TransferWhen: &state.TransferWhenSpec{
				Param:  "no_adopt",
				Equals: false,
			},
		}},
	}
	output := GenerateProtoWithConfig(spec, "mylib", cfg)
	req := extractMessage(t, output, "ContainerPushRequest")
	if !strings.Contains(req, `wasm_take_ownership_when) = "no_adopt"`) {
		t.Errorf("expected selector annotation:\n%s", req)
	}
	if !strings.Contains(req, `wasm_take_ownership_equals) = "false"`) {
		t.Errorf("expected bool equals=false annotation:\n%s", req)
	}
}

// TestGenerateProto_TransferWhen_NoEntryMeansPattern1 pins the
// behaviour when an OwnershipTransferMethods entry omits
// `transfer_when`. Per the new schema, a missing TransferWhen
// means Pattern 1 (unconditional adoption) — every handle param
// gets `wasm_take_ownership = true`. This was the old default
// before the explicit schema, but used to be flipped by the
// `hasBoolParam` heuristic; now it requires an explicit choice.
func TestGenerateProto_TransferWhen_NoEntryMeansPattern1(t *testing.T) {
	src := "mylib/foo.h"
	spec := &apispec.APISpec{
		Namespace: "mylib",
		Classes: []apispec.Class{
			{
				Name: "Item", QualName: "mylib::Item",
				Namespace: "mylib", IsHandle: true, HasPublicDtor: true,
				HasPublicDefaultCtor: true, SourceFile: src,
			},
			{
				Name: "Container", QualName: "mylib::Container",
				Namespace: "mylib", IsHandle: true, HasPublicDtor: true,
				HasPublicDefaultCtor: true, SourceFile: src,
				Methods: []apispec.Function{
					{
						Name: "Adopt", QualName: "mylib::Container::Adopt",
						SourceFile: src,
						Params: []apispec.Param{
							{Name: "item", Type: apispec.TypeRef{Kind: apispec.TypeHandle, Name: "mylib::Item", IsPointer: true, QualType: "mylib::Item *"}},
							// Has a bool param but the entry has no TransferWhen.
							// Old heuristic would have treated this as Pattern 2;
							// the new explicit schema treats it as Pattern 1.
							{Name: "force", Type: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "bool", QualType: "bool"}},
						},
						ReturnType: apispec.TypeRef{Kind: apispec.TypeVoid},
					},
				},
			},
		},
	}
	cfg := BridgeConfig{
		OwnershipTransferMethods: []state.OwnershipTransferEntry{{
			Method:    "mylib::Container::Adopt",
			Signature: []string{"Item *", "bool"},
			// TransferWhen intentionally nil.
		}},
	}
	output := GenerateProtoWithConfig(spec, "mylib", cfg)
	req := extractMessage(t, output, "ContainerAdoptRequest")
	if !strings.Contains(req, "wasm_take_ownership) = true") {
		t.Errorf("expected unconditional take_ownership for Pattern 1 (no transfer_when):\n%s", req)
	}
	if strings.Contains(req, "wasm_take_ownership_when") {
		t.Errorf("must NOT emit conditional annotation when transfer_when is omitted:\n%s", req)
	}
}
