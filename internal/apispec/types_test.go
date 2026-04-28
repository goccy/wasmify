package apispec

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	original := &APISpec{
		Namespace: "demo",
		Functions: []Function{
			{Name: "foo", QualName: "demo::foo", ReturnType: TypeRef{Name: "int", Kind: TypePrimitive}},
		},
		Classes: []Class{
			{Name: "Bar", QualName: "demo::Bar", IsHandle: true},
		},
		Enums: []Enum{
			{Name: "Color", QualName: "demo::Color", Values: []EnumValue{{Name: "Red", Value: 0}}},
		},
		SourceFile: "demo.h",
	}

	if err := Save(dir, original); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "api-spec.json")); err != nil {
		t.Fatalf("api-spec.json not created: %v", err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if loaded == nil {
		t.Fatal("Load() returned nil after Save")
	}
	if loaded.Namespace != "demo" {
		t.Errorf("Namespace = %q, want demo", loaded.Namespace)
	}
	if len(loaded.Functions) != 1 || loaded.Functions[0].Name != "foo" {
		t.Errorf("Functions mismatch: %+v", loaded.Functions)
	}
	if len(loaded.Classes) != 1 || !loaded.Classes[0].IsHandle {
		t.Errorf("Classes mismatch: %+v", loaded.Classes)
	}
	if len(loaded.Enums) != 1 || loaded.Enums[0].Name != "Color" {
		t.Errorf("Enums mismatch: %+v", loaded.Enums)
	}
}

func TestLoad_NonExistent(t *testing.T) {
	dir := t.TempDir()
	spec, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if spec != nil {
		t.Errorf("Load() returned %+v for missing file, want nil", spec)
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "api-spec.json"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestSortSpec_SortsFunctionsByQualName(t *testing.T) {
	spec := &APISpec{
		Functions: []Function{
			{Name: "c", QualName: "z::c"},
			{Name: "a", QualName: "a::a"},
			{Name: "b", QualName: "m::b"},
		},
	}
	SortSpec(spec)
	got := []string{spec.Functions[0].QualName, spec.Functions[1].QualName, spec.Functions[2].QualName}
	want := []string{"a::a", "m::b", "z::c"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Functions[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSortSpec_FallsBackToName(t *testing.T) {
	spec := &APISpec{
		Functions: []Function{
			{Name: "zebra"},
			{Name: "apple"},
		},
		Classes: []Class{
			{Name: "Zeta"},
			{Name: "Alpha"},
		},
		Enums: []Enum{
			{Name: "Z"},
			{Name: "A"},
		},
	}
	SortSpec(spec)
	if spec.Functions[0].Name != "apple" {
		t.Errorf("Functions[0] = %q, want apple", spec.Functions[0].Name)
	}
	if spec.Classes[0].Name != "Alpha" {
		t.Errorf("Classes[0] = %q, want Alpha", spec.Classes[0].Name)
	}
	if spec.Enums[0].Name != "A" {
		t.Errorf("Enums[0] = %q, want A", spec.Enums[0].Name)
	}
}

func TestSortSpec_SortsClassInternals(t *testing.T) {
	spec := &APISpec{
		Classes: []Class{
			{
				Name:     "Foo",
				QualName: "Foo",
				Methods: []Function{
					{Name: "zeta", QualName: "Foo::zeta"},
					{Name: "alpha", QualName: "Foo::alpha"},
				},
				Fields: []Field{
					{Name: "y"},
					{Name: "x"},
				},
			},
		},
	}
	SortSpec(spec)
	c := spec.Classes[0]
	if c.Methods[0].QualName != "Foo::alpha" {
		t.Errorf("Methods[0] = %q, want Foo::alpha", c.Methods[0].QualName)
	}
	if c.Fields[0].Name != "x" {
		t.Errorf("Fields[0] = %q, want x", c.Fields[0].Name)
	}
}

func TestSortSpec_ClassMethodsFallbackToName(t *testing.T) {
	spec := &APISpec{
		Classes: []Class{
			{Name: "C", Methods: []Function{{Name: "z"}, {Name: "a"}}},
		},
	}
	SortSpec(spec)
	if spec.Classes[0].Methods[0].Name != "a" {
		t.Errorf("Methods[0] = %q, want a", spec.Classes[0].Methods[0].Name)
	}
}

func TestSortSpec_EnumValuesSortedByValue(t *testing.T) {
	spec := &APISpec{
		Enums: []Enum{
			{
				Name:     "Color",
				QualName: "Color",
				Values: []EnumValue{
					{Name: "Blue", Value: 2},
					{Name: "Red", Value: 0},
					{Name: "Green", Value: 1},
				},
			},
		},
	}
	SortSpec(spec)
	want := []int64{0, 1, 2}
	for i, v := range spec.Enums[0].Values {
		if v.Value != want[i] {
			t.Errorf("Values[%d].Value = %d, want %d", i, v.Value, want[i])
		}
	}
}

func TestSortSpec_Deterministic(t *testing.T) {
	build := func() *APISpec {
		return &APISpec{
			Functions: []Function{{Name: "b"}, {Name: "a"}},
			Classes:   []Class{{Name: "B"}, {Name: "A"}},
			Enums:     []Enum{{Name: "Y"}, {Name: "X"}},
		}
	}
	s1 := build()
	s2 := build()
	SortSpec(s1)
	SortSpec(s2)

	if s1.Functions[0].Name != s2.Functions[0].Name {
		t.Error("SortSpec not deterministic for Functions")
	}
	if s1.Classes[0].Name != s2.Classes[0].Name {
		t.Error("SortSpec not deterministic for Classes")
	}
	if s1.Enums[0].Name != s2.Enums[0].Name {
		t.Error("SortSpec not deterministic for Enums")
	}
}

func TestSave_SortsBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	spec := &APISpec{
		Functions: []Function{
			{Name: "zzz", QualName: "zzz"},
			{Name: "aaa", QualName: "aaa"},
		},
	}
	if err := Save(dir, spec); err != nil {
		t.Fatal(err)
	}
	// Save mutates its input (in-place sort); verify the effect.
	if spec.Functions[0].Name != "aaa" {
		t.Errorf("Save did not sort in place: %+v", spec.Functions)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Functions[0].Name != "aaa" {
		t.Errorf("loaded Functions[0] = %q, want aaa", loaded.Functions[0].Name)
	}
}
