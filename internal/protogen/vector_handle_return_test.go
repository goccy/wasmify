package protogen

import (
	"strings"
	"testing"

	"github.com/goccy/wasmify/internal/apispec"
)

// widgetSpec is a handle class mylib::Widget with the given methods and
// fields, enough for the bridge and proto generators to emit them.
func widgetSpec(methods []apispec.Function, fields []apispec.Field) *apispec.APISpec {
	src := "mylib/widget.h"
	for i := range methods {
		methods[i].QualName = "mylib::Widget::" + methods[i].Name
		methods[i].SourceFile = src
	}
	return &apispec.APISpec{
		Namespace: "mylib",
		Classes: []apispec.Class{{
			Name:                 "Widget",
			QualName:             "mylib::Widget",
			Namespace:            "mylib",
			IsHandle:             true,
			HasPublicDefaultCtor: true,
			HasPublicDtor:        true,
			Methods:              methods,
			Fields:               fields,
			SourceFile:           src,
		}},
	}
}

func widgetVector(isConst, isRef bool, qualType string) apispec.TypeRef {
	return apispec.TypeRef{
		Kind:     apispec.TypeVector,
		Name:     "std::vector<mylib::Widget>",
		QualType: qualType,
		IsConst:  isConst,
		IsRef:    isRef,
		Inner:    &apispec.TypeRef{Kind: apispec.TypeHandle, Name: "mylib::Widget", QualType: "mylib::Widget"},
	}
}

var statusOrBridgeConfig = BridgeConfig{
	ExternalTypes: []string{"absl::StatusOr"},
	ErrorTypes: map[string]string{
		"absl::StatusOr": `if (!{result}.ok()) { _pw.write_error(std::string({result}.status().message())); }`,
	},
}

// methodBody returns the dispatch body the bridge emits for the method,
// from the comment naming it up to the next export.
func methodBody(t *testing.T, bridge, method string) string {
	t.Helper()
	marker := "// " + method
	start := strings.Index(bridge, marker)
	if start < 0 {
		t.Fatalf("bridge has no body for %s:\n%s", method, bridge)
	}
	rest := bridge[start+len(marker):]
	if end := strings.Index(rest, "\nWASM_EXPORT"); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

func TestGenerateBridge_VectorOfHandlesByValue_HeapCopiesElements(t *testing.T) {
	spec := widgetSpec([]apispec.Function{{
		Name:       "Children",
		ReturnType: widgetVector(false, false, "std::vector<mylib::Widget>"),
	}}, nil)
	body := methodBody(t, GenerateBridgeWithConfig(spec, "mylib", "", BridgeConfig{}), "Children")

	// _result is a local: the address of one of its elements dangles
	// once the dispatch function returns.
	if strings.Contains(body, "&_elem") {
		t.Errorf("by-value vector return writes element addresses of the local:\n%s", body)
	}
	if !strings.Contains(body, "new auto(std::move(_elem))") {
		t.Errorf("by-value vector return does not heap-allocate each element:\n%s", body)
	}
}

func TestGenerateBridge_VectorOfHandlesByConstRef_BorrowsElements(t *testing.T) {
	spec := widgetSpec([]apispec.Function{{
		Name:       "Children",
		IsConst:    true,
		ReturnType: widgetVector(true, true, "const std::vector<mylib::Widget> &"),
	}}, []apispec.Field{{
		Name: "kids",
		Type: widgetVector(false, false, "std::vector<mylib::Widget>"),
	}})
	bridge := GenerateBridgeWithConfig(spec, "mylib", "", BridgeConfig{})

	// The elements live in the object, so the handles borrow them.
	for _, name := range []string{"Children", "GetKids"} {
		body := methodBody(t, bridge, name)
		if !strings.Contains(body, "reinterpret_cast<uint64_t>(&_elem)") {
			t.Errorf("%s: handles to an object's elements should borrow them:\n%s", name, body)
		}
	}
}

func TestGenerateBridge_StatusOrVectorOfHandles_HeapCopiesElements(t *testing.T) {
	spec := widgetSpec([]apispec.Function{{
		Name: "MakeMany",
		ReturnType: apispec.TypeRef{
			Kind:     apispec.TypeValue,
			Name:     "absl::StatusOr<std::vector<mylib::Widget>>",
			QualType: "absl::StatusOr<std::vector<mylib::Widget>>",
		},
	}}, nil)
	body := methodBody(t, GenerateBridgeWithConfig(spec, "mylib", "", statusOrBridgeConfig), "MakeMany")

	if strings.Contains(body, "new auto(std::move(*_result))") {
		t.Errorf("StatusOr<vector<T>> writes the whole vector as one T handle:\n%s", body)
	}
	if !strings.Contains(body, "for (auto& _elem : (*_result))") ||
		!strings.Contains(body, "new auto(std::move(_elem))") {
		t.Errorf("StatusOr<vector<T>> does not heap-allocate each element on success:\n%s", body)
	}
}

func TestGenerateProto_StatusOrVectorOfHandles_Repeated(t *testing.T) {
	spec := widgetSpec([]apispec.Function{{
		Name: "MakeMany",
		ReturnType: apispec.TypeRef{
			Kind:     apispec.TypeValue,
			Name:     "absl::StatusOr<std::vector<mylib::Widget>>",
			QualType: "absl::StatusOr<std::vector<mylib::Widget>>",
		},
	}}, nil)
	output := GenerateProtoWithConfig(spec, "mylib", statusOrBridgeConfig)

	resp := extractMessage(t, output, "WidgetMakeManyResponse")
	if !strings.Contains(resp, "repeated Widget result = 1;") {
		t.Errorf("StatusOr<vector<Widget>> should be repeated Widget; got:\n%s", resp)
	}
}

func uniqueWidgetVector(isConst, isRef bool, qualType string) apispec.TypeRef {
	return apispec.TypeRef{
		Kind:     apispec.TypeVector,
		Name:     "std::vector<std::unique_ptr<mylib::Widget>>",
		QualType: qualType,
		IsConst:  isConst,
		IsRef:    isRef,
		Inner: &apispec.TypeRef{
			Kind:     apispec.TypeHandle,
			Name:     "std::unique_ptr<mylib::Widget>",
			QualType: "std::unique_ptr<mylib::Widget>",
		},
	}
}

func TestGenerateBridge_VectorOfUniquePtrByValue_ReleasesElements(t *testing.T) {
	spec := widgetSpec([]apispec.Function{{
		Name:       "TakeChildren",
		ReturnType: uniqueWidgetVector(false, false, "std::vector<std::unique_ptr<mylib::Widget>>"),
	}}, nil)
	body := methodBody(t, GenerateBridgeWithConfig(spec, "mylib", "", BridgeConfig{}), "TakeChildren")

	// The local's unique_ptrs delete their objects when it dies.
	if strings.Contains(body, "_elem.get()") {
		t.Errorf("by-value vector<unique_ptr> return borrows from the local:\n%s", body)
	}
	if !strings.Contains(body, "_elem.release()") {
		t.Errorf("by-value vector<unique_ptr> return does not release each element:\n%s", body)
	}
}

func TestGenerateBridge_VectorOfUniquePtrByConstRef_BorrowsElements(t *testing.T) {
	spec := widgetSpec([]apispec.Function{{
		Name:       "Children",
		IsConst:    true,
		ReturnType: uniqueWidgetVector(true, true, "const std::vector<std::unique_ptr<mylib::Widget>> &"),
	}}, nil)
	body := methodBody(t, GenerateBridgeWithConfig(spec, "mylib", "", BridgeConfig{}), "Children")

	if !strings.Contains(body, "_elem.get()") || strings.Contains(body, "_elem.release()") {
		t.Errorf("const-ref vector<unique_ptr> return should borrow via get():\n%s", body)
	}
}

func TestGenerateBridge_StatusOrVectorOfUniquePtr_ReleasesElements(t *testing.T) {
	spec := widgetSpec([]apispec.Function{{
		Name: "MakeMany",
		ReturnType: apispec.TypeRef{
			Kind:     apispec.TypeValue,
			Name:     "absl::StatusOr<std::vector<std::unique_ptr<mylib::Widget>>>",
			QualType: "absl::StatusOr<std::vector<std::unique_ptr<mylib::Widget>>>",
		},
	}}, nil)
	body := methodBody(t, GenerateBridgeWithConfig(spec, "mylib", "", statusOrBridgeConfig), "MakeMany")

	if !strings.Contains(body, "for (auto& _elem : (*_result))") || !strings.Contains(body, "_elem.release()") {
		t.Errorf("StatusOr<vector<unique_ptr<T>>> does not release each element on success:\n%s", body)
	}
}

func TestGenerateBridge_StatusOrVectorByConstRef_BorrowsElements(t *testing.T) {
	spec := widgetSpec([]apispec.Function{{
		Name:    "Cached",
		IsConst: true,
		ReturnType: apispec.TypeRef{
			Kind:     apispec.TypeValue,
			Name:     "absl::StatusOr<std::vector<mylib::Widget>>",
			QualType: "const absl::StatusOr<std::vector<mylib::Widget>> &",
			IsConst:  true,
			IsRef:    true,
		},
	}}, nil)
	body := methodBody(t, GenerateBridgeWithConfig(spec, "mylib", "", statusOrBridgeConfig), "Cached")

	// The wrapper outlives the call, so its elements are borrowed.
	if !strings.Contains(body, "reinterpret_cast<uint64_t>(&_elem)") {
		t.Errorf("const-ref StatusOr<vector<T>> should borrow its elements:\n%s", body)
	}
}

func TestTrampolineReturnSupported_StatusOrVectorUnsupported(t *testing.T) {
	prev := bridgeConfig
	bridgeConfig = statusOrBridgeConfig
	defer func() { bridgeConfig = prev }()

	ret := apispec.TypeRef{
		Kind:     apispec.TypeValue,
		Name:     "absl::StatusOr<std::vector<mylib::Widget>>",
		QualType: "absl::StatusOr<std::vector<mylib::Widget>>",
	}
	// The trampoline reads one handle, but the callback response
	// carries the vector as a repeated field.
	if trampolineReturnSupported(ret) {
		t.Errorf("StatusOr<vector<T>> trampoline return should be unsupported")
	}
	single := ret
	single.Name = "absl::StatusOr<mylib::Widget>"
	single.QualType = "absl::StatusOr<mylib::Widget>"
	if !trampolineReturnSupported(single) {
		t.Errorf("StatusOr<T> trampoline return should stay supported")
	}
}
