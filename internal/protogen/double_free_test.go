package protogen

import (
	"strings"
	"testing"

	"github.com/goccy/wasmify/internal/apispec"
)

// TestDoubleFreeFix_ProtoMarker pins the proto-emission half of the
// double-free fix. The .proto file emitted by GenerateProto must carry
// `[(wasmify.wasm_take_ownership) = true]` on request fields whose C++
// counterpart absorbs the wrapped pointer (unique_ptr<T> by value or
// rvalue-ref) and MUST NOT carry it on raw pointers or shared_ptr (the
// latter is reference-counted and safe).
//
// Before the fix the marker did not exist at all, so the post-invoke
// `clearPtr()` lines could not be wired up by protoc-gen-wasmify-go and
// the Go-side finalizer would double-free memory the C++ unique_ptr
// destructor had already reclaimed.
func TestDoubleFreeFix_ProtoMarker(t *testing.T) {
	spec := &apispec.APISpec{
		Namespace: "mylib",
		Classes: []apispec.Class{
			{
				Name: "Item", QualName: "mylib::Item", Namespace: "mylib",
				IsHandle: true, HasPublicDefaultCtor: true, HasPublicDtor: true,
				SourceFile: "mylib/types.h",
			},
			{
				Name: "Box", QualName: "mylib::Box", Namespace: "mylib",
				IsHandle: true, HasPublicDefaultCtor: true, HasPublicDtor: true,
				SourceFile: "mylib/types.h",
				Methods: []apispec.Function{
					{
						Name: "AddOwned", QualName: "mylib::Box::AddOwned",
						ReturnType: apispec.TypeRef{Kind: apispec.TypeVoid},
						Params: []apispec.Param{
							{
								Name: "item",
								Type: apispec.TypeRef{
									Kind:     apispec.TypeHandle,
									Name:     "Item",
									QualType: "std::unique_ptr<mylib::Item>",
								},
							},
						},
						SourceFile: "mylib/types.h",
					},
					{
						Name: "AddShared", QualName: "mylib::Box::AddShared",
						ReturnType: apispec.TypeRef{Kind: apispec.TypeVoid},
						Params: []apispec.Param{
							{
								Name: "item",
								Type: apispec.TypeRef{
									Kind:     apispec.TypeHandle,
									Name:     "Item",
									QualType: "std::shared_ptr<mylib::Item>",
								},
							},
						},
						SourceFile: "mylib/types.h",
					},
					{
						Name: "AddRaw", QualName: "mylib::Box::AddRaw",
						ReturnType: apispec.TypeRef{Kind: apispec.TypeVoid},
						Params: []apispec.Param{
							{
								Name: "item",
								Type: apispec.TypeRef{
									Kind:     apispec.TypeHandle,
									Name:     "Item",
									QualType: "mylib::Item *",
								},
							},
						},
						SourceFile: "mylib/types.h",
					},
				},
			},
		},
	}

	out := GenerateProto(spec, "mylib")

	addOwned := extractRequestMessage(t, out, "BoxAddOwnedRequest")
	if !strings.Contains(addOwned, "[(wasmify.wasm_take_ownership) = true]") {
		t.Fatalf("AddOwned (unique_ptr<Item>) request must carry wasm_take_ownership marker; got:\n%s", addOwned)
	}

	addShared := extractRequestMessage(t, out, "BoxAddSharedRequest")
	if strings.Contains(addShared, "wasm_take_ownership") {
		t.Fatalf("AddShared (shared_ptr<Item>) request must NOT carry wasm_take_ownership (shared_ptr is ref-counted); got:\n%s", addShared)
	}

	addRaw := extractRequestMessage(t, out, "BoxAddRawRequest")
	if strings.Contains(addRaw, "wasm_take_ownership") {
		t.Fatalf("AddRaw (raw pointer) request must NOT carry wasm_take_ownership (no static ownership signal); got:\n%s", addRaw)
	}
}

// TestDoubleFreeFix_ParamPredicate isolates paramTakesOwnership so a
// regression in the smart-pointer-classification helpers (smartPointerInner
// / isSharedPointerType in bridge.go) is caught even if proto emission
// happens to mask it.
func TestDoubleFreeFix_ParamPredicate(t *testing.T) {
	cases := []struct {
		qual string
		want bool
	}{
		{"std::unique_ptr<Foo>", true},
		{"std::unique_ptr<const Foo>", true},
		{"unique_ptr<Foo>", true},
		{"std::unique_ptr<Foo>&&", true},
		{"std::shared_ptr<Foo>", false},
		{"std::shared_ptr<const Foo>", false},
		{"shared_ptr<Foo>", false},
		{"Foo *", false},
		{"const Foo &", false},
		{"int", false},
		{"", false},
	}
	for _, c := range cases {
		got := paramTakesOwnership(apispec.Param{
			Type: apispec.TypeRef{QualType: c.qual},
		})
		if got != c.want {
			t.Errorf("paramTakesOwnership(%q) = %v, want %v", c.qual, got, c.want)
		}
	}
}

// extractRequestMessage returns the body of `message <name> { ... }` from
// a generated .proto string. Fails the test if the message is missing.
func extractRequestMessage(t *testing.T, proto, name string) string {
	t.Helper()
	hdr := "message " + name + " {"
	i := strings.Index(proto, hdr)
	if i < 0 {
		t.Fatalf("generated proto does not contain message %s; full output:\n%s", name, proto)
	}
	rest := proto[i:]
	end := strings.Index(rest, "\n}")
	if end < 0 {
		t.Fatalf("message %s is not closed; got:\n%s", name, rest)
	}
	return rest[:end+2]
}
