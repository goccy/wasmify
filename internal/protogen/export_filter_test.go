package protogen

import (
	"testing"

	"github.com/goccy/wasmify/internal/apispec"
)

func TestApplyExportFilter_FunctionsOnly(t *testing.T) {
	spec := &apispec.APISpec{
		Namespace: "mylib",
		Functions: []apispec.Function{
			{Name: "Parse", QualName: "mylib::Parse", ReturnType: apispec.TypeRef{Name: "Output", Kind: apispec.TypeHandle}},
			{Name: "Unused", QualName: "mylib::Unused"},
		},
		Classes: []apispec.Class{
			{Name: "Output", QualName: "mylib::Output", IsHandle: true},
			{Name: "Unrelated", QualName: "mylib::Unrelated", IsHandle: true},
		},
	}
	cfg := BridgeConfig{ExportFunctions: []string{"mylib::Parse"}}

	filtered := ApplyExportFilter(spec, cfg)

	if len(filtered.Functions) != 1 || filtered.Functions[0].QualName != "mylib::Parse" {
		t.Errorf("expected 1 function (mylib::Parse), got %d", len(filtered.Functions))
	}
	// Output should be included (return type dependency)
	found := false
	for _, c := range filtered.Classes {
		if c.QualName == "mylib::Output" {
			found = true
		}
		if c.QualName == "mylib::Unrelated" {
			t.Error("Unrelated should not be included")
		}
	}
	if !found {
		t.Error("Output class should be included as return type dependency")
	}
}

func TestApplyExportFilter_ClassConstructor(t *testing.T) {
	spec := &apispec.APISpec{
		Namespace: "mylib",
		Classes: []apispec.Class{
			{Name: "Catalog", QualName: "mylib::Catalog", IsHandle: true},
			{Name: "Unused", QualName: "mylib::Unused", IsHandle: true},
		},
	}
	cfg := BridgeConfig{ExportFunctions: []string{"mylib::Catalog"}}

	filtered := ApplyExportFilter(spec, cfg)

	found := false
	for _, c := range filtered.Classes {
		if c.QualName == "mylib::Catalog" {
			found = true
			if !c.IsHandle {
				t.Error("explicitly exported class should be promoted to handle")
			}
		}
	}
	if !found {
		t.Error("Catalog should be in filtered classes")
	}
}

func TestApplyExportFilter_AbstractSubclasses(t *testing.T) {
	spec := &apispec.APISpec{
		Namespace: "mylib",
		Functions: []apispec.Function{
			{Name: "GetType", QualName: "mylib::GetType", ReturnType: apispec.TypeRef{Name: "Type", Kind: apispec.TypeHandle}},
		},
		Classes: []apispec.Class{
			{Name: "Type", QualName: "mylib::Type", IsHandle: true, IsAbstract: true},
			{Name: "SimpleType", QualName: "mylib::SimpleType", IsHandle: true, Parent: "mylib::Type"},
			{Name: "ArrayType", QualName: "mylib::ArrayType", IsHandle: true, Parent: "mylib::Type"},
			{Name: "Unrelated", QualName: "mylib::Unrelated", IsHandle: true},
		},
	}
	cfg := BridgeConfig{ExportFunctions: []string{"mylib::GetType"}}

	filtered := ApplyExportFilter(spec, cfg)

	names := map[string]bool{}
	for _, c := range filtered.Classes {
		names[c.QualName] = true
	}

	if !names["mylib::Type"] {
		t.Error("abstract Type should be included")
	}
	if !names["mylib::SimpleType"] {
		t.Error("SimpleType (subclass of abstract Type) should be included")
	}
	if !names["mylib::ArrayType"] {
		t.Error("ArrayType (subclass of abstract Type) should be included")
	}
	if names["mylib::Unrelated"] {
		t.Error("Unrelated should not be included")
	}
}

func TestApplyExportFilter_ParentChainResolution(t *testing.T) {
	spec := &apispec.APISpec{
		Namespace: "mylib",
		Classes: []apispec.Class{
			{Name: "Base", QualName: "mylib::Base", IsHandle: true, IsAbstract: true},
			{Name: "Middle", QualName: "mylib::Middle", IsHandle: true, Parent: "mylib::Base"},
			{Name: "Concrete", QualName: "mylib::Concrete", IsHandle: true, Parent: "Middle",
				Parents: []string{"Middle"}},
		},
	}
	cfg := BridgeConfig{ExportFunctions: []string{"mylib::Concrete"}}

	filtered := ApplyExportFilter(spec, cfg)

	names := map[string]bool{}
	for _, c := range filtered.Classes {
		names[c.QualName] = true
	}

	if !names["mylib::Concrete"] {
		t.Error("Concrete should be included")
	}
	if !names["mylib::Middle"] {
		t.Error("Middle (parent) should be included")
	}
	if !names["mylib::Base"] {
		t.Error("Base (grandparent) should be included")
	}
}

func TestApplyExportFilter_AmbiguousShortName(t *testing.T) {
	spec := &apispec.APISpec{
		Namespace: "mylib",
		Functions: []apispec.Function{
			{Name: "GetCol", QualName: "mylib::GetCol", ReturnType: apispec.TypeRef{Name: "Column", Kind: apispec.TypeHandle}},
		},
		Classes: []apispec.Class{
			{Name: "Column", QualName: "mylib::Column", IsHandle: true},
			{Name: "Column", QualName: "mylib::internal::Column", IsHandle: true},
		},
	}
	cfg := BridgeConfig{ExportFunctions: []string{"mylib::GetCol"}}

	filtered := ApplyExportFilter(spec, cfg)

	// Both Column classes should be included (ambiguous short name → include all)
	names := map[string]bool{}
	for _, c := range filtered.Classes {
		names[c.QualName] = true
	}
	if !names["mylib::Column"] {
		t.Error("mylib::Column should be included")
	}
	if !names["mylib::internal::Column"] {
		t.Error("mylib::internal::Column should also be included (ambiguous short name)")
	}
}

func TestApplyExportFilter_ValueTypePromotion(t *testing.T) {
	spec := &apispec.APISpec{
		Namespace: "mylib",
		Classes: []apispec.Class{
			{
				Name:     "Config",
				QualName: "mylib::Config",
				IsHandle: false, // value type (not a handle)
				Fields: []apispec.Field{
					{Name: "name", Type: apispec.TypeRef{Name: "string", Kind: apispec.TypeString}},
				},
			},
		},
	}
	cfg := BridgeConfig{ExportFunctions: []string{"mylib::Config"}}

	filtered := ApplyExportFilter(spec, cfg)

	found := false
	for _, c := range filtered.Classes {
		if c.QualName == "mylib::Config" {
			found = true
			if !c.IsHandle {
				t.Error("non-handle class explicitly listed in ExportFunctions should be promoted to handle (IsHandle = true)")
			}
		}
	}
	if !found {
		t.Error("Config should be in filtered classes")
	}
}

func TestApplyExportFilter_EmptyExportFunctions(t *testing.T) {
	spec := &apispec.APISpec{
		Namespace: "mylib",
		Functions: []apispec.Function{
			{Name: "Foo", QualName: "mylib::Foo"},
		},
	}
	cfg := BridgeConfig{} // No ExportFunctions

	filtered := ApplyExportFilter(spec, cfg)

	// Should return spec unchanged
	if len(filtered.Functions) != len(spec.Functions) {
		t.Error("empty ExportFunctions should return spec unchanged")
	}
}
