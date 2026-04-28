package protogen

import (
	"strings"
	"testing"

	"github.com/goccy/wasmify/internal/apispec"
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
