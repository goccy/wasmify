package clangast

import (
	"testing"

	"github.com/goccy/wasmify/internal/apispec"
)

// TestPostProcessQualifyShortNames_NamespaceLookup verifies that an
// unqualified type spelling at the declaration site of a method
// living in `google::protobuf` is rewritten to its FQDN by walking
// the namespace stack inside-out.
//
// This mirrors the real-world failure that motivated the pass:
// `google::protobuf::Descriptor::GetSourceLocation(SourceLocation*)`
// where clang records the parameter as bare `SourceLocation *`
// despite the surrounding namespace; without rewriting, the bridge
// disambiguates against an arbitrary `SourceLocation` in
// `classQualNames` and emits a reinterpret_cast to the wrong
// namespace's class.
func TestPostProcessQualifyShortNames_NamespaceLookup(t *testing.T) {
	spec := &apispec.APISpec{
		Classes: []apispec.Class{
			{
				Name:      "SourceLocation",
				Namespace: "google::protobuf",
				QualName:  "google::protobuf::SourceLocation",
			},
			{
				Name:      "SourceLocation",
				Namespace: "googlesql_base",
				QualName:  "googlesql_base::SourceLocation",
			},
			{
				Name:      "Descriptor",
				Namespace: "google::protobuf",
				QualName:  "google::protobuf::Descriptor",
				Methods: []apispec.Function{
					{
						Name:       "GetSourceLocation",
						QualName:   "google::protobuf::Descriptor::GetSourceLocation",
						ReturnType: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "bool", QualType: "bool"},
						Params: []apispec.Param{
							{
								Name: "out_location",
								Type: apispec.TypeRef{
									Kind:      apispec.TypeHandle,
									Name:      "SourceLocation",
									QualType:  "SourceLocation *",
									IsPointer: true,
								},
							},
						},
					},
				},
			},
		},
	}

	PostProcessQualifyShortNames(spec)

	got := spec.Classes[2].Methods[0].Params[0].Type
	if got.Name != "google::protobuf::SourceLocation" {
		t.Errorf("Name: got %q, want %q", got.Name, "google::protobuf::SourceLocation")
	}
	if got.QualType != "google::protobuf::SourceLocation *" {
		t.Errorf("QualType: got %q, want %q", got.QualType, "google::protobuf::SourceLocation *")
	}
}

// TestPostProcessQualifyShortNames_BaseClassNestedType verifies
// that a derived class's method referencing an unqualified name
// resolves through the inheritance chain to a nested type
// declared in a base class -- mirroring C++ class-scope lookup.
//
// Concrete failure this guards: `googlesql_base::UnsafeArena::status()`
// returns `Status` (the source spelling). The actual type is the
// nested `BaseArena::Status` struct in the base class. Without
// inheritance walking, the namespace fallback would mis-qualify
// it as `googlesql_base::Status`, which matches an unrelated
// ErrorTypes config entry and the bridge then emits `.ok()` /
// `.message()` calls against an object that does not have them.
func TestPostProcessQualifyShortNames_BaseClassNestedType(t *testing.T) {
	spec := &apispec.APISpec{
		Classes: []apispec.Class{
			{
				Name:      "BaseArena",
				Namespace: "googlesql_base",
				QualName:  "googlesql_base::BaseArena",
			},
			{
				Name:      "Status",
				Namespace: "googlesql_base::BaseArena",
				QualName:  "googlesql_base::BaseArena::Status",
			},
			{
				Name:      "UnsafeArena",
				Namespace: "googlesql_base",
				QualName:  "googlesql_base::UnsafeArena",
				Parents:   []string{"googlesql_base::BaseArena"},
				Methods: []apispec.Function{
					{
						Name:     "status",
						QualName: "googlesql_base::UnsafeArena::status",
						ReturnType: apispec.TypeRef{
							Kind:     apispec.TypeValue,
							Name:     "Status",
							QualType: "Status",
						},
					},
				},
			},
		},
	}

	PostProcessQualifyShortNames(spec)

	got := spec.Classes[2].Methods[0].ReturnType
	if got.Name != "googlesql_base::BaseArena::Status" {
		t.Errorf("Name: got %q, want %q", got.Name, "googlesql_base::BaseArena::Status")
	}
	if got.QualType != "googlesql_base::BaseArena::Status" {
		t.Errorf("QualType: got %q, want %q", got.QualType, "googlesql_base::BaseArena::Status")
	}
}

// TestPostProcessQualifyShortNames_ExternalTypeFallback verifies
// the fallback path: a type spelling that resolves to nothing in
// the parsed class universe still gets the enclosing namespace
// prepended, mirroring the C++ rule that an unqualified name in
// a `namespace X { ... }` block always begins lookup in `X`.
//
// Concrete case: `google::protobuf::SourceLocation` is referenced
// from project methods but never parsed itself (header lives
// outside the scan set). Without the fallback the bare spelling
// reaches the bridge, which then has no way to disambiguate it
// from another namespace's `SourceLocation`.
func TestPostProcessQualifyShortNames_ExternalTypeFallback(t *testing.T) {
	spec := &apispec.APISpec{
		Classes: []apispec.Class{
			{
				Name:      "Descriptor",
				Namespace: "google::protobuf",
				QualName:  "google::protobuf::Descriptor",
				Methods: []apispec.Function{
					{
						Name:     "Frobnicate",
						QualName: "google::protobuf::Descriptor::Frobnicate",
						ReturnType: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "void", QualType: "void"},
						Params: []apispec.Param{
							{
								Name: "loc",
								Type: apispec.TypeRef{
									Kind:      apispec.TypeHandle,
									Name:      "SourceLocation",
									QualType:  "SourceLocation *",
									IsPointer: true,
								},
							},
						},
					},
				},
			},
		},
	}

	PostProcessQualifyShortNames(spec)

	got := spec.Classes[0].Methods[0].Params[0].Type
	if got.Name != "google::protobuf::SourceLocation" {
		t.Errorf("Name: got %q, want %q", got.Name, "google::protobuf::SourceLocation")
	}
	if got.QualType != "google::protobuf::SourceLocation *" {
		t.Errorf("QualType: got %q, want %q", got.QualType, "google::protobuf::SourceLocation *")
	}
}

// TestPostProcessQualifyShortNames_AlreadyQualifiedUnchanged
// guards against re-qualifying a name that already carries a
// `::` separator. Re-running the pass must be idempotent and
// must not mangle types like `std::string` or
// `googlesql_base::Status`.
func TestPostProcessQualifyShortNames_AlreadyQualifiedUnchanged(t *testing.T) {
	spec := &apispec.APISpec{
		Classes: []apispec.Class{
			{
				Name:      "X",
				Namespace: "ns",
				QualName:  "ns::X",
				Methods: []apispec.Function{
					{
						Name:     "f",
						QualName: "ns::X::f",
						ReturnType: apispec.TypeRef{
							Kind:     apispec.TypeValue,
							Name:     "googlesql_base::Status",
							QualType: "googlesql_base::Status",
						},
					},
				},
			},
		},
	}

	PostProcessQualifyShortNames(spec)

	got := spec.Classes[0].Methods[0].ReturnType
	if got.Name != "googlesql_base::Status" {
		t.Errorf("Name: got %q, want %q (must not be re-qualified)", got.Name, "googlesql_base::Status")
	}
	if got.QualType != "googlesql_base::Status" {
		t.Errorf("QualType: got %q, want %q (must not be re-qualified)", got.QualType, "googlesql_base::Status")
	}
}

// TestPostProcessQualifyShortNames_ClassScopeBeatsNamespace
// verifies the priority order: a bare `Status` inside a method
// of `Outer` resolves to `Outer::Status` (class scope) rather
// than `ns::Status` (namespace scope), matching the C++
// resolution rule "class members shadow namespace members".
func TestPostProcessQualifyShortNames_ClassScopeBeatsNamespace(t *testing.T) {
	spec := &apispec.APISpec{
		Classes: []apispec.Class{
			{
				Name:      "Status",
				Namespace: "ns",
				QualName:  "ns::Status",
			},
			{
				Name:      "Status",
				Namespace: "ns::Outer",
				QualName:  "ns::Outer::Status",
			},
			{
				Name:      "Outer",
				Namespace: "ns",
				QualName:  "ns::Outer",
				Methods: []apispec.Function{
					{
						Name:     "f",
						QualName: "ns::Outer::f",
						ReturnType: apispec.TypeRef{
							Kind:     apispec.TypeValue,
							Name:     "Status",
							QualType: "Status",
						},
					},
				},
			},
		},
	}

	PostProcessQualifyShortNames(spec)

	got := spec.Classes[2].Methods[0].ReturnType
	if got.Name != "ns::Outer::Status" {
		t.Errorf("class scope must shadow namespace scope: got %q, want %q",
			got.Name, "ns::Outer::Status")
	}
}

// TestPostProcessQualifyShortNames_TemplateInnerRecurses verifies
// that template element types (ref.Inner) are rewritten too.
// `std::vector<Foo>` declared inside `ns` should produce
// inner.Name == "ns::Foo" so downstream code doesn't have to
// re-resolve template arguments separately.
func TestPostProcessQualifyShortNames_TemplateInnerRecurses(t *testing.T) {
	spec := &apispec.APISpec{
		Classes: []apispec.Class{
			{
				Name:      "Foo",
				Namespace: "ns",
				QualName:  "ns::Foo",
			},
			{
				Name:      "Container",
				Namespace: "ns",
				QualName:  "ns::Container",
				Methods: []apispec.Function{
					{
						Name:     "all",
						QualName: "ns::Container::all",
						ReturnType: apispec.TypeRef{
							Kind:     apispec.TypeVector,
							Name:     "std::vector",
							QualType: "std::vector<Foo>",
							Inner: &apispec.TypeRef{
								Kind:     apispec.TypeHandle,
								Name:     "Foo",
								QualType: "Foo",
							},
						},
					},
				},
			},
		},
	}

	PostProcessQualifyShortNames(spec)

	inner := spec.Classes[1].Methods[0].ReturnType.Inner
	if inner == nil {
		t.Fatal("expected Inner to be set")
	}
	if inner.Name != "ns::Foo" {
		t.Errorf("inner Name: got %q, want %q", inner.Name, "ns::Foo")
	}
	if inner.QualType != "ns::Foo" {
		t.Errorf("inner QualType: got %q, want %q", inner.QualType, "ns::Foo")
	}
}

// TestPostProcessQualifyShortNames_FreeFunctionUsesNamespace
// verifies that free (non-method) functions still get their
// parameter types qualified using the function's enclosing
// namespace, even though there is no class scope to consult.
func TestPostProcessQualifyShortNames_FreeFunctionUsesNamespace(t *testing.T) {
	spec := &apispec.APISpec{
		Classes: []apispec.Class{
			{
				Name:      "Result",
				Namespace: "ns",
				QualName:  "ns::Result",
			},
		},
		Functions: []apispec.Function{
			{
				Name:      "Compute",
				Namespace: "ns",
				QualName:  "ns::Compute",
				ReturnType: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "void", QualType: "void"},
				Params: []apispec.Param{
					{
						Name: "out",
						Type: apispec.TypeRef{
							Kind:      apispec.TypeHandle,
							Name:      "Result",
							QualType:  "Result *",
							IsPointer: true,
						},
					},
				},
			},
		},
	}

	PostProcessQualifyShortNames(spec)

	got := spec.Functions[0].Params[0].Type
	if got.Name != "ns::Result" {
		t.Errorf("Name: got %q, want %q", got.Name, "ns::Result")
	}
	if got.QualType != "ns::Result *" {
		t.Errorf("QualType: got %q, want %q", got.QualType, "ns::Result *")
	}
}

// TestPostProcessQualifyShortNames_GlobalScopeNoFallback verifies
// that a bare type at global scope (no namespace, no class) is
// left alone -- the fallback "prepend namespace" only fires for
// types declared inside some namespace.
func TestPostProcessQualifyShortNames_GlobalScopeNoFallback(t *testing.T) {
	spec := &apispec.APISpec{
		Functions: []apispec.Function{
			{
				Name:       "GlobalFn",
				Namespace:  "",
				QualName:   "GlobalFn",
				ReturnType: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "void", QualType: "void"},
				Params: []apispec.Param{
					{
						Name: "x",
						Type: apispec.TypeRef{
							Kind:      apispec.TypeHandle,
							Name:      "Unparsed",
							QualType:  "Unparsed *",
							IsPointer: true,
						},
					},
				},
			},
		},
	}

	PostProcessQualifyShortNames(spec)

	got := spec.Functions[0].Params[0].Type
	if got.Name != "Unparsed" {
		t.Errorf("Name: got %q, want %q (global scope must stay unchanged)", got.Name, "Unparsed")
	}
	if got.QualType != "Unparsed *" {
		t.Errorf("QualType: got %q, want %q (global scope must stay unchanged)", got.QualType, "Unparsed *")
	}
}
