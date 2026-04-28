package protogen

import (
	"strings"
	"testing"

	"github.com/goccy/wasmify/internal/apispec"
)

func TestOperatorToProtoName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"operator==", "OperatorEqual"},
		{"operator!=", "OperatorNotEqual"},
		{"operator<", "OperatorLess"},
		{"operator<=", "OperatorLessEqual"},
		{"operator>", "OperatorGreater"},
		{"operator>=", "OperatorGreaterEqual"},
		{"operator+", "OperatorAdd"},
		{"operator-", "OperatorSubtract"},
		{"operator*", "OperatorMultiply"},
		{"operator/", "OperatorDivide"},
		{"operator%", "OperatorModulo"},
		{"operator[]", "OperatorIndex"},
		{"operator()", "OperatorCall"},
		{"operator<<", "OperatorShiftLeft"},
		{"operator>>", "OperatorShiftRight"},
		{"operator&", "OperatorBitwiseAnd"},
		{"operator|", "OperatorBitwiseOr"},
		{"operator^", "OperatorBitwiseXor"},
		{"operator~", "OperatorBitwiseNot"},
		{"operator!", "OperatorNot"},
		{"operator&&", "OperatorLogicalAnd"},
		{"operator||", "OperatorLogicalOr"},
		{"operator=", "OperatorAssign"},
		{"operator+=", "OperatorAddAssign"},
		{"operator-=", "OperatorSubtractAssign"},
		{"operator->", "OperatorArrow"},
		// Conversion operator
		{"operator bool", "OperatorConvertToBool"},
		{"operator int", "OperatorConvertToInt"},
		// Fallback: unknown operator
		{"operator?unknown", "Operatorunknown"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := operatorToProtoName(tt.in)
			if got != tt.want {
				t.Errorf("operatorToProtoName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestToProtoRPCName(t *testing.T) {
	tests := []struct {
		in, wantRPC, wantOrig string
	}{
		{"~Foo", "", ""}, // destructor skipped
		{"Foo", "Foo", ""},
		{"foo_bar", "FooBar", ""},
		{"operator==", "OperatorEqual", "operator=="},
		{"operator==2", "OperatorEqual2", "operator==2"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			rpc, orig := toProtoRPCName(tt.in)
			if rpc != tt.wantRPC {
				t.Errorf("toProtoRPCName(%q) RPC = %q, want %q", tt.in, rpc, tt.wantRPC)
			}
			if orig != tt.wantOrig {
				t.Errorf("toProtoRPCName(%q) orig = %q, want %q", tt.in, orig, tt.wantOrig)
			}
		})
	}
}

func TestParseMapType(t *testing.T) {
	// Extra map / set prefixes (e.g. absl::flat_hash_map,
	// google::protobuf::Map) are registered through BridgeConfig;
	// reset bridgeConfig around this test so the behaviour is
	// exercised end-to-end.
	prev := bridgeConfig
	bridgeConfig = BridgeConfig{
		MapLikeTypePrefixes: []string{
			"absl::flat_hash_map",
			"google::protobuf::Map",
		},
		SetLikeTypePrefixes: []string{"absl::flat_hash_set"},
	}
	defer func() { bridgeConfig = prev }()

	tests := []struct {
		name             string
		in               string
		wantKey, wantVal string
		wantOK           bool
	}{
		{"flat_hash_map", "absl::flat_hash_map<int, std::string>", "int", "std::string", true},
		{"unordered_map", "std::unordered_map<int, Foo>", "int", "Foo", true},
		{"std::map", "std::map<std::string, int>", "std::string", "int", true},
		{"flat_hash_set", "absl::flat_hash_set<int>", "int", "", true},
		{"std::set", "std::set<std::string>", "std::string", "", true},
		{"protobuf::Map", "google::protobuf::Map<int, Foo>", "int", "Foo", true},
		{"not a map", "std::vector<int>", "", "", false},
		{"plain type", "Foo", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, v, ok := parseMapType(tt.in)
			if ok != tt.wantOK {
				t.Errorf("parseMapType(%q) ok = %v, want %v", tt.in, ok, tt.wantOK)
			}
			if ok {
				if k != tt.wantKey || v != tt.wantVal {
					t.Errorf("parseMapType(%q) = (%q, %q), want (%q, %q)", tt.in, k, v, tt.wantKey, tt.wantVal)
				}
			}
		})
	}
}

func TestSplitTemplateArgs(t *testing.T) {
	tests := []struct {
		in                         string
		wantA, wantB string
		wantOK       bool
	}{
		{"a, b", "a", "b", true},
		{"a,b", "a", "b", true},
		{"a<x, y>, b", "a<x, y>", "b", true},
		{"a", "", "", false},
		{"", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			a, b, ok := splitTemplateArgs(tt.in)
			if ok != tt.wantOK {
				t.Errorf("splitTemplateArgs(%q) ok = %v, want %v", tt.in, ok, tt.wantOK)
			}
			if ok {
				if a != tt.wantA || b != tt.wantB {
					t.Errorf("splitTemplateArgs(%q) = (%q, %q), want (%q, %q)", tt.in, a, b, tt.wantA, tt.wantB)
				}
			}
		})
	}
}

func TestCppTypeNameToProto(t *testing.T) {
	// Library-specific string aliases (e.g. absl::string_view,
	// absl::Cord) are registered through BridgeConfig.ExtraStringTypes;
	// set them here so the mapping is exercised end-to-end.
	prev := bridgeConfig
	bridgeConfig = BridgeConfig{
		ExtraStringTypes: []string{"absl::string_view", "absl::Cord"},
	}
	defer func() { bridgeConfig = prev }()

	tests := []struct {
		in, want string
	}{
		{"int", "int32"},
		{"bool", "bool"},
		{"double", "double"},
		{"std::string", "string"},
		{"string", "string"},
		{"absl::string_view", "string"},
		{"std::string_view", "string"},
		{"absl::Cord", "string"},
		// Note: "const char" gets its "const " prefix stripped to "char",
		// which matches primitiveToPbType → "int32" (not string, because
		// cppTypeNameToProto only recognizes specific strings before the
		// primitive check).
		{"Foo", "Foo"},
		{"const int*", "int32"},
		{"ns::MyClass", "MyClass"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := cppTypeNameToProto(tt.in)
			if got != tt.want {
				t.Errorf("cppTypeNameToProto(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTryPrimitiveToPbType(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"int", "int32"},
		{"bool", "bool"},
		{"double", "double"},
		{"float", "float"},
		{"uint64_t", "uint64"},
		{"size_t", "uint64"},
		{"short", "int32"},
		{"unknown", ""},
		{"std::string", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := tryPrimitiveToPbType(tt.in)
			if got != tt.want {
				t.Errorf("tryPrimitiveToPbType(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsProtoMapKeyType(t *testing.T) {
	valid := []string{"int32", "int64", "uint32", "uint64", "sint32", "sint64",
		"fixed32", "fixed64", "sfixed32", "sfixed64", "bool", "string"}
	for _, v := range valid {
		if !isProtoMapKeyType(v) {
			t.Errorf("%q should be valid map key", v)
		}
	}
	// Invalid
	for _, v := range []string{"double", "float", "bytes", "Foo"} {
		if isProtoMapKeyType(v) {
			t.Errorf("%q should not be valid map key", v)
		}
	}
}

func TestIsKnownProtoType(t *testing.T) {
	// Built-in scalars
	for _, v := range []string{"double", "float", "int32", "int64", "uint32", "uint64",
		"bool", "string", "bytes"} {
		if !isKnownProtoType(v) {
			t.Errorf("%q should be known", v)
		}
	}
	// Unknown without messageNameMap: false
	prev := messageNameMap
	messageNameMap = map[string]string{"ns::Foo": "Foo"}
	defer func() { messageNameMap = prev }()
	if !isKnownProtoType("Foo") {
		t.Error("Foo should be known via messageNameMap")
	}
	if isKnownProtoType("Unknown") {
		t.Error("Unknown should not be known")
	}
}

func TestHandleMapType(t *testing.T) {
	prev := wrapperMessages
	wrapperMessages = make(map[string]string)
	defer func() { wrapperMessages = prev }()

	// Native map<int, string>
	got := handleMapType("", "int", "std::string")
	if got != "map<int32, string>" {
		t.Errorf("got %q, want map<int32, string>", got)
	}

	// Set type (no value): repeated
	got = handleMapType("", "int", "")
	if got != "repeated int32" {
		t.Errorf("got %q, want repeated int32", got)
	}

	// Unknown key or value → bytes
	got = handleMapType("", "UnknownType", "OtherUnknown")
	if got != "bytes" {
		t.Errorf("got %q, want bytes", got)
	}
}

func TestStripTemplateWrappers(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"std::unique_ptr<Foo>", "Foo"},
		{"unique_ptr<Foo>", "Foo"},
		{"std::shared_ptr<Foo>", "Foo"},
		{"shared_ptr<Foo>", "Foo"},
		{"std::unique_ptr<const Foo>", "Foo"},
		{"Foo", "Foo"},
		{"std::vector<Foo>", "std::vector<Foo>"}, // not stripped
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := stripTemplateWrappers(tt.in)
			if got != tt.want {
				t.Errorf("stripTemplateWrappers(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsScreamingSnake(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"HELLO", true},
		{"HELLO_WORLD", true},
		{"FOO_123", true},
		{"hello", false},
		{"Hello", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := isScreamingSnake(tt.in); got != tt.want {
				t.Errorf("isScreamingSnake(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsValidProtoIdent(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"Foo", true},
		{"Foo_Bar", true},
		{"Foo123", true},
		{"ns.Foo", true},
		{"Foo<Bar>", false},
		{"Foo Bar", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := isValidProtoIdent(tt.in); got != tt.want {
				t.Errorf("isValidProtoIdent(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestDisambiguateProtoName(t *testing.T) {
	// Two conflicting classes: each uses distinct parent name for disambiguation
	name1 := disambiguateProtoName("ns::FooProto::_Internal",
		[]string{"ns::FooProto::_Internal", "ns::BarProto::_Internal"})
	// Should be "FooProto__Internal"
	if name1 == "" {
		t.Error("expected disambiguated name")
	}
	if !strings.Contains(name1, "FooProto") {
		t.Errorf("expected FooProto in %q", name1)
	}
}

func TestSanitizeFieldName(t *testing.T) {
	tests := []struct {
		in   string
		num  int
		want string
	}{
		{"foo", 1, "foo"},
		{"foo_bar", 1, "foo_bar"},
		{"", 3, "field_3"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := sanitizeFieldName(tt.in, tt.num)
			if got != tt.want {
				t.Errorf("sanitizeFieldName(%q, %d) = %q, want %q", tt.in, tt.num, got, tt.want)
			}
		})
	}
}

func TestIsOutputParam(t *testing.T) {
	// Pointer to pointer: output
	p := apispec.Param{
		Type: apispec.TypeRef{Kind: apispec.TypeHandle, IsPointer: true, QualType: "Foo **"},
	}
	if !isOutputParam(p) {
		t.Error("T** should be output")
	}
	// const T* input: not output
	p2 := apispec.Param{
		Type: apispec.TypeRef{Kind: apispec.TypeHandle, IsPointer: true, IsConst: true, QualType: "const Foo*"},
	}
	if isOutputParam(p2) {
		t.Error("const T* should not be output")
	}
}

func TestDisambiguateOverloads(t *testing.T) {
	fns := []apispec.Function{
		{Name: "foo"},
		{Name: "bar"},
		{Name: "foo"},
		{Name: "foo"},
		{Name: "baz"},
	}
	result := disambiguateOverloads(fns)
	// foo, bar, foo2, foo3, baz
	if result[0].Name != "foo" {
		t.Errorf("result[0] = %q", result[0].Name)
	}
	if result[1].Name != "bar" {
		t.Errorf("result[1] = %q", result[1].Name)
	}
	if result[2].Name != "foo2" {
		t.Errorf("result[2] = %q", result[2].Name)
	}
	if result[3].Name != "foo3" {
		t.Errorf("result[3] = %q", result[3].Name)
	}
	if result[4].Name != "baz" {
		t.Errorf("result[4] = %q", result[4].Name)
	}
	// OriginalName preserved
	if result[2].OriginalName != "foo" {
		t.Errorf("OriginalName missing: %q", result[2].OriginalName)
	}
}

func TestWriteRequestResponse(t *testing.T) {
	prev := classQualNames
	prevMsgs := messageNameMap
	prevWrap := wrapperMessages
	classQualNames = map[string]string{}
	messageNameMap = map[string]string{}
	wrapperMessages = make(map[string]string)
	defer func() {
		classQualNames = prev
		messageNameMap = prevMsgs
		wrapperMessages = prevWrap
	}()

	fn := &apispec.Function{
		Name: "Compute",
		Params: []apispec.Param{
			{Name: "a", Type: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"}},
			{Name: "b", Type: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "double"}},
		},
		ReturnType: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"},
	}
	var b strings.Builder
	emitted := make(map[string]bool)
	writeRequestResponse(&b, fn, emitted)
	out := b.String()
	// ComputeRequest with params
	if !strings.Contains(out, "message ComputeRequest") {
		t.Errorf("expected ComputeRequest: %q", out)
	}
	if !strings.Contains(out, "int32 a = 1") {
		t.Errorf("expected int32 a = 1: %q", out)
	}
	if !strings.Contains(out, "double b = 2") {
		t.Errorf("expected double b = 2: %q", out)
	}
	// ComputeResponse with result
	if !strings.Contains(out, "message ComputeResponse") {
		t.Errorf("expected ComputeResponse: %q", out)
	}
	if !strings.Contains(out, "int32 result = 1") {
		t.Errorf("expected int32 result = 1: %q", out)
	}
	if !strings.Contains(out, "string error = 15") {
		t.Errorf("expected error field: %q", out)
	}
}

func TestWriteRequestResponse_VoidReturn(t *testing.T) {
	prev := classQualNames
	prevMsgs := messageNameMap
	prevWrap := wrapperMessages
	classQualNames = map[string]string{}
	messageNameMap = map[string]string{}
	wrapperMessages = make(map[string]string)
	defer func() {
		classQualNames = prev
		messageNameMap = prevMsgs
		wrapperMessages = prevWrap
	}()

	fn := &apispec.Function{
		Name:       "DoVoid",
		ReturnType: apispec.TypeRef{Kind: apispec.TypeVoid},
	}
	var b strings.Builder
	emitted := make(map[string]bool)
	writeRequestResponse(&b, fn, emitted)
	out := b.String()
	// Should not have result field
	if strings.Contains(out, "result =") {
		t.Errorf("void return should not have result field: %q", out)
	}
	// Still has error field
	if !strings.Contains(out, "string error = 15") {
		t.Errorf("expected error field: %q", out)
	}
}

func TestTypeRefToProto_Map(t *testing.T) {
	prevQual := classQualNames
	prevMsg := messageNameMap
	prevWrap := wrapperMessages
	classQualNames = map[string]string{}
	messageNameMap = map[string]string{}
	wrapperMessages = make(map[string]string)
	defer func() {
		classQualNames = prevQual
		messageNameMap = prevMsg
		wrapperMessages = prevWrap
	}()

	// Map type as TypeValue
	ref := apispec.TypeRef{
		Kind: apispec.TypeValue,
		Name: "std::unordered_map<int, std::string>",
	}
	got := typeRefToProto(ref)
	if !strings.Contains(got, "map<int32, string>") {
		t.Errorf("expected map<int32, string>, got %q", got)
	}

	// Unknown with map name
	ref = apispec.TypeRef{
		Kind: apispec.TypeUnknown,
		Name: "std::set<int>",
	}
	got = typeRefToProto(ref)
	if !strings.Contains(got, "repeated int32") {
		t.Errorf("expected repeated int32, got %q", got)
	}
}

func TestTypeRefToProto_NestedVector(t *testing.T) {
	prevMsg := messageNameMap
	prevWrap := wrapperMessages
	messageNameMap = map[string]string{}
	wrapperMessages = make(map[string]string)
	defer func() {
		messageNameMap = prevMsg
		wrapperMessages = prevWrap
	}()

	// vector<vector<int>>
	ref := apispec.TypeRef{
		Kind: apispec.TypeVector,
		Inner: &apispec.TypeRef{
			Kind:  apispec.TypeVector,
			Inner: &apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"},
		},
	}
	got := typeRefToProto(ref)
	// Expect wrapper message to be created
	if !strings.Contains(got, "List") {
		t.Errorf("expected wrapper List type, got %q", got)
	}
	// Wrapper message should be registered
	if len(wrapperMessages) == 0 {
		t.Error("expected wrapper message registered")
	}
}

func TestTypeRefToProto_EnumUnqualified(t *testing.T) {
	ref := apispec.TypeRef{Kind: apispec.TypeEnum, Name: "UnqualEnum"}
	got := typeRefToProto(ref)
	// Unqualified → bytes fallback
	if got != "bytes" {
		t.Errorf("unqualified enum should be bytes, got %q", got)
	}
}

func TestTypeRefToProto_EnumQualified(t *testing.T) {
	prevMsg := messageNameMap
	messageNameMap = map[string]string{"ns::MyEnum": "MyEnum"}
	defer func() { messageNameMap = prevMsg }()
	ref := apispec.TypeRef{Kind: apispec.TypeEnum, Name: "ns::MyEnum"}
	got := typeRefToProto(ref)
	if got != "MyEnum" {
		t.Errorf("expected MyEnum, got %q", got)
	}
}

func TestIsSkippedMethod(t *testing.T) {
	// Destructor
	if !isSkippedMethod("~Foo") {
		t.Error("destructor should be skipped")
	}
	// Operator
	if !isSkippedMethod("operator==") {
		t.Error("operator should be skipped")
	}
	// Regular method
	if isSkippedMethod("MyMethod") {
		t.Error("regular method should not be skipped")
	}
}

func TestDisambiguateMethodNames(t *testing.T) {
	methods := []apispec.Function{
		{Name: "foo"},
		{Name: "foo"},
		{Name: "bar"},
		{Name: "foo"},
		{Name: "~Dtor"},
		{Name: "baz", IsStatic: true},
	}
	result := disambiguateMethodNames(methods)
	// foo, foo2, bar, foo3, ~Dtor, baz
	if result[0].Name != "foo" {
		t.Errorf("result[0] = %q, want foo", result[0].Name)
	}
	if result[1].Name != "foo2" {
		t.Errorf("result[1] = %q, want foo2", result[1].Name)
	}
	if result[2].Name != "bar" {
		t.Errorf("result[2] = %q, want bar", result[2].Name)
	}
	if result[3].Name != "foo3" {
		t.Errorf("result[3] = %q, want foo3", result[3].Name)
	}
	if result[5].Name != "baz" {
		t.Errorf("static should not be renamed, got %q", result[5].Name)
	}
	// OriginalName preserved
	if result[1].OriginalName != "foo" {
		t.Errorf("OriginalName not preserved: %q", result[1].OriginalName)
	}
}

func TestCollectReferencedHandleTypes(t *testing.T) {
	functions := []apispec.Function{
		{
			Name:       "f1",
			ReturnType: apispec.TypeRef{Kind: apispec.TypeHandle, Name: "Foo"},
		},
		{
			Name: "f2",
			Params: []apispec.Param{
				{Type: apispec.TypeRef{Kind: apispec.TypeHandle, Name: "Bar", IsPointer: true}},
			},
		},
	}
	spec := &apispec.APISpec{}
	refs := collectReferencedHandleTypes(spec, functions)
	if !refs["Foo"] {
		t.Error("expected Foo referenced")
	}
	if !refs["Bar"] {
		t.Error("expected Bar referenced")
	}
}
