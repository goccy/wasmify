package clangast

import (
	"encoding/json"
	"testing"

	"github.com/goccy/wasmify/internal/apispec"
)

func TestCollectCompileFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "joined -I flag",
			args: []string{"-I/usr/include", "foo.c"},
			want: []string{"-I/usr/include"},
		},
		{
			name: "separate -I flag",
			args: []string{"-I", "/usr/include", "foo.c"},
			want: []string{"-I", "/usr/include"},
		},
		{
			name: "joined -D flag",
			args: []string{"-DFOO=1", "foo.c"},
			want: []string{"-DFOO=1"},
		},
		{
			name: "separate -D flag",
			args: []string{"-D", "FOO=1", "foo.c"},
			want: []string{"-D", "FOO=1"},
		},
		{
			name: "-std= flag",
			args: []string{"-std=c++17", "foo.c"},
			want: []string{"-std=c++17"},
		},
		{
			name: "joined -isystem flag",
			args: []string{"-isystem/usr/include", "foo.c"},
			want: []string{"-isystem/usr/include"},
		},
		{
			name: "separate -isystem flag",
			args: []string{"-isystem", "/usr/include", "foo.c"},
			want: []string{"-isystem", "/usr/include"},
		},
		{
			name: "joined -iquote flag",
			args: []string{"-iquote/usr/include", "foo.c"},
			want: []string{"-iquote/usr/include"},
		},
		{
			name: "separate -iquote flag",
			args: []string{"-iquote", "/usr/include", "foo.c"},
			want: []string{"-iquote", "/usr/include"},
		},
		{
			name: "mixed flags",
			args: []string{"-O2", "-I/usr/include", "-DFOO", "-std=c++17", "-Wall", "-isystem", "/opt/include", "-iquote", ".", "foo.c"},
			want: []string{"-I/usr/include", "-DFOO", "-std=c++17", "-isystem", "/opt/include", "-iquote", "."},
		},
		{
			name: "no matching flags",
			args: []string{"-O2", "-Wall", "-Werror", "foo.c"},
			want: nil,
		},
		{
			name: "empty args",
			args: []string{},
			want: nil,
		},
		{
			name: "separate -I at end of args (no value)",
			args: []string{"-I"},
			want: []string{"-I"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CollectCompileFlags(tt.args)
			if len(got) != len(tt.want) {
				t.Fatalf("CollectCompileFlags(%v) = %v, want %v", tt.args, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("CollectCompileFlags(%v)[%d] = %q, want %q", tt.args, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestClassifyType(t *testing.T) {
	p := &Parser{
		spec:       &apispec.APISpec{},
		headerFile: "test.h",
		classes:    make(map[string]*apispec.Class),
	}

	tests := []struct {
		name      string
		qualType  string
		wantKind  apispec.TypeKind
		wantConst bool
		wantPtr   bool
		wantRef   bool
	}{
		// Primitives
		{name: "int", qualType: "int", wantKind: apispec.TypePrimitive},
		{name: "double", qualType: "double", wantKind: apispec.TypePrimitive},
		{name: "uint32_t", qualType: "uint32_t", wantKind: apispec.TypePrimitive},
		{name: "bool", qualType: "bool", wantKind: apispec.TypePrimitive},
		{name: "float", qualType: "float", wantKind: apispec.TypePrimitive},
		{name: "size_t", qualType: "size_t", wantKind: apispec.TypePrimitive},

		// Strings
		{name: "std::string", qualType: "std::string", wantKind: apispec.TypeString},
		{name: "const char *", qualType: "const char *", wantKind: apispec.TypeString, wantConst: true},
		{name: "std::string_view", qualType: "std::string_view", wantKind: apispec.TypeString},

		// Void
		{name: "void", qualType: "void", wantKind: apispec.TypeVoid},

		// Pointer to class -> handle
		{name: "MyClass *", qualType: "MyClass *", wantKind: apispec.TypeHandle, wantPtr: true},

		// Reference to class -> handle
		{name: "const MyClass &", qualType: "const MyClass &", wantKind: apispec.TypeHandle, wantConst: true, wantRef: true},

		// Vector
		{name: "std::vector<int>", qualType: "std::vector<int>", wantKind: apispec.TypeVector},

		// Plain class name -> value
		{name: "MyClass", qualType: "MyClass", wantKind: apispec.TypeValue},

		// const_iterator is NOT a vector — it's a value type (iterator)
		{name: "vector const_iterator", qualType: "std::vector<int>::const_iterator", wantKind: apispec.TypeValue},
		{name: "vector iterator", qualType: "std::vector<std::string>::iterator", wantKind: apispec.TypeValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.classifyType(tt.qualType)
			if got.Kind != tt.wantKind {
				t.Errorf("classifyType(%q).Kind = %q, want %q", tt.qualType, got.Kind, tt.wantKind)
			}
			if got.IsConst != tt.wantConst {
				t.Errorf("classifyType(%q).IsConst = %v, want %v", tt.qualType, got.IsConst, tt.wantConst)
			}
			if got.IsPointer != tt.wantPtr {
				t.Errorf("classifyType(%q).IsPointer = %v, want %v", tt.qualType, got.IsPointer, tt.wantPtr)
			}
			if got.IsRef != tt.wantRef {
				t.Errorf("classifyType(%q).IsRef = %v, want %v", tt.qualType, got.IsRef, tt.wantRef)
			}
		})
	}
}

func TestClassifyTypeVectorInner(t *testing.T) {
	p := &Parser{
		spec:       &apispec.APISpec{},
		headerFile: "test.h",
		classes:    make(map[string]*apispec.Class),
	}

	ref := p.classifyType("std::vector<int>")
	if ref.Kind != apispec.TypeVector {
		t.Fatalf("expected TypeVector, got %q", ref.Kind)
	}
	if ref.Inner == nil {
		t.Fatal("expected Inner to be non-nil for vector type")
	}
	if ref.Inner.Kind != apispec.TypePrimitive {
		t.Errorf("expected inner kind TypePrimitive, got %q", ref.Inner.Kind)
	}
	if ref.Inner.Name != "int" {
		t.Errorf("expected inner name \"int\", got %q", ref.Inner.Name)
	}
}

func TestParseReturnType(t *testing.T) {
	p := &Parser{
		spec:       &apispec.APISpec{},
		headerFile: "test.h",
		classes:    make(map[string]*apispec.Class),
	}

	tests := []struct {
		name     string
		funcType string
		wantKind apispec.TypeKind
		wantName string
	}{
		{
			name:     "int return",
			funcType: "int (const char *, int)",
			wantKind: apispec.TypePrimitive,
			wantName: "int",
		},
		{
			name:     "void return",
			funcType: "void (int)",
			wantKind: apispec.TypeVoid,
			wantName: "void",
		},
		{
			name:     "std::string return",
			funcType: "std::string (int, double)",
			wantKind: apispec.TypeString,
			wantName: "std::string",
		},
		{
			name:     "no parens (bare type)",
			funcType: "double",
			wantKind: apispec.TypePrimitive,
			wantName: "double",
		},
		{
			name:     "bool return",
			funcType: "bool (void)",
			wantKind: apispec.TypePrimitive,
			wantName: "bool",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.parseReturnType(tt.funcType)
			if got.Kind != tt.wantKind {
				t.Errorf("parseReturnType(%q).Kind = %q, want %q", tt.funcType, got.Kind, tt.wantKind)
			}
			if got.Name != tt.wantName {
				t.Errorf("parseReturnType(%q).Name = %q, want %q", tt.funcType, got.Name, tt.wantName)
			}
		})
	}
}

func TestNodeFile(t *testing.T) {
	p := &Parser{
		spec:       &apispec.APISpec{},
		headerFile: "mylib.h",
		classes:    make(map[string]*apispec.Class),
	}

	tests := []struct {
		name string
		node Node
		want string
	}{
		{
			name: "file from Loc",
			node: Node{Loc: &Loc{File: "/path/to/mylib.h"}},
			want: "/path/to/mylib.h",
		},
		{
			name: "file from Range.Begin",
			node: Node{
				Loc:   &Loc{},
				Range: &Range{Begin: Loc{File: "/path/to/mylib.h"}},
			},
			want: "/path/to/mylib.h",
		},
		{
			name: "nil Loc returns empty",
			node: Node{},
			want: "",
		},
		{
			name: "empty Loc returns empty",
			node: Node{Loc: &Loc{}},
			want: "",
		},
		{
			name: "IncludedFrom is not used (avoids stdlib leak)",
			node: Node{Loc: &Loc{IncludedFrom: &struct {
				File string `json:"file,omitempty"`
			}{File: "/path/to/mylib.h"}}},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.nodeFile(&tt.node)
			if got != tt.want {
				t.Errorf("nodeFile() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMatchesHeaderFile(t *testing.T) {
	tests := []struct {
		file       string
		headerFile string
		want       bool
	}{
		{"/path/to/mylib.h", "mylib.h", true},
		{"/path/to/mylib.h", "/path/to/mylib.h", true},
		{"mylib.h", "/path/to/mylib.h", true},
		{"/usr/include/stdio.h", "mylib.h", false},
		{"/path/to/other.h", "mylib.h", false},
	}

	for _, tt := range tests {
		got := matchesHeaderFile(tt.file, tt.headerFile)
		if got != tt.want {
			t.Errorf("matchesHeaderFile(%q, %q) = %v, want %v", tt.file, tt.headerFile, got, tt.want)
		}
	}
}

func TestFileContextPropagation(t *testing.T) {
	// Test that child nodes without file info inherit context from parent.
	// NamespaceDecl with file=target → children without file should be included.
	// NamespaceDecl with file=other → children without file should be excluded.
	headerFile := "mylib.h"

	root := &Node{
		Kind: "TranslationUnitDecl",
		Inner: []Node{
			// Namespace from target file — children should be included
			{
				Kind: "NamespaceDecl",
				Name: "myns",
				Loc:  &Loc{File: "/path/to/mylib.h"},
				Inner: []Node{
					{
						Kind: "FunctionDecl",
						Name: "target_func",
						Loc:  &Loc{}, // empty file, inherits from parent
						Type: &Type{QualType: "int ()"},
					},
				},
			},
			// Namespace from other file — children should be excluded
			{
				Kind: "NamespaceDecl",
				Name: "stdlib",
				Loc:  &Loc{File: "/usr/include/c++/v1/string"},
				Inner: []Node{
					{
						Kind: "FunctionDecl",
						Name: "stdlib_func",
						Loc:  &Loc{}, // empty file, inherits from parent
						Type: &Type{QualType: "int ()"},
					},
				},
			},
		},
	}

	spec := Parse(root, headerFile)

	if len(spec.Functions) != 1 {
		t.Fatalf("expected 1 function, got %d", len(spec.Functions))
	}
	if spec.Functions[0].Name != "target_func" {
		t.Errorf("expected target_func, got %s", spec.Functions[0].Name)
	}
}

func TestParseFullAST(t *testing.T) {
	// Build a synthetic AST tree:
	//   TranslationUnitDecl
	//     NamespaceDecl "testns"
	//       FunctionDecl "add" : int (int, int)
	//       CXXRecordDecl "Widget" with base "Base"
	//         CXXMethodDecl "show" : void (const std::string &) [public]
	//         FieldDecl "width" : int [public]
	//         FieldDecl "internal_" : int [private]
	//       EnumDecl "Color"
	//         EnumConstantDecl "Red" = 0
	//         EnumConstantDecl "Green" = 1
	//         EnumConstantDecl "Blue" = 2

	headerFile := "test.h"
	loc := &Loc{File: headerFile}

	root := &Node{
		Kind: "TranslationUnitDecl",
		Inner: []Node{
			{
				Kind: "NamespaceDecl",
				Name: "testns",
				Inner: []Node{
					// Free function: int add(int a, int b)
					{
						Kind: "FunctionDecl",
						Name: "add",
						Loc:  loc,
						Type: &Type{QualType: "int (int, int)"},
						Inner: []Node{
							{
								Kind: "ParmVarDecl",
								Name: "a",
								Type: &Type{QualType: "int"},
							},
							{
								Kind: "ParmVarDecl",
								Name: "b",
								Type: &Type{QualType: "int"},
							},
						},
					},
					// Class Widget with base Base
					{
						Kind: "CXXRecordDecl",
						Name: "Widget",
						Loc:  loc,
						Bases: []Base{
							{
								Access: "public",
								Type:   Type{QualType: "class Base"},
							},
						},
						Inner: []Node{
							// Public method: void show(const std::string& msg)
							{
								Kind:   "CXXMethodDecl",
								Name:   "show",
								Access: "public",
								Type:   &Type{QualType: "void (const std::string &)"},
								Inner: []Node{
									{
										Kind: "ParmVarDecl",
										Name: "msg",
										Type: &Type{QualType: "const std::string &"},
									},
								},
							},
							// Public field: int width
							{
								Kind:   "FieldDecl",
								Name:   "width",
								Access: "public",
								Type:   &Type{QualType: "int"},
							},
							// Private field: int internal_ (should be filtered)
							{
								Kind:   "FieldDecl",
								Name:   "internal_",
								Access: "private",
								Type:   &Type{QualType: "int"},
							},
						},
					},
					// Enum Color with Red=0, Green=1, Blue=2
					{
						Kind: "EnumDecl",
						Name: "Color",
						Loc:  loc,
						Inner: []Node{
							{
								Kind: "EnumConstantDecl",
								Name: "Red",
								Inner: []Node{
									{Kind: "ConstantExpr", Value: json.RawMessage(`"0"`)},
								},
							},
							{
								Kind: "EnumConstantDecl",
								Name: "Green",
								Inner: []Node{
									{Kind: "ConstantExpr", Value: json.RawMessage(`"1"`)},
								},
							},
							{
								Kind: "EnumConstantDecl",
								Name: "Blue",
								Inner: []Node{
									{Kind: "ConstantExpr", Value: json.RawMessage(`"2"`)},
								},
							},
						},
					},
				},
			},
		},
	}

	spec := Parse(root, headerFile)

	// Verify functions
	if len(spec.Functions) != 1 {
		t.Fatalf("expected 1 function, got %d", len(spec.Functions))
	}
	fn := spec.Functions[0]
	if fn.Name != "add" {
		t.Errorf("function name = %q, want %q", fn.Name, "add")
	}
	if fn.QualName != "testns::add" {
		t.Errorf("function qual_name = %q, want %q", fn.QualName, "testns::add")
	}
	if fn.Namespace != "testns" {
		t.Errorf("function namespace = %q, want %q", fn.Namespace, "testns")
	}
	if fn.ReturnType.Kind != apispec.TypePrimitive {
		t.Errorf("function return type kind = %q, want %q", fn.ReturnType.Kind, apispec.TypePrimitive)
	}
	if len(fn.Params) != 2 {
		t.Fatalf("expected 2 params, got %d", len(fn.Params))
	}
	if fn.Params[0].Name != "a" || fn.Params[0].Type.Kind != apispec.TypePrimitive {
		t.Errorf("param[0] = %+v, want name=a kind=primitive", fn.Params[0])
	}
	if fn.Params[1].Name != "b" || fn.Params[1].Type.Kind != apispec.TypePrimitive {
		t.Errorf("param[1] = %+v, want name=b kind=primitive", fn.Params[1])
	}

	// Verify classes
	if len(spec.Classes) != 1 {
		t.Fatalf("expected 1 class, got %d", len(spec.Classes))
	}
	cls := spec.Classes[0]
	if cls.Name != "Widget" {
		t.Errorf("class name = %q, want %q", cls.Name, "Widget")
	}
	if cls.QualName != "testns::Widget" {
		t.Errorf("class qual_name = %q, want %q", cls.QualName, "testns::Widget")
	}
	if cls.Namespace != "testns" {
		t.Errorf("class namespace = %q, want %q", cls.Namespace, "testns")
	}
	if cls.Parent != "Base" {
		t.Errorf("class parent = %q, want %q", cls.Parent, "Base")
	}
	if len(cls.Parents) != 1 || cls.Parents[0] != "Base" {
		t.Errorf("class parents = %v, want [Base]", cls.Parents)
	}
	if !cls.IsHandle {
		t.Error("expected class to be handle type (has base class)")
	}

	// Verify methods (only public, no constructors/destructors)
	if len(cls.Methods) != 1 {
		t.Fatalf("expected 1 method, got %d", len(cls.Methods))
	}
	method := cls.Methods[0]
	if method.Name != "show" {
		t.Errorf("method name = %q, want %q", method.Name, "show")
	}
	if method.QualName != "testns::Widget::show" {
		t.Errorf("method qual_name = %q, want %q", method.QualName, "testns::Widget::show")
	}
	if method.ReturnType.Kind != apispec.TypeVoid {
		t.Errorf("method return type kind = %q, want %q", method.ReturnType.Kind, apispec.TypeVoid)
	}
	if len(method.Params) != 1 {
		t.Fatalf("expected 1 method param, got %d", len(method.Params))
	}
	if method.Params[0].Name != "msg" {
		t.Errorf("method param name = %q, want %q", method.Params[0].Name, "msg")
	}
	if method.Params[0].Type.Kind != apispec.TypeString {
		t.Errorf("method param type kind = %q, want %q", method.Params[0].Type.Kind, apispec.TypeString)
	}

	// Verify fields (only public, private internal_ should be filtered)
	if len(cls.Fields) != 1 {
		t.Fatalf("expected 1 public field, got %d", len(cls.Fields))
	}
	if cls.Fields[0].Name != "width" {
		t.Errorf("field name = %q, want %q", cls.Fields[0].Name, "width")
	}
	if cls.Fields[0].Type.Kind != apispec.TypePrimitive {
		t.Errorf("field type kind = %q, want %q", cls.Fields[0].Type.Kind, apispec.TypePrimitive)
	}

	// Verify enums
	if len(spec.Enums) != 1 {
		t.Fatalf("expected 1 enum, got %d", len(spec.Enums))
	}
	en := spec.Enums[0]
	if en.Name != "Color" {
		t.Errorf("enum name = %q, want %q", en.Name, "Color")
	}
	if en.QualName != "testns::Color" {
		t.Errorf("enum qual_name = %q, want %q", en.QualName, "testns::Color")
	}
	if len(en.Values) != 3 {
		t.Fatalf("expected 3 enum values, got %d", len(en.Values))
	}
	expectedValues := []struct {
		name  string
		value int64
	}{
		{"Red", 0},
		{"Green", 1},
		{"Blue", 2},
	}
	for i, ev := range expectedValues {
		if en.Values[i].Name != ev.name {
			t.Errorf("enum value[%d].Name = %q, want %q", i, en.Values[i].Name, ev.name)
		}
		if en.Values[i].Value != ev.value {
			t.Errorf("enum value[%d].Value = %d, want %d", i, en.Values[i].Value, ev.value)
		}
	}
}

func TestParseImplicitAndStaticFiltered(t *testing.T) {
	headerFile := "test.h"
	loc := &Loc{File: headerFile}

	root := &Node{
		Kind: "TranslationUnitDecl",
		Inner: []Node{
			// Implicit function should be filtered
			{
				Kind:       "FunctionDecl",
				Name:       "implicit_fn",
				Loc:        loc,
				IsImplicit: true,
				Type:       &Type{QualType: "void ()"},
			},
			// Static function should be filtered
			{
				Kind:         "FunctionDecl",
				Name:         "static_fn",
				Loc:          loc,
				StorageClass: "static",
				Type:         &Type{QualType: "void ()"},
			},
			// Normal function should be included
			{
				Kind: "FunctionDecl",
				Name: "normal_fn",
				Loc:  loc,
				Type: &Type{QualType: "void ()"},
			},
		},
	}

	spec := Parse(root, headerFile)
	if len(spec.Functions) != 1 {
		t.Fatalf("expected 1 function (implicit and static filtered), got %d", len(spec.Functions))
	}
	if spec.Functions[0].Name != "normal_fn" {
		t.Errorf("expected normal_fn, got %q", spec.Functions[0].Name)
	}
}

func TestParseLinkageSpecDecl(t *testing.T) {
	headerFile := "test.h"
	loc := &Loc{File: headerFile}

	root := &Node{
		Kind: "TranslationUnitDecl",
		Inner: []Node{
			{
				Kind: "LinkageSpecDecl",
				Inner: []Node{
					{
						Kind: "FunctionDecl",
						Name: "c_func",
						Loc:  loc,
						Type: &Type{QualType: "int (int)"},
						Inner: []Node{
							{Kind: "ParmVarDecl", Name: "x", Type: &Type{QualType: "int"}},
						},
					},
				},
			},
		},
	}

	spec := Parse(root, headerFile)
	if len(spec.Functions) != 1 {
		t.Fatalf("expected 1 function from extern C block, got %d", len(spec.Functions))
	}
	if spec.Functions[0].Name != "c_func" {
		t.Errorf("expected c_func, got %q", spec.Functions[0].Name)
	}
}

func TestParseClassVirtualMethod(t *testing.T) {
	headerFile := "test.h"
	loc := &Loc{File: headerFile}

	root := &Node{
		Kind: "TranslationUnitDecl",
		Inner: []Node{
			{
				Kind: "CXXRecordDecl",
				Name: "Interface",
				Loc:  loc,
				Inner: []Node{
					{
						Kind:      "CXXMethodDecl",
						Name:      "doSomething",
						Access:    "public",
						IsVirtual: true,
						IsPure:    true,
						Type:      &Type{QualType: "void ()"},
					},
				},
			},
		},
	}

	spec := Parse(root, headerFile)
	if len(spec.Classes) != 1 {
		t.Fatalf("expected 1 class, got %d", len(spec.Classes))
	}
	cls := spec.Classes[0]
	if !cls.IsHandle {
		t.Error("expected class with virtual method to be handle type")
	}
	if len(cls.Methods) != 1 {
		t.Fatalf("expected 1 method, got %d", len(cls.Methods))
	}
	if !cls.Methods[0].IsVirtual {
		t.Error("expected method to be virtual")
	}
}

// TestParseClassNestedOutOfLine verifies that out-of-line definitions of
// nested classes are correctly attributed to their parent class via the
// parentDeclContextId field, NOT to the enclosing namespace.
//
// clang represents:
//
//	namespace ns {
//	class Outer { class Nested; };       // forward decl inside Outer
//	class Outer::Nested final { int x; };  // out-of-line definition
//	}
//
// as a top-level CXXRecordDecl "Nested" (under namespace ns) with
// parentDeclContextId pointing to Outer. We must resolve this to
// "ns::Outer::Nested", not "ns::Nested".
func TestParseClassNestedOutOfLine(t *testing.T) {
	headerFile := "test.h"
	loc := &Loc{File: headerFile}

	root := &Node{
		Kind: "TranslationUnitDecl",
		Inner: []Node{
			{
				Kind: "NamespaceDecl",
				Name: "ns",
				Loc:  loc,
				Inner: []Node{
					{
						// Outer class definition
						ID:             "OUTER_ID",
						Kind:           "CXXRecordDecl",
						Name:           "Outer",
						Loc:            loc,
						DefinitionData: &DefData{},
						Inner: []Node{
							// Forward declaration of Nested inside Outer
							{
								ID:   "NESTED_FWD_ID",
								Kind: "CXXRecordDecl",
								Name: "Nested",
								Loc:  loc,
							},
						},
					},
					// Out-of-line definition: top-level under namespace,
					// but parentDeclContextId points to Outer
					{
						ID:                  "NESTED_DEF_ID",
						Kind:                "CXXRecordDecl",
						Name:                "Nested",
						Loc:                 loc,
						ParentDeclContextID: "OUTER_ID",
						PreviousDecl:        "NESTED_FWD_ID",
						DefinitionData:      &DefData{},
						Inner: []Node{
							{
								Kind:   "FieldDecl",
								Name:   "field",
								Access: "public",
								Type:   &Type{QualType: "int"},
							},
						},
					},
				},
			},
		},
	}

	spec := Parse(root, headerFile)

	var outer, nestedCorrect, nestedWrong *apispec.Class
	for i := range spec.Classes {
		c := &spec.Classes[i]
		switch c.QualName {
		case "ns::Outer":
			outer = c
		case "ns::Outer::Nested":
			nestedCorrect = c
		case "ns::Nested":
			nestedWrong = c
		}
	}

	if outer == nil {
		t.Fatal("expected ns::Outer class to be parsed")
	}
	if nestedCorrect == nil {
		t.Fatal("expected ns::Outer::Nested (out-of-line definition) to be resolved with correct FQDN")
	}
	if len(nestedCorrect.Fields) != 1 {
		t.Errorf("expected ns::Outer::Nested to have 1 field (real definition), got %d", len(nestedCorrect.Fields))
	}
	if nestedWrong != nil {
		t.Errorf("ns::Nested should NOT be registered (it's actually nested in Outer): %+v", nestedWrong)
	}
}

// TestParseClassAccessSpecDecl verifies that access (public/private/protected)
// is correctly attributed to class members via AccessSpecDecl nodes.
// clang does NOT put access on individual member nodes; instead, AccessSpecDecl
// nodes appear in the inner array and subsequent members inherit that access.
func TestParseClassAccessSpecDecl(t *testing.T) {
	headerFile := "test.h"
	loc := &Loc{File: headerFile}

	// Simulate:
	//   class Foo {
	//    public:
	//     void pub_method();
	//     int pub_field;
	//    private:
	//     void priv_method();
	//     int priv_field;
	//    protected:
	//     void prot_method();
	//     int prot_field;
	//   };
	root := &Node{
		Kind: "TranslationUnitDecl",
		Inner: []Node{
			{
				Kind:           "CXXRecordDecl",
				Name:           "Foo",
				TagUsed:        "class",
				Loc:            loc,
				DefinitionData: &DefData{},
				Inner: []Node{
					{Kind: "AccessSpecDecl", Access: "public"},
					{
						Kind: "CXXMethodDecl",
						Name: "pub_method",
						Type: &Type{QualType: "void ()"},
					},
					{
						Kind: "FieldDecl",
						Name: "pub_field",
						Type: &Type{QualType: "int"},
					},
					{Kind: "AccessSpecDecl", Access: "private"},
					{
						Kind: "CXXMethodDecl",
						Name: "priv_method",
						Type: &Type{QualType: "void ()"},
					},
					{
						Kind: "FieldDecl",
						Name: "priv_field",
						Type: &Type{QualType: "int"},
					},
					{Kind: "AccessSpecDecl", Access: "protected"},
					{
						Kind: "CXXMethodDecl",
						Name: "prot_method",
						Type: &Type{QualType: "void ()"},
					},
					{
						Kind: "FieldDecl",
						Name: "prot_field",
						Type: &Type{QualType: "int"},
					},
				},
			},
		},
	}

	spec := Parse(root, headerFile)
	if len(spec.Classes) != 1 {
		t.Fatalf("expected 1 class, got %d", len(spec.Classes))
	}
	cls := spec.Classes[0]

	// Only pub_method should be in methods (private/protected skipped)
	if len(cls.Methods) != 1 {
		t.Errorf("expected 1 public method, got %d: %+v", len(cls.Methods), cls.Methods)
	}
	if len(cls.Methods) > 0 && cls.Methods[0].Name != "pub_method" {
		t.Errorf("expected pub_method, got %s", cls.Methods[0].Name)
	}

	// Only pub_field should be in fields
	if len(cls.Fields) != 1 {
		t.Errorf("expected 1 public field, got %d: %+v", len(cls.Fields), cls.Fields)
	}
	if len(cls.Fields) > 0 && cls.Fields[0].Name != "pub_field" {
		t.Errorf("expected pub_field, got %s", cls.Fields[0].Name)
	}
}

// TestParseStructDefaultAccess verifies that struct members default to public
// when there's no explicit AccessSpecDecl before them.
func TestParseStructDefaultAccess(t *testing.T) {
	headerFile := "test.h"
	loc := &Loc{File: headerFile}

	// struct Bar { int x; void y(); };
	// members before any AccessSpecDecl should be public (struct default)
	root := &Node{
		Kind: "TranslationUnitDecl",
		Inner: []Node{
			{
				Kind:           "CXXRecordDecl",
				Name:           "Bar",
				TagUsed:        "struct",
				Loc:            loc,
				DefinitionData: &DefData{},
				Inner: []Node{
					{
						Kind: "FieldDecl",
						Name: "x",
						Type: &Type{QualType: "int"},
					},
					{
						Kind: "CXXMethodDecl",
						Name: "y",
						Type: &Type{QualType: "void ()"},
					},
				},
			},
		},
	}

	spec := Parse(root, headerFile)
	if len(spec.Classes) != 1 {
		t.Fatalf("expected 1 class, got %d", len(spec.Classes))
	}
	cls := spec.Classes[0]
	if len(cls.Fields) != 1 || cls.Fields[0].Name != "x" {
		t.Errorf("expected 1 public field 'x', got %+v", cls.Fields)
	}
	if len(cls.Methods) != 1 || cls.Methods[0].Name != "y" {
		t.Errorf("expected 1 public method 'y', got %+v", cls.Methods)
	}
}

// TestParseClassDefaultAccessPrivate verifies that class members default to
// private (so they should be SKIPPED) when there's no explicit AccessSpecDecl.
func TestParseClassDefaultAccessPrivate(t *testing.T) {
	headerFile := "test.h"
	loc := &Loc{File: headerFile}

	// class Baz { int x; void y(); };
	// members before any AccessSpecDecl should be private (class default) → skipped
	root := &Node{
		Kind: "TranslationUnitDecl",
		Inner: []Node{
			{
				Kind:           "CXXRecordDecl",
				Name:           "Baz",
				TagUsed:        "class",
				Loc:            loc,
				DefinitionData: &DefData{},
				Inner: []Node{
					{
						Kind: "FieldDecl",
						Name: "x",
						Type: &Type{QualType: "int"},
					},
					{
						Kind: "CXXMethodDecl",
						Name: "y",
						Type: &Type{QualType: "void ()"},
					},
				},
			},
		},
	}

	spec := Parse(root, headerFile)
	if len(spec.Classes) != 1 {
		t.Fatalf("expected 1 class, got %d", len(spec.Classes))
	}
	cls := spec.Classes[0]
	if len(cls.Fields) != 0 {
		t.Errorf("expected 0 fields (class defaults to private), got %+v", cls.Fields)
	}
	if len(cls.Methods) != 0 {
		t.Errorf("expected 0 methods (class defaults to private), got %+v", cls.Methods)
	}
}

// TestParseClassForwardDeclarationStandalone verifies that a top-level forward
// declaration (without a real definition) is not registered as a class.
func TestParseClassForwardDeclarationStandalone(t *testing.T) {
	headerFile := "test.h"
	loc := &Loc{File: headerFile}

	root := &Node{
		Kind: "TranslationUnitDecl",
		Inner: []Node{
			{
				// Forward declaration only (e.g., `class Foo;`)
				Kind: "CXXRecordDecl",
				Name: "Foo",
				Loc:  loc,
			},
		},
	}

	spec := Parse(root, headerFile)
	if len(spec.Classes) != 0 {
		t.Errorf("expected 0 classes for forward declaration only, got %d", len(spec.Classes))
		for _, c := range spec.Classes {
			t.Logf("  registered: %s", c.QualName)
		}
	}
}

func TestParseScopedEnum(t *testing.T) {
	headerFile := "test.h"
	loc := &Loc{File: headerFile}

	root := &Node{
		Kind: "TranslationUnitDecl",
		Inner: []Node{
			{
				Kind:          "EnumDecl",
				Name:          "Status",
				Loc:           loc,
				ScopedEnumTag: "class",
				Inner: []Node{
					{
						Kind: "EnumConstantDecl",
						Name: "OK",
						Inner: []Node{
							{Kind: "ConstantExpr", Value: json.RawMessage(`"0"`)},
						},
					},
				},
			},
		},
	}

	spec := Parse(root, headerFile)
	if len(spec.Enums) != 1 {
		t.Fatalf("expected 1 enum, got %d", len(spec.Enums))
	}
	if !spec.Enums[0].IsScoped {
		t.Error("expected scoped enum (enum class)")
	}
}

func TestParseFiltersByFile(t *testing.T) {
	headerFile := "mylib.h"

	root := &Node{
		Kind: "TranslationUnitDecl",
		Inner: []Node{
			// From target file - should be included
			{
				Kind: "FunctionDecl",
				Name: "my_func",
				Loc:  &Loc{File: "/path/to/mylib.h"},
				Type: &Type{QualType: "void ()"},
			},
			// From different file - should be excluded
			{
				Kind: "FunctionDecl",
				Name: "other_func",
				Loc:  &Loc{File: "/usr/include/stdlib.h"},
				Type: &Type{QualType: "void ()"},
			},
		},
	}

	spec := Parse(root, headerFile)
	if len(spec.Functions) != 1 {
		t.Fatalf("expected 1 function (other_func filtered), got %d", len(spec.Functions))
	}
	if spec.Functions[0].Name != "my_func" {
		t.Errorf("expected my_func, got %q", spec.Functions[0].Name)
	}
}

func TestParseConstMethod(t *testing.T) {
	headerFile := "test.h"
	loc := &Loc{File: headerFile}

	root := &Node{
		Kind: "TranslationUnitDecl",
		Inner: []Node{
			{
				Kind: "CXXRecordDecl",
				Name: "Foo",
				Loc:  loc,
				Inner: []Node{
					{
						Kind:   "CXXMethodDecl",
						Name:   "getValue",
						Access: "public",
						Type:   &Type{QualType: "int () const"},
					},
				},
			},
		},
	}

	spec := Parse(root, headerFile)
	if len(spec.Classes) != 1 {
		t.Fatalf("expected 1 class, got %d", len(spec.Classes))
	}
	if len(spec.Classes[0].Methods) != 1 {
		t.Fatalf("expected 1 method, got %d", len(spec.Classes[0].Methods))
	}
	if !spec.Classes[0].Methods[0].IsConst {
		t.Error("expected const method")
	}
}

// TestClassScopedTypeAlias verifies that `using Value = intptr_t;` inside
// a class body is resolved so that methods returning `Value` produce a
// primitive TypeRef, not a handle/value TypeRef with name "Value".
func TestClassScopedTypeAlias(t *testing.T) {
	headerFile := "test_alias.h"
	loc := &Loc{File: headerFile, Line: 1}
	root := &Node{
		Kind: "TranslationUnitDecl",
		Inner: []Node{
			{
				Kind:      "CXXRecordDecl",
				Name:      "Counter",
				Loc:       loc,
				TagUsed:   "class",
				IsImplicit: false,
				Inner: []Node{
					{
						Kind: "TypeAliasDecl",
						Name: "Value",
						Type: &Type{QualType: "intptr_t"},
					},
					{
						Kind:   "CXXMethodDecl",
						Name:   "GetNext",
						Access: "public",
						Type:   &Type{QualType: "Value ()"},
					},
				},
			},
		},
	}

	spec := Parse(root, headerFile)
	if len(spec.Classes) != 1 {
		t.Fatalf("expected 1 class, got %d", len(spec.Classes))
	}
	cls := spec.Classes[0]
	if len(cls.Methods) != 1 {
		t.Fatalf("expected 1 method, got %d", len(cls.Methods))
	}
	ret := cls.Methods[0].ReturnType
	if ret.Kind != apispec.TypePrimitive {
		t.Errorf("GetNext() return kind = %q, want TypePrimitive (class-scoped alias to intptr_t)", ret.Kind)
	}
	if ret.Name != "intptr_t" {
		t.Errorf("GetNext() return name = %q, want \"intptr_t\"", ret.Name)
	}
}

// TestReferenceFieldPromotesHandle verifies that a class with a reference
// field is promoted to a handle type even if it otherwise looks POD-like.
// Brace-init `T var{};` is impossible when T has a reference member.
func TestReferenceFieldPromotesHandle(t *testing.T) {
	headerFile := "test_ref.h"
	loc := &Loc{File: headerFile, Line: 1}
	root := &Node{
		Kind: "TranslationUnitDecl",
		Inner: []Node{
			{
				Kind:    "CXXRecordDecl",
				Name:    "Context",
				Loc:     loc,
				TagUsed: "struct",
				Inner: []Node{
					{
						Kind:   "FieldDecl",
						Name:   "location",
						Access: "public",
						Type:   &Type{QualType: "const Point &"},
					},
					{
						Kind:   "CXXMethodDecl",
						Name:   "doSomething",
						Access: "public",
						Type:   &Type{QualType: "void ()"},
					},
				},
			},
		},
	}

	spec := Parse(root, headerFile)
	if len(spec.Classes) != 1 {
		t.Fatalf("expected 1 class, got %d", len(spec.Classes))
	}
	if !spec.Classes[0].IsHandle {
		t.Error("class with reference field should be promoted to handle")
	}
}

// TestUniquePtrFieldPromotesHandle verifies that a class with a
// unique_ptr field is promoted to handle (copy ctor is implicitly deleted).
func TestUniquePtrFieldPromotesHandle(t *testing.T) {
	headerFile := "test_uptr.h"
	loc := &Loc{File: headerFile, Line: 1}
	root := &Node{
		Kind: "TranslationUnitDecl",
		Inner: []Node{
			{
				Kind:    "CXXRecordDecl",
				Name:    "Info",
				Loc:     loc,
				TagUsed: "struct",
				Inner: []Node{
					{
						Kind:   "FieldDecl",
						Name:   "data",
						Access: "public",
						Type:   &Type{QualType: "std::unique_ptr<Expr>"},
					},
					{
						Kind:   "FieldDecl",
						Name:   "id",
						Access: "public",
						Type:   &Type{QualType: "int"},
					},
				},
			},
		},
	}

	spec := Parse(root, headerFile)
	if len(spec.Classes) != 1 {
		t.Fatalf("expected 1 class, got %d", len(spec.Classes))
	}
	if !spec.Classes[0].IsHandle {
		t.Error("class with unique_ptr field should be promoted to handle")
	}
}
