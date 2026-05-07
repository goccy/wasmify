package protogen

import (
	"strings"
	"testing"

	"github.com/goccy/wasmify/internal/apispec"
)

func TestCppPrimitiveType(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"bool", "bool"},
		{"int", "int32_t"},
		{"int32_t", "int32_t"},
		{"unsigned int", "uint32_t"},
		{"uint32_t", "uint32_t"},
		{"long", "int64_t"},
		{"long long", "int64_t"},
		{"int64_t", "int64_t"},
		{"ssize_t", "int64_t"},
		{"ptrdiff_t", "int64_t"},
		{"intptr_t", "int64_t"},
		{"unsigned long", "uint64_t"},
		{"unsigned long long", "uint64_t"},
		{"uint64_t", "uint64_t"},
		{"size_t", "uint64_t"},
		{"uintptr_t", "uint64_t"},
		{"short", "int32_t"},
		{"int16_t", "int32_t"},
		{"char", "int32_t"},
		{"int8_t", "int32_t"},
		{"signed char", "int32_t"},
		{"unsigned short", "uint32_t"},
		{"uint16_t", "uint32_t"},
		{"unsigned char", "uint32_t"},
		{"uint8_t", "uint32_t"},
		{"float", "float"},
		{"double", "double"},
		{"long double", "double"},
		{"unknown", "int64_t"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := cppPrimitiveType(tt.in); got != tt.want {
				t.Errorf("cppPrimitiveType(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCppLocalType(t *testing.T) {
	// Save and restore global state. cppLocalType calls cppTypeName
	// which consults classQualNames; set-like recognition consults
	// bridgeConfig.SetLikeTypePrefixes via setLikeContainerInfo.
	// classQualNames stays empty for parity with the original
	// expectations (vec<Foo*> resolves to itself); set-like recognition
	// still works because parseMapType keys on the ref.Name spelling.
	prev := classQualNames
	prevCfg := bridgeConfig
	classQualNames = map[string]string{}
	bridgeConfig = DefaultBridgeConfig()
	bridgeConfig.SetLikeTypePrefixes = []string{"absl::flat_hash_set"}
	defer func() {
		classQualNames = prev
		bridgeConfig = prevCfg
	}()

	tests := []struct {
		name string
		ref  apispec.TypeRef
		want string
	}{
		{"bool", apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "bool"}, "bool"},
		{"int", apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"}, "int32_t"},
		{"string", apispec.TypeRef{Kind: apispec.TypeString}, "std::string"},
		{"string_view", apispec.TypeRef{Kind: apispec.TypeString, Name: "std::string_view"}, "std::string"},
		{"handle", apispec.TypeRef{Kind: apispec.TypeHandle, Name: "Foo"}, "uint64_t"},
		{"void", apispec.TypeRef{Kind: apispec.TypeVoid}, "void"},
		{"vec<int>", apispec.TypeRef{
			Kind:  apispec.TypeVector,
			Inner: &apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"},
		}, "std::vector<int32_t>"},
		{"vec<Handle*>", apispec.TypeRef{
			Kind:  apispec.TypeVector,
			Inner: &apispec.TypeRef{Kind: apispec.TypeHandle, Name: "Foo", IsPointer: true},
		}, "std::vector<Foo*>"},
		{"vec<const Handle*>", apispec.TypeRef{
			Kind:  apispec.TypeVector,
			Inner: &apispec.TypeRef{Kind: apispec.TypeHandle, Name: "Foo", IsPointer: true, IsConst: true},
		}, "std::vector<const Foo*>"},
		{"vec_no_inner", apispec.TypeRef{Kind: apispec.TypeVector}, "std::vector<uint8_t>"},
		{"unknown", apispec.TypeRef{Kind: apispec.TypeUnknown}, "/* unknown */"},
		// Set-like containers configured via BridgeConfig.SetLikeTypePrefixes
		// must declare the actual container type as the local. Treating
		// them as opaque uint64 handles (the prior behaviour) is wrong:
		// the proto schema emits `repeated <Handle>` for the same
		// parameter, so the bridge body has to materialise the
		// container element-by-element from the wire.
		{
			"set_like_handle_pointer_inner",
			apispec.TypeRef{
				Kind:     apispec.TypeHandle,
				Name:     "absl::flat_hash_set<const Foo *>",
				QualType: "const absl::flat_hash_set<const Foo *> &",
			},
			"absl::flat_hash_set<const Foo*>",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cppLocalType(tt.ref)
			if got != tt.want {
				t.Errorf("cppLocalType(%v) = %q, want %q", tt.ref, got, tt.want)
			}
		})
	}
}

func TestCppTypeName(t *testing.T) {
	prev := classQualNames
	classQualNames = map[string]string{"Foo": "ns::Foo"}
	defer func() { classQualNames = prev }()

	tests := []struct {
		name string
		ref  apispec.TypeRef
		want string
	}{
		{"primitive", apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"}, "int32_t"},
		{"string", apispec.TypeRef{Kind: apispec.TypeString}, "std::string"},
		{"enum", apispec.TypeRef{Kind: apispec.TypeEnum, Name: "MyEnum"}, "MyEnum"},
		{"handle with qualtype", apispec.TypeRef{Kind: apispec.TypeHandle, Name: "Foo", QualType: "const Foo*"}, "ns::Foo"},
		{"handle short", apispec.TypeRef{Kind: apispec.TypeHandle, Name: "Foo"}, "ns::Foo"},
		{"empty", apispec.TypeRef{Kind: apispec.TypeHandle}, "/* unknown type */"},
		// Elaborated-type-specifiers (`class T` / `struct T` / `enum T`)
		// surface in clang's qual_type spelling for declarations that
		// repeat the keyword. The bridge generator must strip them
		// before resolving the bare name; otherwise resolveTypeName
		// fails to match `class Foo` against the `Foo` → `ns::Foo`
		// entry and the type leaks downstream as an unqualified
		// `class Foo`, breaking compilation.
		{
			"class elaborated qualtype",
			apispec.TypeRef{Kind: apispec.TypeHandle, Name: "class Foo", QualType: "const class Foo *"},
			"ns::Foo",
		},
		{
			"struct elaborated qualtype",
			apispec.TypeRef{Kind: apispec.TypeHandle, Name: "struct Foo", QualType: "const struct Foo &"},
			"ns::Foo",
		},
		{
			"class elaborated short name only",
			apispec.TypeRef{Kind: apispec.TypeHandle, Name: "class Foo"},
			"ns::Foo",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cppTypeName(tt.ref); got != tt.want {
				t.Errorf("cppTypeName(%v) = %q, want %q", tt.ref, got, tt.want)
			}
		})
	}
}

func TestCppParamType(t *testing.T) {
	prev := classQualNames
	classQualNames = map[string]string{"Foo": "ns::Foo"}
	defer func() { classQualNames = prev }()

	tests := []struct {
		name string
		ref  apispec.TypeRef
		want string
	}{
		{"const ref", apispec.TypeRef{Kind: apispec.TypeHandle, Name: "Foo", IsConst: true, IsRef: true}, "const ns::Foo&"},
		{"mut ref", apispec.TypeRef{Kind: apispec.TypeHandle, Name: "Foo", IsRef: true}, "ns::Foo&"},
		{"ptr", apispec.TypeRef{Kind: apispec.TypeHandle, Name: "Foo", IsPointer: true}, "ns::Foo*"},
		{"const ptr", apispec.TypeRef{Kind: apispec.TypeHandle, Name: "Foo", IsPointer: true, IsConst: true}, "const ns::Foo*"},
		{"primitive", apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"}, "int32_t"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cppParamType(tt.ref); got != tt.want {
				t.Errorf("cppParamType(%v) = %q, want %q", tt.ref, got, tt.want)
			}
		})
	}
}

func TestCppReturnType(t *testing.T) {
	prev := classQualNames
	classQualNames = nil
	defer func() { classQualNames = prev }()

	tests := []struct {
		name string
		ref  apispec.TypeRef
		want string
	}{
		{"primitive", apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"}, "int32_t"},
		{"enum", apispec.TypeRef{Kind: apispec.TypeEnum, Name: "E"}, "E"},
		{"const lvalue ref", apispec.TypeRef{Kind: apispec.TypeHandle, IsRef: true, IsConst: true, QualType: "const Foo&"}, "const auto&"},
		{"mut lvalue ref", apispec.TypeRef{Kind: apispec.TypeHandle, IsRef: true, QualType: "Foo&"}, "auto&"},
		{"rvalue ref", apispec.TypeRef{Kind: apispec.TypeHandle, IsRef: true, QualType: "Foo&&"}, "auto&&"},
		{"value", apispec.TypeRef{Kind: apispec.TypeHandle, Name: "Foo"}, "auto"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cppReturnType(tt.ref); got != tt.want {
				t.Errorf("cppReturnType(%v) = %q, want %q", tt.ref, got, tt.want)
			}
		})
	}
}

func TestProtoWireType(t *testing.T) {
	tests := []struct {
		name string
		ref  apispec.TypeRef
		want int
	}{
		{"float", apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "float"}, 5},
		{"double", apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "double"}, 1},
		{"int", apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"}, 0},
		{"enum", apispec.TypeRef{Kind: apispec.TypeEnum}, 0},
		{"string", apispec.TypeRef{Kind: apispec.TypeString}, 2},
		{"handle", apispec.TypeRef{Kind: apispec.TypeHandle}, 2},
		{"value", apispec.TypeRef{Kind: apispec.TypeValue}, 2},
		{"vector", apispec.TypeRef{Kind: apispec.TypeVector}, 2},
		{"unknown", apispec.TypeRef{Kind: apispec.TypeUnknown}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := protoWireType(tt.ref); got != tt.want {
				t.Errorf("protoWireType(%v) = %d, want %d", tt.ref, got, tt.want)
			}
		})
	}
}

func TestIsHandleByPointer(t *testing.T) {
	if !isHandleByPointer(apispec.TypeRef{IsPointer: true}) {
		t.Error("expected true for IsPointer")
	}
	if !isHandleByPointer(apispec.TypeRef{IsRef: true}) {
		t.Error("expected true for IsRef")
	}
	if isHandleByPointer(apispec.TypeRef{}) {
		t.Error("expected false for value")
	}
}

func TestIsSmartPointerType(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"std::unique_ptr<T>", true},
		{"unique_ptr<T>", true},
		{"std::shared_ptr<T>", true},
		{"shared_ptr<T>", true},
		{"const std::unique_ptr<T>", true},
		{"Foo", false},
		{"", false},
		{"std::vector<T>", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := isSmartPointerType(tt.in); got != tt.want {
				t.Errorf("isSmartPointerType(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsSharedPointerType(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"std::shared_ptr<T>", true},
		{"shared_ptr<T>", true},
		{"const std::shared_ptr<T>", true},
		{"std::unique_ptr<T>", false},
		{"Foo", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := isSharedPointerType(tt.in); got != tt.want {
				t.Errorf("isSharedPointerType(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestExtractTemplateArgFromQualType(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"Span<const Foo>", "const Foo"},
		{"std::unique_ptr<Bar>", "Bar"},
		{"Foo", ""},
		{"<incomplete", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := extractTemplateArgFromQualType(tt.in); got != tt.want {
				t.Errorf("extractTemplateArgFromQualType(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsProjectSource(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"mylib/include/foo.h", true},
		{"src/lib.cc", true},
		{"external/abseil-cpp/foo.h", false},
		{"/usr/include/stdio.h", false},
		{"/Library/SDKs/mac.sdk/foo.h", false},
		{"/bar/c++/v1/string", false},
		{"/foo/include/c++/8/string", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := isProjectSource(tt.in); got != tt.want {
				t.Errorf("isProjectSource(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestResolveTypeName(t *testing.T) {
	prev := classQualNames
	classQualNames = map[string]string{
		"Foo": "ns::Foo",
		"Bar": "ns::Bar",
	}
	defer func() { classQualNames = prev }()

	tests := []struct {
		in, want string
	}{
		{"Foo", "ns::Foo"},
		{"Bar", "ns::Bar"},
		{"Baz", "Baz"},          // not in map, return as-is
		{"ns::Foo", "ns::Foo"},  // already qualified
		{"int", "int"},          // primitive stays
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := resolveTypeName(tt.in); got != tt.want {
				t.Errorf("resolveTypeName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestResolveTypeNameInContext(t *testing.T) {
	prev := classQualNames
	classQualNames = map[string]string{
		"Foo": "ns::Foo",
	}
	defer func() { classQualNames = prev }()

	// When the short name matches the enclosing class, resolve to enclosing
	got := resolveTypeNameInContext("Foo", "ns::other::Foo")
	if got != "ns::other::Foo" {
		t.Errorf("expected ns::other::Foo, got %q", got)
	}

	// Unknown name + no context falls through
	got = resolveTypeNameInContext("Unknown", "")
	if got != "Unknown" {
		t.Errorf("got %q", got)
	}
}

func TestQualifyTemplateArgs(t *testing.T) {
	prev := classQualNames
	prevSpec := specNamespace
	classQualNames = map[string]string{"Foo": "ns::Foo"}
	specNamespace = "ns"
	defer func() {
		classQualNames = prev
		specNamespace = prevSpec
	}()

	tests := []struct {
		in, want string
	}{
		{"std::unique_ptr<Foo>", "std::unique_ptr<ns::Foo>"},
		{"std::unique_ptr<const Foo>", "std::unique_ptr<const ns::Foo>"},
		{"std::unique_ptr<Foo*>", "std::unique_ptr<ns::Foo*>"},
		{"std::unique_ptr<Foo&>", "std::unique_ptr<ns::Foo&>"},
		// Plain names (no template)
		{"Foo", "Foo"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := qualifyTemplateArgs(tt.in)
			if got != tt.want {
				t.Errorf("qualifyTemplateArgs(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestQualifySingleArg(t *testing.T) {
	prev := classQualNames
	classQualNames = map[string]string{"Foo": "ns::Foo"}
	defer func() { classQualNames = prev }()

	tests := []struct {
		in, want string
	}{
		{"Foo", "ns::Foo"},
		{"const Foo", "const ns::Foo"},
		{"Foo*", "ns::Foo*"},
		{"Foo&", "ns::Foo&"},
		{"const Foo*", "const ns::Foo*"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := qualifySingleArg(tt.in)
			if got != tt.want {
				t.Errorf("qualifySingleArg(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestMatchErrorType(t *testing.T) {
	// Set up config
	prev := bridgeConfig
	bridgeConfig = BridgeConfig{
		ErrorTypes: map[string]string{
			"absl::Status":   "if (!{result}.ok()) {...}",
			"absl::StatusOr": "if (!{result}.ok()) {...}",
		},
	}
	defer func() { bridgeConfig = prev }()

	// Exact match
	if got := matchErrorType("absl::Status"); got == "" {
		t.Errorf("expected absl::Status match, got %q", got)
	}
	// Template prefix match
	if got := matchErrorType("absl::StatusOr<int>"); got == "" {
		t.Errorf("expected absl::StatusOr<int> template match, got %q", got)
	}
	// No match
	if got := matchErrorType("std::string"); got != "" {
		t.Errorf("expected no match for std::string, got %q", got)
	}
}

func TestIsAllowedExternalType(t *testing.T) {
	prev := bridgeConfig
	bridgeConfig = BridgeConfig{
		ExternalTypes: []string{"absl::Status", "absl::StatusOr"},
	}
	defer func() { bridgeConfig = prev }()

	// Exact match
	if !isAllowedExternalType("absl::Status") {
		t.Error("expected absl::Status allowed")
	}
	// Template prefix match
	if !isAllowedExternalType("absl::StatusOr<Foo>") {
		t.Error("expected absl::StatusOr<Foo> allowed")
	}
	// With const qualifier
	if !isAllowedExternalType("const absl::Status&") {
		t.Error("expected const absl::Status& allowed")
	}
	// Smart pointer types always allowed
	if !isAllowedExternalType("std::unique_ptr<Foo>") {
		t.Error("expected unique_ptr allowed")
	}
	// Not allowed
	if isAllowedExternalType("std::string") {
		t.Error("expected std::string not allowed")
	}
}

func TestIsStaticFactory(t *testing.T) {
	// Set up required global state
	prev := classQualNames
	prevSrcFiles := classSourceFiles
	prevAbstract := classAbstract
	prevNoNew := classNoNew
	prevBridgeCfg := bridgeConfig
	classQualNames = map[string]string{"Foo": "ns::Foo"}
	classSourceFiles = map[string]string{"ns::Foo": "mylib/foo.h"}
	classAbstract = map[string]bool{}
	classNoNew = map[string]bool{}
	bridgeConfig = DefaultBridgeConfig()
	defer func() {
		classQualNames = prev
		classSourceFiles = prevSrcFiles
		classAbstract = prevAbstract
		classNoNew = prevNoNew
		bridgeConfig = prevBridgeCfg
	}()

	// Non-static: false
	m := apispec.Function{
		IsStatic: false,
		Name:     "Create",
		QualName: "ns::Foo::Create",
		ReturnType: apispec.TypeRef{Kind: apispec.TypeHandle, Name: "Foo"},
	}
	if isStaticFactory(m) {
		t.Error("non-static should not be factory")
	}

	// Static returning own class: true
	m.IsStatic = true
	if !isStaticFactory(m) {
		t.Error("static method returning own class should be factory")
	}

	// Static but returns pointer: false
	m2 := m
	m2.ReturnType = apispec.TypeRef{Kind: apispec.TypeHandle, Name: "Foo", IsPointer: true}
	if isStaticFactory(m2) {
		t.Error("static returning pointer should not be factory")
	}

	// Static returning unrelated type: false
	m3 := m
	m3.ReturnType = apispec.TypeRef{Kind: apispec.TypeHandle, Name: "Bar"}
	if isStaticFactory(m3) {
		t.Error("static returning unrelated type should not be factory")
	}

	// Static but in skip list: false
	m4 := m
	m4.Name = "default_instance"
	m4.QualName = "ns::Foo::default_instance"
	if isStaticFactory(m4) {
		t.Error("default_instance should be skipped")
	}

	// Abstract return: false
	classAbstract = map[string]bool{"ns::Foo": true}
	if isStaticFactory(m) {
		t.Error("abstract factory return should not be factory")
	}
}

// TestIsStaticFactory_StatusOutParam guards Bug #4: a static method that
// follows the conventional C++ "Status f(in_args..., OutType* out)" idiom
// must be recognised as a factory for the containing class.
//
// The pattern looks like:
//
//	class Widget {
//	 public:
//	   static absl::Status Create(absl::string_view name,
//	                              std::unique_ptr<Widget>* out);
//	   // …
//	};
//
// The call returns the constructed object via the out-parameter and
// reports failure through the Status. The Go-facing equivalent should
// expose this as `NewWidget(...) (*Widget, error)`. Before the fix the
// predicate rejected such methods because (a) `absl::Status` matched
// `bridgeConfig.ErrorTypes` and (b) the original "factory must return
// the class by value" rule had no carve-out for the out-param idiom.
func TestIsStaticFactory_StatusOutParam(t *testing.T) {
	prev := classQualNames
	prevSrcFiles := classSourceFiles
	prevAbstract := classAbstract
	prevNoNew := classNoNew
	prevBridgeCfg := bridgeConfig
	classQualNames = map[string]string{"Widget": "mylib::Widget"}
	classSourceFiles = map[string]string{"mylib::Widget": "mylib/widget.h"}
	classAbstract = map[string]bool{}
	classNoNew = map[string]bool{}
	bridgeConfig = DefaultBridgeConfig()
	bridgeConfig.ErrorTypes = map[string]string{
		"absl::Status":   "if (!{result}.ok()) { ... }",
		"absl::StatusOr": "if (!{result}.ok()) { ... }",
	}
	defer func() {
		classQualNames = prev
		classSourceFiles = prevSrcFiles
		classAbstract = prevAbstract
		classNoNew = prevNoNew
		bridgeConfig = prevBridgeCfg
	}()

	// `static absl::Status Widget::Create(absl::string_view name,
	//                                     std::unique_ptr<Widget>* out)`.
	//
	// `result` is the conventional name in many libraries; the out-param
	// is `unique_ptr<Widget>*` — the canonical way to transfer ownership
	// of a freshly-constructed object back to the caller.
	m := apispec.Function{
		IsStatic: true,
		Name:     "Create",
		QualName: "mylib::Widget::Create",
		ReturnType: apispec.TypeRef{
			Name:     "absl::Status",
			Kind:     apispec.TypeValue,
			QualType: "absl::Status",
		},
		Params: []apispec.Param{
			{
				Name: "name",
				Type: apispec.TypeRef{
					Name:     "absl::string_view",
					Kind:     apispec.TypeString,
					QualType: "absl::string_view",
				},
			},
			{
				Name: "out",
				Type: apispec.TypeRef{
					Name:      "Widget",
					Kind:      apispec.TypeHandle,
					IsPointer: true,
					QualType:  "std::unique_ptr<Widget> *",
				},
			},
		},
	}
	if !isStaticFactory(m) {
		t.Error("Status f(in_args..., unique_ptr<T>*) on T must be recognised as a factory")
	}

	// And the same shape with a `T**` (raw pointer-to-pointer) out-param.
	m2 := m
	m2.Params = []apispec.Param{
		m.Params[0],
		{
			Name: "out",
			Type: apispec.TypeRef{
				Name:      "Widget",
				Kind:      apispec.TypeHandle,
				IsConst:   true,
				IsPointer: true,
				QualType:  "const Widget **",
			},
		},
	}
	if !isStaticFactory(m2) {
		t.Error("Status f(in_args..., T**) on T must be recognised as a factory")
	}

	// Negative: no out-param of own class type — still rejected. A static
	// method that just returns Status with no factory shape is not a
	// factory; treating it as one would generate a New<T> constructor
	// that yields nothing.
	m3 := m
	m3.Params = []apispec.Param{m.Params[0]}
	if isStaticFactory(m3) {
		t.Error("Status f(in_args...) with no own-class out-param must not be classified as a factory")
	}
}

func TestLookupValueClass(t *testing.T) {
	prev := valueClasses
	prevQualNames := classQualNames

	result := &apispec.Class{Name: "Result", QualName: "ns::Result"}
	valueClasses = map[string]*apispec.Class{
		"ns::Result": result,
		"Result":     result,
	}
	classQualNames = map[string]string{"Result": "ns::Result"}
	defer func() {
		valueClasses = prev
		classQualNames = prevQualNames
	}()

	// Direct qual name
	if lookupValueClass("ns::Result") == nil {
		t.Error("expected to find by qual name")
	}
	// Short name
	if lookupValueClass("Result") == nil {
		t.Error("expected to find by short name")
	}
	// With const/ptr/ref qualifiers
	if lookupValueClass("const Result*") == nil {
		t.Error("expected to find with const qualifier")
	}
	if lookupValueClass("Result&") == nil {
		t.Error("expected to find with ref")
	}
	// Unknown
	if lookupValueClass("Unknown") != nil {
		t.Error("expected nil for unknown")
	}
}

func TestReadExpr(t *testing.T) {
	prev := classQualNames
	classQualNames = nil
	defer func() { classQualNames = prev }()

	tests := []struct {
		name     string
		ref      apispec.TypeRef
		varName  string
		contains string
	}{
		{"bool", apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "bool"}, "x", "read_bool()"},
		{"float", apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "float"}, "x", "read_float()"},
		{"double", apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "double"}, "x", "read_double()"},
		{"int", apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"}, "x", "read_int32()"},
		{"uint32", apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "uint32_t"}, "x", "read_uint32()"},
		{"int64", apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int64_t"}, "x", "read_int64()"},
		{"uint64", apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "uint64_t"}, "x", "read_uint64()"},
		{"string", apispec.TypeRef{Kind: apispec.TypeString}, "s", "read_string()"},
		{"enum", apispec.TypeRef{Kind: apispec.TypeEnum, Name: "E"}, "e", "read_int32() - 1"},
		{"handle", apispec.TypeRef{Kind: apispec.TypeHandle, Name: "Foo"}, "h", "read_handle_ptr(reader)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := readExpr(tt.ref, tt.varName)
			if !strings.Contains(got, tt.contains) {
				t.Errorf("readExpr(%v, %q) = %q, expected to contain %q", tt.ref, tt.varName, got, tt.contains)
			}
			if !strings.Contains(got, tt.varName) {
				t.Errorf("expected var name %q in %q", tt.varName, got)
			}
		})
	}
}

// TestReadExpr_SetLikeContainer guards Bug #5B: when a parameter is
// classified by parseMapType as a set-like container (configured via
// BridgeConfig.SetLikeTypePrefixes), the bridge body must read each
// wire element as a handle and `insert(...)` it into the local
// container, mirroring how the proto schema side already emits
// `repeated <Handle>`.
//
// The earlier behaviour treated the whole parameter as an opaque
// `read_handle_ptr` (single uint64), which is incompatible with the
// repeated-handle wire format the proto schema declares. The two
// halves of the generator have to agree, and the bridge body is the
// half that was lagging.
func TestReadExpr_SetLikeContainer(t *testing.T) {
	prevCfg := bridgeConfig
	prevQual := classQualNames
	bridgeConfig = DefaultBridgeConfig()
	bridgeConfig.SetLikeTypePrefixes = []string{"absl::flat_hash_set"}
	classQualNames = map[string]string{"Foo": "ns::Foo"}
	defer func() {
		bridgeConfig = prevCfg
		classQualNames = prevQual
	}()

	ref := apispec.TypeRef{
		Kind:     apispec.TypeHandle, // clang misclassifies the set as handle
		Name:     "absl::flat_hash_set<const Foo *>",
		QualType: "const absl::flat_hash_set<const Foo *> &",
	}
	got := readExpr(ref, "labels")

	if !strings.Contains(got, "labels.insert(") {
		t.Errorf("expected set-like read to call .insert on the local container; got %q", got)
	}
	if !strings.Contains(got, "read_handle_ptr") {
		t.Errorf("expected each element to be read as a handle pointer; got %q", got)
	}
	if strings.Contains(got, "labels = read_handle_ptr") {
		t.Errorf("set-like read must not assign a single handle to the container variable; got %q", got)
	}
}

func TestWriteReturnExpr_Primitive(t *testing.T) {
	// Need bridgeConfig
	prev := bridgeConfig
	bridgeConfig = BridgeConfig{}
	defer func() { bridgeConfig = prev }()

	tests := []struct {
		name     string
		ref      apispec.TypeRef
		contains string
	}{
		{"bool", apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "bool"}, "write_bool"},
		{"float", apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "float"}, "write_float"},
		{"double", apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "double"}, "write_double"},
		{"int", apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"}, "write_int32"},
		{"int64", apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int64_t"}, "write_int64"},
		{"string", apispec.TypeRef{Kind: apispec.TypeString}, "write_string"},
		{"enum", apispec.TypeRef{Kind: apispec.TypeEnum, Name: "E"}, "write_int32"},
		{"handle ptr", apispec.TypeRef{Kind: apispec.TypeHandle, Name: "Foo", IsPointer: true}, "write_handle"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := writeReturnExpr(tt.ref, 1, "x")
			if !strings.Contains(got, tt.contains) {
				t.Errorf("writeReturnExpr(%v) = %q, expected to contain %q", tt.ref, got, tt.contains)
			}
		})
	}
}

func TestDiscoverLibraries(t *testing.T) {
	spec := &apispec.APISpec{
		Classes: []apispec.Class{
			{QualName: "proj::Foo", SourceFile: "proj/foo.h"},
			{QualName: "ns::Bar", SourceFile: "external/abseil-cpp/bar.h"},
			{QualName: "ns::Baz", SourceFile: "external/re2~/re2.h"},
		},
	}
	libs := DiscoverLibraries(spec)
	// abseil-cpp and re2 should be present
	if _, ok := libs["abseil-cpp"]; !ok {
		t.Error("expected abseil-cpp in libs")
	}
	if _, ok := libs["re2"]; !ok {
		t.Error("expected re2 in libs")
	}
	// All set to false
	for name, enabled := range libs {
		if enabled {
			t.Errorf("expected %s disabled by default", name)
		}
	}
}

func TestClassifyLibrary(t *testing.T) {
	// Project source
	if got := classifyLibrary("proj/foo.h"); got != "" {
		t.Errorf("expected empty for project, got %q", got)
	}
	// External dep
	if got := classifyLibrary("external/abseil-cpp~/foo.h"); got != "abseil-cpp" {
		t.Errorf("expected abseil-cpp, got %q", got)
	}
	if got := classifyLibrary("external/re2/re2.h"); got != "re2" {
		t.Errorf("expected re2, got %q", got)
	}
	// Empty
	if got := classifyLibrary(""); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestIsLibraryEnabled(t *testing.T) {
	prev := bridgeConfig
	defer func() { bridgeConfig = prev }()

	// Empty config → all enabled
	bridgeConfig = BridgeConfig{}
	if !isLibraryEnabled("abseil-cpp") {
		t.Error("empty config should enable all")
	}
	if !isLibraryEnabled("") {
		t.Error("empty (project) always enabled")
	}

	// With explicit false
	bridgeConfig = BridgeConfig{
		ExportDependentLibraries: map[string]bool{
			"abseil-cpp": false,
			"re2":        true,
		},
	}
	if isLibraryEnabled("abseil-cpp") {
		t.Error("expected abseil-cpp disabled")
	}
	if !isLibraryEnabled("re2") {
		t.Error("expected re2 enabled")
	}
	// Not listed → enabled by default
	if !isLibraryEnabled("unlisted") {
		t.Error("expected unlisted enabled")
	}
}

func TestIsInstantiableTypeForLocal(t *testing.T) {
	// Handle type is always instantiable (stored as uint64)
	h := apispec.TypeRef{Kind: apispec.TypeHandle}
	if !isInstantiableTypeForLocal(h) {
		t.Error("handle type should be instantiable as uint64")
	}
	// Primitive
	p := apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"}
	if !isInstantiableTypeForLocal(p) {
		t.Error("primitive should be instantiable")
	}
}

func TestIsInstantiableType(t *testing.T) {
	prev := classDeletedCopy
	prevAbstract := classAbstract
	prevNoDefCtor := classNoDefaultCtor
	prevQualNames := classQualNames
	prevConfig := bridgeConfig

	classQualNames = map[string]string{"Foo": "ns::Foo"}
	classDeletedCopy = map[string]bool{}
	classAbstract = map[string]bool{}
	classNoDefaultCtor = map[string]bool{}
	bridgeConfig = BridgeConfig{}
	defer func() {
		classDeletedCopy = prev
		classAbstract = prevAbstract
		classNoDefaultCtor = prevNoDefCtor
		classQualNames = prevQualNames
		bridgeConfig = prevConfig
	}()

	// Non-vector: always true
	ref := apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"}
	if !isInstantiableType(ref) {
		t.Error("primitive should be instantiable")
	}

	// Vector<int>: true
	vecInt := apispec.TypeRef{
		Kind:  apispec.TypeVector,
		Inner: &apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"},
	}
	if !isInstantiableType(vecInt) {
		t.Error("vector<int> should be instantiable")
	}

	// vector<Foo> where Foo has deleted copy → false
	classDeletedCopy = map[string]bool{"ns::Foo": true}
	vecFoo := apispec.TypeRef{
		Kind:  apispec.TypeVector,
		Inner: &apispec.TypeRef{Kind: apispec.TypeHandle, Name: "Foo"},
	}
	if isInstantiableType(vecFoo) {
		t.Error("vector<Foo> with deleted copy should not be instantiable")
	}

	// vector<Foo> where Foo is abstract → false
	classDeletedCopy = map[string]bool{}
	classAbstract = map[string]bool{"ns::Foo": true}
	if isInstantiableType(vecFoo) {
		t.Error("vector<Foo> abstract should not be instantiable")
	}

	// vector<Foo> where Foo is a HANDLE type with no default ctor → true.
	// Handle-typed vector elements are populated via push_back from caller-
	// supplied handle pointers (see writeReadVectorBody); default
	// construction is never needed, so the no-default-ctor flag does not
	// block bridge generation.
	classAbstract = map[string]bool{}
	classNoDefaultCtor = map[string]bool{"ns::Foo": true}
	if !isInstantiableType(vecFoo) {
		t.Error("vector<Foo> handle + no-default-ctor SHOULD be instantiable (push_back from caller handles)")
	}
	// vector<Foo> where Foo is a VALUE class with no default ctor AND
	// no public fields → still false, because there's no way to
	// emplace_back field-by-field.
	vecFooValue := apispec.TypeRef{
		Kind:  apispec.TypeVector,
		Inner: &apispec.TypeRef{Kind: apispec.TypeValue, Name: "Foo"},
	}
	if isInstantiableType(vecFooValue) {
		t.Error("vector<Foo> value + no-default-ctor + no fields should not be instantiable")
	}

	// vector<unique_ptr<Foo>> → instantiable. unique_ptr's move-only
	// semantics make vector's copy-ctor instantiation a non-issue,
	// and the bridge transports each element as a uint64 handle that
	// the C++ side wraps via `unique_ptr<T>(reinterpret_cast<T*>(_h))`.
	// (See TestIsInstantiableType_VectorOfUniquePointer for a richer
	// fixture with classQualNames populated.)
	classNoDefaultCtor = map[string]bool{}
	vecUnique := apispec.TypeRef{
		Kind:  apispec.TypeVector,
		Inner: &apispec.TypeRef{Kind: apispec.TypeHandle, Name: "std::unique_ptr<Foo>"},
	}
	if !isInstantiableType(vecUnique) {
		t.Error("vector<unique_ptr<T>> must be instantiable")
	}

	// vector<shared_ptr<Foo>> → still rejected: shared_ptr has no
	// transport across the wasm boundary today.
	vecShared := apispec.TypeRef{
		Kind:  apispec.TypeVector,
		Inner: &apispec.TypeRef{Kind: apispec.TypeHandle, Name: "std::shared_ptr<Foo>"},
	}
	if isInstantiableType(vecShared) {
		t.Error("vector<shared_ptr> must remain rejected")
	}
}

func TestIsUsableType_PrimitiveAndPointer(t *testing.T) {
	// void* → not usable
	r := apispec.TypeRef{Kind: apispec.TypeVoid, IsPointer: true}
	if isUsableType(r) {
		t.Error("void* should not be usable")
	}

	// void → usable
	if !isUsableType(apispec.TypeRef{Kind: apispec.TypeVoid}) {
		t.Error("void should be usable")
	}

	// primitive, string, enum → usable
	for _, k := range []apispec.TypeKind{apispec.TypePrimitive, apispec.TypeString, apispec.TypeEnum} {
		if !isUsableType(apispec.TypeRef{Kind: k}) {
			t.Errorf("kind %s should be usable", k)
		}
	}

	// vector with no inner → false
	if isUsableType(apispec.TypeRef{Kind: apispec.TypeVector}) {
		t.Error("vector with no inner should not be usable")
	}

	// vector<int> → usable
	if !isUsableType(apispec.TypeRef{
		Kind:  apispec.TypeVector,
		Inner: &apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"},
	}) {
		t.Error("vector<int> should be usable")
	}

	// unknown → not usable
	if isUsableType(apispec.TypeRef{Kind: apispec.TypeUnknown}) {
		t.Error("unknown should not be usable")
	}
}

// TestIsUsableType_ExternalHandlesViaConfig demonstrates the existing
// opt-in path for accepting handle types whose declaring header lives
// outside the project's own sources: the consumer adds the type's
// fully-qualified name (or a prefix that the type's spelling matches)
// to `BridgeConfig.ExternalTypes` in `wasmify.json`. This is the
// generic mechanism wasmify already provides for exposing
// dependency-owned types (e.g. `absl::Status`, `absl::Span`) through
// the bridge.
//
// Protobuf reflection types (`google::protobuf::Descriptor` etc.) are
// a real-world example: any C++ library that integrates protobuf uses
// them in its public API to register message or enum types. With the
// types listed in `ExternalTypes`, the bridge filter accepts them as
// opaque handles end-to-end.
//
// This test guards the existing behaviour and serves as a regression
// fixture for any future filter logic change: project-default rejects
// unregistered external handles; explicit registration in
// `ExternalTypes` flips them through.
func TestIsUsableType_ExternalHandlesViaConfig(t *testing.T) {
	prevSrcFiles := classSourceFiles
	prevEnums := enumQualNames
	prevCfg := bridgeConfig
	classSourceFiles = map[string]string{}
	enumQualNames = map[string]bool{}
	bridgeConfig = DefaultBridgeConfig()
	bridgeConfig.ExternalTypes = []string{
		"google::protobuf::Descriptor",
		"google::protobuf::DescriptorPool",
		"google::protobuf::EnumDescriptor",
		"google::protobuf::FieldDescriptor",
		"google::protobuf::FileDescriptor",
		"google::protobuf::FileDescriptorProto",
		"google::protobuf::FileDescriptorSet",
		"google::protobuf::OneofDescriptor",
		"google::protobuf::EnumValueDescriptor",
	}
	defer func() {
		classSourceFiles = prevSrcFiles
		enumQualNames = prevEnums
		bridgeConfig = prevCfg
	}()

	cases := []apispec.TypeRef{
		{
			Name:      "google::protobuf::Descriptor",
			Kind:      apispec.TypeHandle,
			IsConst:   true,
			IsPointer: true,
			QualType:  "const google::protobuf::Descriptor *",
		},
		{
			Name:      "google::protobuf::EnumDescriptor",
			Kind:      apispec.TypeHandle,
			IsConst:   true,
			IsPointer: true,
			QualType:  "const google::protobuf::EnumDescriptor *",
		},
		{
			Name:      "google::protobuf::DescriptorPool",
			Kind:      apispec.TypeHandle,
			IsPointer: true,
			QualType:  "google::protobuf::DescriptorPool *",
		},
		{
			Name:      "google::protobuf::FileDescriptorSet",
			Kind:      apispec.TypeHandle,
			IsPointer: true,
			QualType:  "google::protobuf::FileDescriptorSet *",
		},
	}
	for _, ref := range cases {
		t.Run(ref.Name, func(t *testing.T) {
			if !isUsableType(ref) {
				t.Errorf("isUsableType(%q) = false; an external handle listed in ExternalTypes must pass through the filter", ref.QualType)
			}
		})
	}

	// Negative: a handle type that isn't in the project's sources AND
	// isn't registered in ExternalTypes is rejected. The user must
	// list it explicitly to expose it through the bridge.
	t.Run("unregistered_external_handle_rejected", func(t *testing.T) {
		ref := apispec.TypeRef{
			Name:      "vendor::lib::SomeClass",
			Kind:      apispec.TypeHandle,
			IsPointer: true,
			QualType:  "vendor::lib::SomeClass *",
		}
		if isUsableType(ref) {
			t.Error("unregistered external handle must remain rejected — explicit ExternalTypes registration is the intended opt-in")
		}
	})
}

// TestIsInstantiableType_VectorOfUniquePointer guards Bug #5C: a
// `std::vector<std::unique_ptr<T>>` parameter is the canonical
// shape any C++ library uses to hand a list of freshly-owned
// objects to a constructor (factories that produce sole-owned
// instances which the receiver then takes ownership of).
//
// The bridge filter previously rejected every vector with a
// smart-pointer inner because the deleted-copy-ctor concern that
// motivates the filter (vector's internal copy-ctor instantiation
// breaks at compile time when T's copy ctor is deleted) does NOT
// apply to `unique_ptr<T>`: unique_ptr is move-only, vector's copy
// is never instantiated unless the user tries to copy the vector,
// and the bridge never copies the vector itself.
//
// The proto-schema side already emits `repeated <Handle>` for the
// same parameter (smart_pointer wrappers route through
// `repeated <Inner>` because the wire only carries the inner
// pointee handle). The bridge filter has to agree.
func TestIsInstantiableType_VectorOfUniquePointer(t *testing.T) {
	prevAbstract := classAbstract
	prevDeletedCopy := classDeletedCopy
	prevNoDefault := classNoDefaultCtor
	prevQual := classQualNames
	classQualNames = map[string]string{"Foo": "ns::Foo"}
	classAbstract = map[string]bool{}
	classDeletedCopy = map[string]bool{}
	classNoDefaultCtor = map[string]bool{}
	defer func() {
		classAbstract = prevAbstract
		classDeletedCopy = prevDeletedCopy
		classNoDefaultCtor = prevNoDefault
		classQualNames = prevQual
	}()

	ref := apispec.TypeRef{
		Name:     "std::vector<std::unique_ptr<const Foo>>",
		Kind:     apispec.TypeVector,
		QualType: "std::vector<std::unique_ptr<const Foo>>",
		Inner: &apispec.TypeRef{
			Name:     "std::unique_ptr<const Foo>",
			Kind:     apispec.TypeHandle,
			QualType: "std::unique_ptr<const Foo>",
		},
	}
	if !isInstantiableType(ref) {
		t.Error("vector<unique_ptr<T>> must be instantiable — unique_ptr's move-only semantics mean the deleted-copy concern does not apply")
	}
}

// TestIsInstantiableType_VectorOfSharedPointerStillRejected confirms
// that `vector<shared_ptr<T>>` remains rejected. shared_ptr has a
// copy constructor (it bumps a refcount), so the deleted-copy concern
// does not apply, but the bridge has no transport-level handling for
// shared ownership across the wasm boundary today: the unique-ptr
// path can release() into a raw pointer, but shared_ptr requires
// keeping a heap-allocated control block alive on both sides. Until
// that's wired up, vectors of shared_ptr stay out.
func TestIsInstantiableType_VectorOfSharedPointerStillRejected(t *testing.T) {
	prevQual := classQualNames
	classQualNames = map[string]string{"Foo": "ns::Foo"}
	defer func() { classQualNames = prevQual }()

	ref := apispec.TypeRef{
		Name:     "std::vector<std::shared_ptr<const Foo>>",
		Kind:     apispec.TypeVector,
		QualType: "std::vector<std::shared_ptr<const Foo>>",
		Inner: &apispec.TypeRef{
			Name:     "std::shared_ptr<const Foo>",
			Kind:     apispec.TypeHandle,
			QualType: "std::shared_ptr<const Foo>",
		},
	}
	if isInstantiableType(ref) {
		t.Error("vector<shared_ptr<T>> must remain rejected for now — the bridge has no shared-ownership transport")
	}
}

// TestIsInstantiableType_VectorOfAbstractPointer guards a regression
// in the vector-element instantiability check. A `vector<T*>` stores
// *pointers* to T — the vector never default-constructs, copies, or
// destroys T values, only pointer slots. So properties of T like
// `is_abstract`, `has_deleted_copy_ctor`, or `has_public_default_ctor`
// are irrelevant for the pointer-wrapped form.
//
// The check originally rejected any `vector<T>` whose T was abstract,
// uncopyable, or non-default-constructible, without distinguishing
// the pointer-element case. That over-rejection hid every method
// taking `vector<AbstractBase*>` from the bridge — a common idiom
// for caller-supplied lists of polymorphic objects.
func TestIsInstantiableType_VectorOfAbstractPointer(t *testing.T) {
	prevAbstract := classAbstract
	prevDeletedCopy := classDeletedCopy
	prevNoDefault := classNoDefaultCtor
	prevQual := classQualNames
	classQualNames = map[string]string{"AbstractBase": "ns::AbstractBase"}
	classAbstract = map[string]bool{"ns::AbstractBase": true}
	classDeletedCopy = map[string]bool{"ns::AbstractBase": true}
	classNoDefaultCtor = map[string]bool{"ns::AbstractBase": true}
	defer func() {
		classAbstract = prevAbstract
		classDeletedCopy = prevDeletedCopy
		classNoDefaultCtor = prevNoDefault
		classQualNames = prevQual
	}()

	vectorOfAbstractPtr := apispec.TypeRef{
		Name:     "std::vector<AbstractBase *>",
		Kind:     apispec.TypeVector,
		QualType: "const std::vector<AbstractBase *> &",
		Inner: &apispec.TypeRef{
			Name:      "AbstractBase",
			Kind:      apispec.TypeHandle,
			IsPointer: true,
			QualType:  "AbstractBase *",
		},
	}
	if !isInstantiableType(vectorOfAbstractPtr) {
		t.Error("vector<T*> must be instantiable even when T itself is abstract / non-copyable / non-default-constructible — the vector stores pointer slots, not T values")
	}

	// Sanity check: vector of T-by-value where T is abstract is still
	// rejected (you can't store an abstract instance by value).
	vectorOfAbstractValue := apispec.TypeRef{
		Name:     "std::vector<AbstractBase>",
		Kind:     apispec.TypeVector,
		QualType: "const std::vector<AbstractBase> &",
		Inner: &apispec.TypeRef{
			Name:     "AbstractBase",
			Kind:     apispec.TypeValue,
			QualType: "AbstractBase",
		},
	}
	if isInstantiableType(vectorOfAbstractValue) {
		t.Error("vector<T> by-value of an abstract T must remain rejected — only the pointer form is safe")
	}
}

func TestIsReturnableType(t *testing.T) {
	// Primitive pointer: not returnable (ambiguous ownership)
	r := apispec.TypeRef{Kind: apispec.TypePrimitive, IsPointer: true}
	if isReturnableType(r) {
		t.Error("primitive* should not be returnable")
	}
	// Plain primitive: returnable
	if !isReturnableType(apispec.TypeRef{Kind: apispec.TypePrimitive}) {
		t.Error("primitive should be returnable")
	}
}

func TestIsBridgeableClass_NoNamespace(t *testing.T) {
	c := apispec.Class{Name: "Foo", QualName: "Foo", SourceFile: "mylib/foo.h"}
	if isBridgeableClass(&c) {
		t.Error("class without namespace should not be bridgeable")
	}
}

func TestIsBridgeableClass_Internal(t *testing.T) {
	c := apispec.Class{
		Name: "Foo", QualName: "ns::internal_base::Foo",
		Namespace: "ns::internal_base", SourceFile: "mylib/foo.h",
	}
	if isBridgeableClass(&c) {
		t.Error("internal_ class should not be bridgeable")
	}
}

func TestIsBridgeableClass_NoDtor(t *testing.T) {
	// Classes without a public destructor (factory-owned types like
	// googlesql::ArrayType / StructType / EnumType) are bridgeable —
	// they just can't be manually freed from the Go side. The Free
	// RPC is skipped for them in writeHandleService; methods are
	// still exposed.
	c := apispec.Class{
		Name: "Foo", QualName: "ns::Foo", Namespace: "ns",
		SourceFile:    "mylib/foo.h",
		HasPublicDtor: false,
	}
	if !isBridgeableClass(&c) {
		t.Error("class with non-public dtor should still be bridgeable; Free RPC handled separately")
	}
}

func TestBuildClassNoDefaultCtor(t *testing.T) {
	spec := &apispec.APISpec{
		Classes: []apispec.Class{
			{QualName: "ns::A", HasPublicDefaultCtor: true},
			{QualName: "ns::B", HasPublicDefaultCtor: false},
			{
				QualName: "ns::C", HasPublicDefaultCtor: true,
				Fields: []apispec.Field{
					{Name: "f", Type: apispec.TypeRef{IsRef: true}},
				},
			},
		},
	}
	m := buildClassNoDefaultCtorMap(spec)
	if m["ns::A"] {
		t.Error("A has default ctor")
	}
	if !m["ns::B"] {
		t.Error("B has no default ctor")
	}
	if !m["ns::C"] {
		t.Error("C has reference-type field → no default ctor")
	}
}

func TestBuildClassDeletedCopy_Explicit(t *testing.T) {
	spec := &apispec.APISpec{
		Classes: []apispec.Class{
			{QualName: "ns::A", HasDeletedCopyCtor: true},
			{QualName: "ns::B"},
		},
	}
	m := buildClassDeletedCopyMap(spec)
	if !m["ns::A"] {
		t.Error("A should be in deleted-copy map")
	}
	if m["ns::B"] {
		t.Error("B should not be in deleted-copy map")
	}
}

func TestBuildClassDeletedCopy_PropagateSmartPointer(t *testing.T) {
	spec := &apispec.APISpec{
		Classes: []apispec.Class{
			{
				QualName: "ns::A",
				Fields: []apispec.Field{
					{Name: "f", Type: apispec.TypeRef{QualType: "std::unique_ptr<X>"}},
				},
			},
		},
	}
	m := buildClassDeletedCopyMap(spec)
	if !m["ns::A"] {
		t.Error("A contains unique_ptr → implicitly non-copyable")
	}
}

func TestBuildClassPrivateDtor(t *testing.T) {
	spec := &apispec.APISpec{
		Classes: []apispec.Class{
			{QualName: "ns::A", HasPublicDtor: true},
			{QualName: "ns::B", HasPublicDtor: false},
		},
	}
	m := buildClassPrivateDtorMap(spec)
	if m["ns::A"] {
		t.Error("A has public dtor")
	}
	if !m["ns::B"] {
		t.Error("B has private dtor")
	}
}

func TestBuildClassNoNew(t *testing.T) {
	spec := &apispec.APISpec{
		Classes: []apispec.Class{
			{QualName: "ns::A", HasDeletedOperatorNew: true},
			{QualName: "ns::B", Parent: "ns::A"}, // inherits no-new
			{QualName: "ns::C"}, // unrelated
		},
	}
	m := buildClassNoNewMap(spec)
	if !m["ns::A"] {
		t.Error("A has deleted new")
	}
	if !m["ns::B"] {
		t.Error("B should inherit from A")
	}
	if m["ns::C"] {
		t.Error("C unrelated")
	}
}

func TestBuildClassQualNameMap(t *testing.T) {
	spec := &apispec.APISpec{
		Namespace: "ns",
		Classes: []apispec.Class{
			{Name: "Foo", QualName: "ns::Foo"},
			{Name: "Foo", QualName: "ns::other::Foo"}, // ambiguous
			{Name: "Bar", QualName: "ns::Bar"},
		},
	}
	prev := specNamespace
	specNamespace = "ns"
	defer func() { specNamespace = prev }()

	m := buildClassQualNameMap(spec)
	// Foo should resolve to top-level (ns::Foo prefers over ns::other::Foo)
	if m["Foo"] != "ns::Foo" {
		t.Errorf("expected ns::Foo for short Foo, got %q", m["Foo"])
	}
	if m["Bar"] != "ns::Bar" {
		t.Errorf("expected ns::Bar, got %q", m["Bar"])
	}
	// Ambiguous flag set
	if !ambiguousShortNames["Foo"] {
		t.Error("Foo should be marked ambiguous")
	}
}

func TestNormalizeHeaderPath(t *testing.T) {
	tests := []struct {
		path, projectRoot, want string
	}{
		{"", "", ""},
		{"mylib/foo.h", "", "mylib/foo.h"},
		{"/proj/mylib/foo.h", "/proj", "mylib/foo.h"},
		{"/unrelated/foo.h", "/proj", "/unrelated/foo.h"},
		{"bazel-out/k8-fastbuild/bin/mylib/foo.h", "", "mylib/foo.h"},
		{"/root/execroot/_main/mylib/foo.h", "", "mylib/foo.h"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := normalizeHeaderPath(tt.path, tt.projectRoot)
			if got != tt.want {
				t.Errorf("normalizeHeaderPath(%q, %q) = %q, want %q", tt.path, tt.projectRoot, got, tt.want)
			}
		})
	}
}

// TestWriteCallBody_FreeFunction drives writeCallBody for a free function
// (no handle class). Exercises the non-method parsing path.
func TestWriteCallBody_FreeFunction(t *testing.T) {
	prev := classQualNames
	prevSrc := classSourceFiles
	prevAbs := classAbstract
	prevND := classNoDefaultCtor
	prevDC := classDeletedCopy
	prevNoNew := classNoNew
	prevCfg := bridgeConfig
	prevVC := valueClasses
	classQualNames = map[string]string{}
	classSourceFiles = map[string]string{}
	classAbstract = map[string]bool{}
	classNoDefaultCtor = map[string]bool{}
	classDeletedCopy = map[string]bool{}
	classNoNew = map[string]bool{}
	bridgeConfig = BridgeConfig{}
	valueClasses = map[string]*apispec.Class{}
	defer func() {
		classQualNames = prev
		classSourceFiles = prevSrc
		classAbstract = prevAbs
		classNoDefaultCtor = prevND
		classDeletedCopy = prevDC
		classNoNew = prevNoNew
		bridgeConfig = prevCfg
		valueClasses = prevVC
	}()

	fn := &apispec.Function{
		Name:     "add",
		QualName: "mylib::add",
		Params: []apispec.Param{
			{Name: "a", Type: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"}},
			{Name: "b", Type: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"}},
		},
		ReturnType: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"},
	}
	var b strings.Builder
	spec := &apispec.APISpec{}
	writeCallBody(&b, fn, "", spec, "        ")
	code := b.String()
	// Must contain ProtoReader and response writer
	if !strings.Contains(code, "ProtoReader reader") {
		t.Error("expected ProtoReader")
	}
	if !strings.Contains(code, "mylib::add(") {
		t.Error("expected call to mylib::add")
	}
	if !strings.Contains(code, "_pw.finish()") {
		t.Error("expected _pw.finish()")
	}
}

// TestWriteCallBody_Method drives writeCallBody for a method (handleClass
// set). Exercises the _handle_ptr / _self-cast path.
func TestWriteCallBody_Method(t *testing.T) {
	prev := classQualNames
	prevSrc := classSourceFiles
	prevAbs := classAbstract
	prevND := classNoDefaultCtor
	prevDC := classDeletedCopy
	prevNoNew := classNoNew
	prevCfg := bridgeConfig
	prevVC := valueClasses
	classQualNames = map[string]string{"Foo": "mylib::Foo"}
	classSourceFiles = map[string]string{}
	classAbstract = map[string]bool{}
	classNoDefaultCtor = map[string]bool{}
	classDeletedCopy = map[string]bool{}
	classNoNew = map[string]bool{}
	bridgeConfig = BridgeConfig{}
	valueClasses = map[string]*apispec.Class{}
	defer func() {
		classQualNames = prev
		classSourceFiles = prevSrc
		classAbstract = prevAbs
		classNoDefaultCtor = prevND
		classDeletedCopy = prevDC
		classNoNew = prevNoNew
		bridgeConfig = prevCfg
		valueClasses = prevVC
	}()

	fn := &apispec.Function{
		Name:     "Value",
		QualName: "mylib::Foo::Value",
		IsConst:  true,
		ReturnType: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"},
	}
	var b strings.Builder
	spec := &apispec.APISpec{}
	writeCallBody(&b, fn, "mylib::Foo", spec, "        ")
	code := b.String()
	// Must contain null-handle check + _self cast
	if !strings.Contains(code, "_handle_ptr") {
		t.Error("expected _handle_ptr")
	}
	if !strings.Contains(code, "_self->Value()") {
		t.Error("expected _self->Value() call")
	}
	if !strings.Contains(code, "reinterpret_cast") {
		t.Error("expected reinterpret_cast")
	}
}

func TestIsOutputParam_More(t *testing.T) {
	tests := []struct {
		name string
		p    apispec.Param
		want bool
	}{
		{"non-pointer", apispec.Param{Type: apispec.TypeRef{Kind: apispec.TypeHandle}}, false},
		{"handle *", apispec.Param{Type: apispec.TypeRef{Kind: apispec.TypeHandle, IsPointer: true, QualType: "Foo*"}}, false},
		{"const handle *", apispec.Param{Type: apispec.TypeRef{Kind: apispec.TypeHandle, IsPointer: true, IsConst: true, QualType: "const Foo*"}}, false},
		{"string (not output)", apispec.Param{Type: apispec.TypeRef{Kind: apispec.TypeString, IsPointer: true, QualType: "std::string*"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isOutputParam(tt.p); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWriteFieldExpr(t *testing.T) {
	// Handle with unique_ptr
	r := apispec.TypeRef{Kind: apispec.TypeHandle, QualType: "std::unique_ptr<Foo>"}
	got := writeFieldExpr(r, 1, "x")
	if !strings.Contains(got, "release()") {
		t.Errorf("expected .release() for unique_ptr, got %q", got)
	}
	// Handle with shared_ptr
	r = apispec.TypeRef{Kind: apispec.TypeHandle, QualType: "std::shared_ptr<Foo>"}
	got = writeFieldExpr(r, 1, "x")
	if !strings.Contains(got, "get()") {
		t.Errorf("expected .get() for shared_ptr, got %q", got)
	}
	// T** pattern
	r = apispec.TypeRef{Kind: apispec.TypeHandle, QualType: "Foo **"}
	got = writeFieldExpr(r, 1, "x")
	if !strings.Contains(got, "write_handle") {
		t.Errorf("expected write_handle, got %q", got)
	}
	// primitive fallback goes through writeReturnExpr
	r = apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int", IsPointer: true}
	got = writeFieldExpr(r, 1, "x")
	if !strings.Contains(got, "write_int32") {
		t.Errorf("expected write_int32, got %q", got)
	}
}

func TestWriteVectorReturnExpr(t *testing.T) {
	prev := classQualNames
	prevConfig := bridgeConfig
	classQualNames = map[string]string{}
	bridgeConfig = BridgeConfig{}
	defer func() {
		classQualNames = prev
		bridgeConfig = prevConfig
	}()

	// No inner → TODO comment
	r := apispec.TypeRef{Kind: apispec.TypeVector}
	got := writeVectorReturnExpr(r, 1, "x")
	if !strings.Contains(got, "TODO") {
		t.Errorf("expected TODO, got %q", got)
	}

	// Vector<int>
	r = apispec.TypeRef{
		Kind:  apispec.TypeVector,
		Inner: &apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"},
	}
	got = writeVectorReturnExpr(r, 1, "x")
	if !strings.Contains(got, "write_repeated_int32") {
		t.Errorf("expected write_repeated_int32, got %q", got)
	}

	// Vector<bool>
	r = apispec.TypeRef{
		Kind:  apispec.TypeVector,
		Inner: &apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "bool"},
	}
	got = writeVectorReturnExpr(r, 1, "x")
	if !strings.Contains(got, "write_repeated_bool") {
		t.Errorf("expected write_repeated_bool, got %q", got)
	}

	// Vector<double>
	r = apispec.TypeRef{
		Kind:  apispec.TypeVector,
		Inner: &apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "double"},
	}
	got = writeVectorReturnExpr(r, 1, "x")
	if !strings.Contains(got, "write_repeated_double") {
		t.Errorf("expected write_repeated_double, got %q", got)
	}

	// Vector<string>
	r = apispec.TypeRef{
		Kind:  apispec.TypeVector,
		Inner: &apispec.TypeRef{Kind: apispec.TypeString},
	}
	got = writeVectorReturnExpr(r, 1, "x")
	if !strings.Contains(got, "write_repeated_string") {
		t.Errorf("expected write_repeated_string, got %q", got)
	}

	// Vector<Handle*>
	r = apispec.TypeRef{
		Kind:  apispec.TypeVector,
		Inner: &apispec.TypeRef{Kind: apispec.TypeHandle, Name: "Foo", IsPointer: true},
	}
	got = writeVectorReturnExpr(r, 1, "x")
	if !strings.Contains(got, "write_handle") {
		t.Errorf("expected write_handle, got %q", got)
	}

	// Vector<Enum>
	r = apispec.TypeRef{
		Kind:  apispec.TypeVector,
		Inner: &apispec.TypeRef{Kind: apispec.TypeEnum, Name: "E"},
	}
	got = writeVectorReturnExpr(r, 1, "x")
	if !strings.Contains(got, "write_repeated_int32") {
		t.Errorf("expected enum→int32 packed, got %q", got)
	}
}

func TestWriteValueFieldExpr(t *testing.T) {
	// Primitive bool
	r := apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "bool"}
	got := writeValueFieldExpr(r, 1, "x.field", "_sub")
	if !strings.Contains(got, "write_bool") {
		t.Errorf("got %q", got)
	}

	// Primitive double
	r = apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "double"}
	got = writeValueFieldExpr(r, 1, "x.field", "_sub")
	if !strings.Contains(got, "write_double") {
		t.Errorf("got %q", got)
	}

	// String
	r = apispec.TypeRef{Kind: apispec.TypeString}
	got = writeValueFieldExpr(r, 1, "x.field", "_sub")
	if !strings.Contains(got, "write_string") {
		t.Errorf("got %q", got)
	}

	// Enum (shift +1)
	r = apispec.TypeRef{Kind: apispec.TypeEnum, Name: "E"}
	got = writeValueFieldExpr(r, 1, "x.field", "_sub")
	if !strings.Contains(got, "write_int32") || !strings.Contains(got, "+ 1") {
		t.Errorf("enum should write int32 +1, got %q", got)
	}

	// Handle pointer
	r = apispec.TypeRef{Kind: apispec.TypeHandle, IsPointer: true}
	got = writeValueFieldExpr(r, 1, "x.field", "_sub")
	if !strings.Contains(got, "write_handle") {
		t.Errorf("got %q", got)
	}

	// Handle by-value: TODO
	r = apispec.TypeRef{Kind: apispec.TypeHandle}
	got = writeValueFieldExpr(r, 1, "x.field", "_sub")
	if !strings.Contains(got, "TODO") {
		t.Errorf("expected TODO for by-value handle, got %q", got)
	}
}

func TestCollectAllSourceFiles(t *testing.T) {
	spec := &apispec.APISpec{
		Functions: []apispec.Function{{SourceFile: "a.h"}, {SourceFile: "b.h"}},
		Classes: []apispec.Class{
			{SourceFile: "c.h", Methods: []apispec.Function{{SourceFile: "d.h"}}},
		},
		Enums: []apispec.Enum{{SourceFile: "e.h"}},
	}
	got := collectAllSourceFiles(spec, "")
	wantSet := map[string]bool{"a.h": true, "b.h": true, "c.h": true, "d.h": true, "e.h": true}
	if len(got) != len(wantSet) {
		t.Errorf("expected %d files, got %v", len(wantSet), got)
	}
	for _, g := range got {
		if !wantSet[g] {
			t.Errorf("unexpected: %q", g)
		}
	}
}

func TestToCIdentifier(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"foo", "foo"},
		{"foo123", "foo123"},
		{"foo_bar", "foo_bar"},
		{"foo-bar", "foo_bar"},
		{"foo bar", "foo_bar"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := toCIdentifier(tt.in); got != tt.want {
				t.Errorf("toCIdentifier(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestToMethodConstName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"foo", "FOO"},
		{"fooBar", "FOO_BAR"},
		{"compute_result", "COMPUTE_RESULT"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := toMethodConstName(tt.in); got != tt.want {
				t.Errorf("toMethodConstName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCppTypeNameInContext(t *testing.T) {
	prev := classQualNames
	classQualNames = map[string]string{"Foo": "ns::Foo"}
	defer func() { classQualNames = prev }()

	// Empty context falls through to cppTypeName
	got := cppTypeNameInContext(apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"}, "")
	if got != "int32_t" {
		t.Errorf("got %q", got)
	}

	// With context, same-class resolution
	got = cppTypeNameInContext(apispec.TypeRef{Kind: apispec.TypeHandle, Name: "Bar", QualType: "Bar"}, "ns::other::Bar")
	if got != "ns::other::Bar" {
		t.Errorf("expected ns::other::Bar, got %q", got)
	}

	// String
	got = cppTypeNameInContext(apispec.TypeRef{Kind: apispec.TypeString}, "ns::X")
	if got != "std::string" {
		t.Errorf("expected std::string, got %q", got)
	}

	// Enum
	got = cppTypeNameInContext(apispec.TypeRef{Kind: apispec.TypeEnum, Name: "E"}, "ns::X")
	if got != "E" {
		t.Errorf("expected E, got %q", got)
	}

	// Empty qualType and name
	got = cppTypeNameInContext(apispec.TypeRef{Kind: apispec.TypeHandle}, "ns::X")
	if got != "/* unknown type */" {
		t.Errorf("got %q", got)
	}
}

func TestCppLocalTypeInContext(t *testing.T) {
	prev := classQualNames
	classQualNames = nil
	defer func() { classQualNames = prev }()

	// Empty context delegates to cppLocalType
	got := cppLocalTypeInContext(apispec.TypeRef{Kind: apispec.TypeHandle}, "")
	if got != "uint64_t" {
		t.Errorf("got %q", got)
	}

	// Handle type with context
	got = cppLocalTypeInContext(apispec.TypeRef{Kind: apispec.TypeHandle}, "ns::X")
	if got != "uint64_t" {
		t.Errorf("handle should always be uint64_t, got %q", got)
	}

	// Primitive
	got = cppLocalTypeInContext(apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"}, "ns::X")
	if got != "int32_t" {
		t.Errorf("got %q", got)
	}

	// String
	got = cppLocalTypeInContext(apispec.TypeRef{Kind: apispec.TypeString}, "ns::X")
	if got != "std::string" {
		t.Errorf("got %q", got)
	}

	// Void
	got = cppLocalTypeInContext(apispec.TypeRef{Kind: apispec.TypeVoid}, "ns::X")
	if got != "void" {
		t.Errorf("got %q", got)
	}
}

func TestResolveTypeNameInContextForValue(t *testing.T) {
	prev := classQualNames
	classQualNames = map[string]string{"Foo": "ns::Foo"}
	defer func() { classQualNames = prev }()

	// Empty map case: returns as-is
	classQualNames = nil
	got := resolveTypeNameInContextForValue("Foo", "")
	if got != "Foo" {
		t.Errorf("got %q", got)
	}

	// With non-nil map, basic resolution
	classQualNames = map[string]string{"Foo": "ns::Foo"}
	got = resolveTypeNameInContextForValue("Foo", "")
	if got != "ns::Foo" {
		t.Errorf("got %q", got)
	}

	// Template args
	got = resolveTypeNameInContextForValue("std::unique_ptr<Foo>", "ns::Bar")
	if !contains2(got, "Foo") {
		t.Errorf("got %q", got)
	}

	// Already qualified
	got = resolveTypeNameInContextForValue("already::qualified", "ns::X")
	if got != "already::qualified" {
		t.Errorf("got %q", got)
	}

	// Context-aware: same-class resolution
	got = resolveTypeNameInContextForValue("Bar", "ns::other::Bar")
	if got != "ns::other::Bar" {
		t.Errorf("got %q", got)
	}
}

func contains2(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestQualifyTemplateArgsInContext(t *testing.T) {
	prev := classQualNames
	classQualNames = map[string]string{"Foo": "ns::Foo"}
	defer func() { classQualNames = prev }()

	// Empty context delegates to qualifyTemplateArgs
	got := qualifyTemplateArgsInContext("std::unique_ptr<Foo>", "")
	if got != "std::unique_ptr<ns::Foo>" {
		t.Errorf("got %q", got)
	}

	// With context
	got = qualifyTemplateArgsInContext("std::unique_ptr<Foo>", "ns::Bar")
	if got != "std::unique_ptr<ns::Foo>" {
		t.Errorf("got %q", got)
	}

	// No template brackets: return as-is
	got = qualifyTemplateArgsInContext("Plain", "ns::X")
	if got != "Plain" {
		t.Errorf("got %q", got)
	}
}

func TestCppTypeNameInContextForValue(t *testing.T) {
	prev := classQualNames
	classQualNames = map[string]string{"Foo": "ns::Foo"}
	defer func() { classQualNames = prev }()

	// Primitive
	got := cppTypeNameInContextForValue(apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"}, "ns::X")
	if got != "int32_t" {
		t.Errorf("got %q", got)
	}

	// String
	got = cppTypeNameInContextForValue(apispec.TypeRef{Kind: apispec.TypeString}, "ns::X")
	if got != "std::string" {
		t.Errorf("got %q", got)
	}

	// Enum
	got = cppTypeNameInContextForValue(apispec.TypeRef{Kind: apispec.TypeEnum, Name: "E"}, "ns::X")
	if got != "E" {
		t.Errorf("got %q", got)
	}

	// Empty → unknown
	got = cppTypeNameInContextForValue(apispec.TypeRef{Kind: apispec.TypeValue}, "ns::X")
	if got != "/* unknown type */" {
		t.Errorf("got %q", got)
	}
}

func TestQualifySingleArgInContext(t *testing.T) {
	prev := classQualNames
	classQualNames = map[string]string{"Foo": "ns::Foo"}
	defer func() { classQualNames = prev }()

	// Empty context delegates
	got := qualifySingleArgInContext("Foo", "")
	if got != "ns::Foo" {
		t.Errorf("got %q", got)
	}

	// With context
	got = qualifySingleArgInContext("const Foo*", "ns::Bar")
	if got != "const ns::Foo*" {
		t.Errorf("got %q", got)
	}

	// Nested template
	got = qualifySingleArgInContext("std::unique_ptr<Foo>", "ns::Bar")
	if !contains2(got, "Foo") {
		t.Errorf("got %q", got)
	}
}

// TestStripCppTypeQualifiers_LeadingDoubleColon pins the strip
// rule for the C++ global-namespace anchor. clang occasionally
// records qual_types in the absolute form  ::ns::Foo  -- which
// names the exact same C++ type as  ns::Foo  but does not match
// classSourceFiles / classQualNames lookups that key on the
// no-anchor spelling. Without stripping the anchor, every method
// that used clang's absolute-form spelling silently disappeared
// at isUsableType filter time. The most visible casualty was
// SQLTableValuedFunction::Create, which takes
//   const ::googlesql::ResolvedCreateTableFunctionStmt *
// as its first parameter; with the anchor unstripped the static
// factory was filtered out and SQLTableValuedFunction was
// reachable but uninstantiable. The strip also unblocked dozens
// of other classes / methods across the binding (the regen
// counted +58 classes / +9 enums after this fix landed).
func TestStripCppTypeQualifiers_LeadingDoubleColon(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"const ::googlesql::ResolvedCreateTableFunctionStmt *", "googlesql::ResolvedCreateTableFunctionStmt"},
		{"::ns::Foo", "ns::Foo"},
		{"const ::ns::Foo &", "ns::Foo"},
		// Mid-name :: is a regular nested-name-specifier and must
		// stay -- only the leading anchor strips.
		{"ns::Foo", "ns::Foo"},
		{"const class ::ns::Foo *", "ns::Foo"},
	}
	for _, c := range cases {
		if got := stripCppTypeQualifiers(c.in); got != c.want {
			t.Errorf("stripCppTypeQualifiers(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

