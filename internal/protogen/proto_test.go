package protogen

import (
	"strings"
	"testing"

	"github.com/goccy/wasmify/internal/apispec"
)

func testAPISpec() *apispec.APISpec {
	src := "test/calculator.h"
	return &apispec.APISpec{
		Namespace: "test",
		Functions: []apispec.Function{
			{
				Name:     "add",
				QualName: "test::add",
				Params: []apispec.Param{
					{Name: "a", Type: apispec.TypeRef{Name: "double", Kind: apispec.TypePrimitive}},
					{Name: "b", Type: apispec.TypeRef{Name: "double", Kind: apispec.TypePrimitive}},
				},
				ReturnType: apispec.TypeRef{Name: "double", Kind: apispec.TypePrimitive},
				SourceFile: src,
			},
		},
		Classes: []apispec.Class{
			{
				Name:                 "Calculator",
				QualName:             "test::Calculator",
				Namespace:            "test",
				IsHandle:             true,
				HasPublicDefaultCtor: true,
				HasPublicDtor:        true,
				Parent:               "",
				Methods: []apispec.Function{
					{
						Name:     "compute",
						QualName: "test::Calculator::compute",
						Params: []apispec.Param{
							{Name: "op", Type: apispec.TypeRef{Name: "int", Kind: apispec.TypePrimitive}},
							{Name: "a", Type: apispec.TypeRef{Name: "double", Kind: apispec.TypePrimitive}},
							{Name: "b", Type: apispec.TypeRef{Name: "double", Kind: apispec.TypePrimitive}},
						},
						ReturnType: apispec.TypeRef{Name: "double", Kind: apispec.TypePrimitive},
						SourceFile: src,
					},
				},
				Fields: []apispec.Field{
					{Name: "name", Type: apispec.TypeRef{Name: "string", Kind: apispec.TypeString}},
				},
				SourceFile: src,
			},
			{
				Name:                 "ScientificCalculator",
				QualName:             "test::ScientificCalculator",
				Namespace:            "test",
				IsHandle:             true,
				HasPublicDefaultCtor: true,
				HasPublicDtor:        true,
				Parent:               "test::Calculator",
				Methods: []apispec.Function{
					{
						Name:     "power",
						QualName: "test::ScientificCalculator::power",
						Params: []apispec.Param{
							{Name: "base", Type: apispec.TypeRef{Name: "double", Kind: apispec.TypePrimitive}},
							{Name: "exp", Type: apispec.TypeRef{Name: "double", Kind: apispec.TypePrimitive}},
						},
						ReturnType: apispec.TypeRef{Name: "double", Kind: apispec.TypePrimitive},
						SourceFile: src,
					},
				},
				SourceFile: src,
			},
			{
				Name:                 "Result",
				QualName:             "test::Result",
				Namespace:            "test",
				IsHandle:             false,
				HasPublicDefaultCtor: true,
				HasPublicDtor:        true,
				Fields: []apispec.Field{
					{Name: "value", Type: apispec.TypeRef{Name: "double", Kind: apispec.TypePrimitive}},
					{Name: "ok", Type: apispec.TypeRef{Name: "bool", Kind: apispec.TypePrimitive}},
				},
				SourceFile: src,
			},
		},
		Enums: []apispec.Enum{
			{
				Name:     "Operation",
				QualName: "test::Operation",
				Values: []apispec.EnumValue{
					{Name: "Add", Value: 0},
					{Name: "Subtract", Value: 1},
				},
				SourceFile: src,
			},
		},
	}
}

func TestGenerateProto(t *testing.T) {
	spec := testAPISpec()
	output := GenerateProto(spec, "test")

	checks := []struct {
		name string
		want string
	}{
		{"syntax", `syntax = "proto3"`},
		{"package", "package wasmify.test"},
		{"go_package", `option go_package = "github.com/goccy/wasmify/gen/test"`},
		{"import options", `import "wasmify/options.proto"`},

		// Enum
		{"enum declaration", "enum Operation {"},
		{"enum unspecified", "OPERATION_UNSPECIFIED = 0"},
		{"enum add", "OPERATION_ADD = 1"},
		{"enum subtract", "OPERATION_SUBTRACT = 2"},

		// Handle class: Calculator
		{"calculator message", "message Calculator {"},
		{"calculator wasm_handle", "option (wasmify.wasm_handle) = true"},
		{"calculator ptr field", "uint64 ptr = 1"},

		// Derived handle: ScientificCalculator
		{"sci calc message", "message ScientificCalculator {"},
		{"sci calc wasm_parent", `option (wasmify.wasm_parent) = "Calculator"`},

		// Value struct: Result
		{"result message", "message Result {"},
		{"result value field", "double value = 1"},
		{"result ok field", "bool ok = 2"},

		// Free function service
		{"free func service", "service Test {"},
		{"free func rpc", "rpc Add(AddRequest) returns (AddResponse)"},

		// Free function request/response
		{"add request", "message AddRequest {"},
		{"add request param a", "double a = 1"},
		{"add request param b", "double b = 2"},
		{"add response", "message AddResponse {"},
		{"add response result", "double result = 1"},

		// Calculator service
		{"calculator service", "service CalculatorService {"},
		{"compute rpc", "rpc Compute(CalculatorComputeRequest) returns (CalculatorComputeResponse)"},
		{"get name rpc", "rpc GetName(Calculator) returns (CalculatorGetNameResponse)"},
		{"free rpc", "rpc Free(Calculator) returns (Empty)"},

		// Downcast RPCs are intentionally NOT emitted. Go type
		// assertion handles abstract → concrete conversion without
		// a wasm round-trip. See CLAUDE.md:
		// "do not emit Downcast APIs".

		// Free RPC with option
		{"free option", `option (wasmify.wasm_method_type) = "free"`},

		// Getter option
		{"getter option", `option (wasmify.wasm_method_type) = "getter"`},

		// Compute request/response
		{"compute request", "message CalculatorComputeRequest {"},
		{"compute request handle", "Calculator handle = 1"},
		{"compute request op", "int32 op = 2"},
		{"compute response", "message CalculatorComputeResponse {"},

		// Getter response
		{"get name response", "message CalculatorGetNameResponse {"},
		{"get name value", "string value = 1"},

		// ScientificCalculator service
		{"sci calc service", "service ScientificCalculatorService {"},
		{"power rpc", "rpc Power(ScientificCalculatorPowerRequest) returns (ScientificCalculatorPowerResponse)"},

		// Empty message
		{"empty message", "message Empty {}"},
	}

	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(output, tc.want) {
				t.Errorf("output missing %q\nGot:\n%s", tc.want, output)
			}
		})
	}

	// Result should NOT have ptr field
	t.Run("result no ptr", func(t *testing.T) {
		// Find the Result message block and check it doesn't have ptr
		idx := strings.Index(output, "message Result {")
		if idx < 0 {
			t.Fatal("message Result not found")
		}
		endIdx := strings.Index(output[idx:], "}")
		resultBlock := output[idx : idx+endIdx+1]
		if strings.Contains(resultBlock, "ptr") {
			t.Errorf("Result message should not have ptr field, got:\n%s", resultBlock)
		}
	})

	// Validate proto compiles with protocompile
	t.Run("protocompile validation", func(t *testing.T) {
		if err := ValidateProto(output, "test"); err != nil {
			t.Errorf("generated proto failed validation: %v\nProto:\n%s", err, output)
		}
	})
}

func TestToUpperCamel(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"add", "Add"},
		{"compute_result", "ComputeResult"},
		{"get_name", "GetName"},
		{"hello", "Hello"},
		{"a_b_c", "ABC"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := toUpperCamel(tc.input)
			if got != tc.want {
				t.Errorf("toUpperCamel(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestToSnakeCase(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"hello", "hello"},
		{"HelloWorld", "hello_world"},
		{"getHTTP", "get_http"},
		{"ABC", "abc"},
		{"HTTPServer", "http_server"},
		{"INNER", "inner"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := toSnakeCase(tc.input)
			if got != tc.want {
				t.Errorf("toSnakeCase(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestToScreamingSnake(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Operation", "OPERATION"},
		{"HelloWorld", "HELLO_WORLD"},
		{"add", "ADD"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := toScreamingSnake(tc.input)
			if got != tc.want {
				t.Errorf("toScreamingSnake(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestProtoMessageName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"ResolvedAST", "ResolvedAST"},
		{"zetasql::ResolvedAST", "ResolvedAST"},
		{"zetasql::functions::DateDiff", "DateDiff"},
		{"SomeClass*", "SomeClass"},
		{"SomeClass &", "SomeClass"},
		{"", "Unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := protoMessageName(tc.input)
			if got != tc.want {
				t.Errorf("protoMessageName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestTypeRefToProto(t *testing.T) {
	tests := []struct {
		name string
		ref  apispec.TypeRef
		want string
	}{
		{"double", apispec.TypeRef{Name: "double", Kind: apispec.TypePrimitive}, "double"},
		{"int", apispec.TypeRef{Name: "int", Kind: apispec.TypePrimitive}, "int32"},
		{"bool", apispec.TypeRef{Name: "bool", Kind: apispec.TypePrimitive}, "bool"},
		{"string", apispec.TypeRef{Kind: apispec.TypeString}, "string"},
		{"void", apispec.TypeRef{Kind: apispec.TypeVoid}, "Empty"},
		{"enum", apispec.TypeRef{Name: "test::Operation", Kind: apispec.TypeEnum}, "Operation"},
		{"handle", apispec.TypeRef{Name: "test::Calculator", Kind: apispec.TypeHandle}, "Calculator"},
		{"value", apispec.TypeRef{Name: "test::Result", Kind: apispec.TypeValue}, "Result"},
		{"vector", apispec.TypeRef{
			Kind:  apispec.TypeVector,
			Inner: &apispec.TypeRef{Name: "int", Kind: apispec.TypePrimitive},
		}, "repeated int32"},
		{"vector no inner", apispec.TypeRef{Kind: apispec.TypeVector}, "bytes"},
		{"unknown", apispec.TypeRef{Kind: apispec.TypeUnknown}, "bytes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := typeRefToProto(tc.ref)
			if got != tc.want {
				t.Errorf("typeRefToProto(%v) = %q, want %q", tc.ref, got, tc.want)
			}
		})
	}
}

func TestPrimitiveToPbType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"bool", "bool"},
		{"int", "int32"},
		{"int32_t", "int32"},
		{"unsigned int", "uint32"},
		{"uint32_t", "uint32"},
		{"long", "int64"},
		{"long long", "int64"},
		{"int64_t", "int64"},
		{"size_t", "uint64"},
		{"uint64_t", "uint64"},
		{"short", "int32"},
		{"float", "float"},
		{"double", "double"},
		{"long double", "double"},
		{"char", "int32"},
		{"unsigned char", "uint32"},
		{"unknown_type", "int64"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := primitiveToPbType(tc.input)
			if got != tc.want {
				t.Errorf("primitiveToPbType(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestIsOutputParamPointerToPointer verifies that T** (pointer-to-pointer)
// params are detected as output params, even when the inner pointee is const.
func TestIsOutputParamPointerToPointer(t *testing.T) {
	tests := []struct {
		name string
		p    apispec.Param
		want bool
	}{
		{
			name: "handle T* is INPUT (not output)",
			p: apispec.Param{
				Name: "node",
				Type: apispec.TypeRef{Kind: apispec.TypeHandle, IsPointer: true, QualType: "ASTNode *"},
			},
			want: false,
		},
		{
			name: "T** is output",
			p: apispec.Param{
				Name: "out",
				Type: apispec.TypeRef{Kind: apispec.TypeHandle, IsPointer: true, QualType: "ModuleCatalog **"},
			},
			want: true,
		},
		{
			name: "const T** is output",
			p: apispec.Param{
				Name: "out",
				Type: apispec.TypeRef{Kind: apispec.TypeHandle, IsPointer: true, IsConst: true, QualType: "const Type **"},
			},
			want: true,
		},
		{
			name: "const T* is input",
			p: apispec.Param{
				Name: "opts",
				Type: apispec.TypeRef{Kind: apispec.TypeHandle, IsPointer: true, IsConst: true, QualType: "const Options *"},
			},
			want: false,
		},
		{
			name: "unique_ptr<T>* is output",
			p: apispec.Param{
				Name: "out",
				Type: apispec.TypeRef{Kind: apispec.TypeHandle, IsPointer: true, QualType: "std::unique_ptr<Output> *"},
			},
			want: true,
		},
		{
			name: "primitive bool* is output",
			p: apispec.Param{
				Name: "at_end",
				Type: apispec.TypeRef{Kind: apispec.TypePrimitive, IsPointer: true, QualType: "bool *"},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isOutputParam(tt.p)
			if got != tt.want {
				t.Errorf("isOutputParam(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// TestGenerateProtoFactoryOwned verifies that concrete classes with
// non-public destructors (factory-owned types like googlesql::ArrayType)
// still get a service with methods, but without a Free RPC. This is
// the googlesql::Type / ArrayType / StructType / EnumType case where
// a TypeFactory retains lifetime; the Go binding holds a borrowed
// pointer it must not delete.
func TestGenerateProtoFactoryOwned(t *testing.T) {
	spec := &apispec.APISpec{
		Namespace: "test",
		Classes: []apispec.Class{
			{
				Name:          "ManagedValue",
				QualName:      "test::ManagedValue",
				Namespace:     "test",
				IsHandle:      true,
				HasPublicDtor: false, // dtor is protected/private
				SourceFile:    "test/managed.h",
				Methods: []apispec.Function{
					{
						Name:       "kind",
						QualName:   "test::ManagedValue::kind",
						Access:     "public",
						ReturnType: apispec.TypeRef{Name: "int", Kind: apispec.TypePrimitive},
					},
				},
			},
		},
	}
	output := GenerateProto(spec, "test")
	// The service itself must exist — the class has methods worth
	// exposing.
	if !strings.Contains(output, "service ManagedValueService {") {
		t.Error("expected ManagedValueService to be emitted for class with non-public dtor")
	}
	// The Kind method must be wired as an RPC.
	if !strings.Contains(output, "rpc Kind(") {
		t.Error("expected Kind RPC in ManagedValueService")
	}
	// No Free RPC — the handle cannot be deleted from outside.
	svcStart := strings.Index(output, "service ManagedValueService {")
	svcEnd := strings.Index(output[svcStart:], "}\n")
	if svcEnd < 0 {
		t.Fatal("ManagedValueService body not properly terminated")
	}
	svcBody := output[svcStart : svcStart+svcEnd]
	if strings.Contains(svcBody, "rpc Free(") {
		t.Error("Free RPC must NOT be emitted for factory-owned class (non-public dtor)")
	}
}

// TestGenerateProtoAbstract verifies that abstract classes get
// `option (wasmify.wasm_abstract) = true;` in the generated proto.
func TestGenerateProtoAbstract(t *testing.T) {
	spec := &apispec.APISpec{
		Namespace: "test",
		Classes: []apispec.Class{
			{
				Name:          "BaseNode",
				QualName:      "test::BaseNode",
				Namespace:     "test",
				IsHandle:      true,
				IsAbstract:    true,
				HasPublicDtor: true,
				SourceFile:    "test/node.h",
			},
			{
				Name:          "ConcreteNode",
				QualName:      "test::ConcreteNode",
				Namespace:     "test",
				IsHandle:      true,
				Parent:        "test::BaseNode",
				HasPublicDtor: true,
				HasPublicDefaultCtor: true,
				SourceFile:    "test/node.h",
			},
		},
	}
	output := GenerateProto(spec, "test")
	if !strings.Contains(output, "wasm_abstract") {
		t.Error("expected wasm_abstract option for abstract class BaseNode")
	}
	// ConcreteNode should NOT have wasm_abstract
	// Find ConcreteNode section and verify no wasm_abstract
	idx := strings.Index(output, "message ConcreteNode")
	if idx < 0 {
		t.Fatal("ConcreteNode message not found")
	}
	section := output[idx : idx+200]
	if strings.Contains(section, "wasm_abstract") {
		t.Error("ConcreteNode should not have wasm_abstract")
	}
	// BaseNode should have wasm_parent on ConcreteNode
	if !strings.Contains(output, "wasm_parent") {
		t.Error("expected wasm_parent on ConcreteNode")
	}
}
