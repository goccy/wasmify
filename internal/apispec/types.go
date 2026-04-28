package apispec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// TypeKind classifies how a C/C++ type maps to the bridge layer.
type TypeKind string

const (
	TypePrimitive TypeKind = "primitive" // int, float, double, bool, etc.
	TypeString    TypeKind = "string"    // std::string, const char*, std::string_view
	TypeHandle    TypeKind = "handle"    // pointer/reference to a class with methods
	TypeValue     TypeKind = "value"     // POD struct passed by value
	TypeEnum      TypeKind = "enum"      // enum or enum class
	TypeVector    TypeKind = "vector"    // std::vector<T>
	TypeVoid      TypeKind = "void"      // void return type
	TypeUnknown   TypeKind = "unknown"   // unmappable type
)

// APISpec is the top-level structure for api-spec.json.
type APISpec struct {
	Namespace  string     `json:"namespace,omitempty"`
	Functions  []Function `json:"functions,omitempty"`
	Classes    []Class    `json:"classes,omitempty"`
	Enums      []Enum     `json:"enums,omitempty"`
	SourceFile string     `json:"source_file,omitempty"`
}

// Function represents a free function, static method, member method,
// or constructor. When IsConstructor is true, the function has no return
// type (or returns the class itself) and Name holds the class's simple name.
type Function struct {
	Name       string  `json:"name"`
	Namespace  string  `json:"namespace,omitempty"`
	QualName   string  `json:"qual_name,omitempty"` // fully qualified name
	ReturnType TypeRef `json:"return_type"`
	Params     []Param `json:"params"`
	IsStatic   bool    `json:"is_static,omitempty"`
	IsConst     bool   `json:"is_const,omitempty"`
	IsVirtual   bool   `json:"is_virtual,omitempty"`
	// IsPureVirtual indicates a virtual method declared `= 0`. Callers
	// providing a Go implementation of an abstract class must override
	// exactly these. The callback-trampoline generator uses this to decide
	// which methods to forward over wasmify_callback_invoke.
	IsPureVirtual bool `json:"is_pure_virtual,omitempty"`
	IsRvalueRef   bool `json:"is_rvalue_ref,omitempty"` // method has && ref-qualifier
	// IsConstructor is true when this Function represents a constructor
	// declared inside a class (CXXConstructorDecl). Constructors have no
	// real return type; the bridge generator emits `new ClassName(args)`
	// and returns the pointer as a handle.
	IsConstructor bool   `json:"is_constructor,omitempty"`
	Access        string `json:"access,omitempty"` // public, protected, private
	// OriginalName holds the unmodified C++ name when Name has been
	// altered (e.g., by disambiguateOverloads appending a numeric suffix
	// for overloaded functions). Use this for generating actual C++ calls.
	OriginalName string `json:"original_name,omitempty"`
	SourceFile   string `json:"source_file,omitempty"` // header file where declared
	Comment      string `json:"comment,omitempty"`
}

// Param represents a function parameter.
type Param struct {
	Name string  `json:"name"`
	Type TypeRef `json:"type"`
}

// Class represents a C++ class or struct.
type Class struct {
	Name       string     `json:"name"`
	Namespace  string     `json:"namespace,omitempty"`
	QualName   string     `json:"qual_name,omitempty"`
	Parent     string     `json:"parent,omitempty"`     // base class qualified name
	Parents    []string   `json:"parents,omitempty"`    // all base classes (for multiple inheritance)
	Fields     []Field    `json:"fields,omitempty"`
	Methods    []Function `json:"methods,omitempty"`
	IsHandle   bool       `json:"is_handle"`            // true if typically used via pointer/reference
	IsAbstract bool       `json:"is_abstract,omitempty"`
	// HasPublicDefaultCtor indicates the class has an accessible default
	// constructor (either implicit or explicitly declared public and not deleted).
	// This determines whether the bridge can declare a local variable of this type.
	HasPublicDefaultCtor bool `json:"has_public_default_ctor,omitempty"`
	// HasDeletedCopyCtor indicates the class has an explicitly-deleted copy
	// constructor. Such types cannot be passed by value.
	HasDeletedCopyCtor bool `json:"has_deleted_copy_ctor,omitempty"`
	// HasPublicDtor indicates the class has an accessible (public) destructor.
	// Classes with private or protected destructors cannot be deleted from
	// outside the class, so bridge code cannot call `delete` on them (e.g.,
	// singleton types with private destructors).
	HasPublicDtor bool `json:"has_public_dtor,omitempty"`
	// HasDeletedOperatorNew indicates the class has an explicitly-deleted
	// `operator new`. Such classes (e.g., arena-allocated protobuf messages)
	// cannot be heap-allocated via `new`, so bridge code cannot use
	// `new Type(value)` to copy the result.
	HasDeletedOperatorNew bool `json:"has_deleted_operator_new,omitempty"`
	// HasPrivateFields indicates the class has at least one non-public
	// (private or protected) data member. This matters for bridge design:
	// such state is invisible outside the class and can only be observed
	// or mutated through public methods. A class with private state
	// therefore cannot be safely serialized as a "value" because the
	// bridge has no way to reconstruct the private members from a proto
	// message. Such classes must be treated as handles so the C++ object
	// stays alive and successive method calls operate on the same object.
	HasPrivateFields bool  `json:"has_private_fields,omitempty"`
	SourceFile            string `json:"source_file,omitempty"` // header file where declared
	Comment               string `json:"comment,omitempty"`
}

// Field represents a class/struct field.
type Field struct {
	Name    string  `json:"name"`
	Type    TypeRef `json:"type"`
	Access  string  `json:"access,omitempty"`
	Comment string  `json:"comment,omitempty"`
}

// Enum represents a C/C++ enum.
type Enum struct {
	Name       string      `json:"name"`
	Namespace  string      `json:"namespace,omitempty"`
	QualName   string      `json:"qual_name,omitempty"`
	IsScoped   bool        `json:"is_scoped,omitempty"` // enum class
	Values     []EnumValue `json:"values"`
	SourceFile string      `json:"source_file,omitempty"` // header file where declared
	Comment    string      `json:"comment,omitempty"`
}

// EnumValue represents a single enumerator.
type EnumValue struct {
	Name    string `json:"name"`
	Value   int64  `json:"value"`
	Comment string `json:"comment,omitempty"`
}

// TypeRef describes a type reference with all qualifiers.
type TypeRef struct {
	Name      string   `json:"name"`                 // display name: "int32_t", "std::string", "ResolvedAST*"
	Kind      TypeKind `json:"kind"`                 // classification
	Inner     *TypeRef `json:"inner,omitempty"`       // for vector<T>, the T
	IsConst   bool     `json:"is_const,omitempty"`
	IsPointer bool     `json:"is_pointer,omitempty"`
	IsRef     bool     `json:"is_reference,omitempty"`
	QualType  string   `json:"qual_type,omitempty"`   // clang's qualified type string
}

// SortSpec sorts all slices in the APISpec for deterministic/idempotent output.
// Functions, Classes, and Enums are sorted by QualName (falling back to Name).
// Within each Class, Methods are sorted by QualName and Fields by Name.
// Within each Enum, Values are sorted by their numeric Value.
func SortSpec(spec *APISpec) {
	sort.Slice(spec.Functions, func(i, j int) bool {
		qi, qj := spec.Functions[i].QualName, spec.Functions[j].QualName
		if qi == "" {
			qi = spec.Functions[i].Name
		}
		if qj == "" {
			qj = spec.Functions[j].Name
		}
		return qi < qj
	})
	sort.Slice(spec.Classes, func(i, j int) bool {
		qi, qj := spec.Classes[i].QualName, spec.Classes[j].QualName
		if qi == "" {
			qi = spec.Classes[i].Name
		}
		if qj == "" {
			qj = spec.Classes[j].Name
		}
		return qi < qj
	})
	for idx := range spec.Classes {
		c := &spec.Classes[idx]
		sort.Slice(c.Methods, func(i, j int) bool {
			qi, qj := c.Methods[i].QualName, c.Methods[j].QualName
			if qi == "" {
				qi = c.Methods[i].Name
			}
			if qj == "" {
				qj = c.Methods[j].Name
			}
			return qi < qj
		})
		sort.Slice(c.Fields, func(i, j int) bool {
			return c.Fields[i].Name < c.Fields[j].Name
		})
	}
	sort.Slice(spec.Enums, func(i, j int) bool {
		qi, qj := spec.Enums[i].QualName, spec.Enums[j].QualName
		if qi == "" {
			qi = spec.Enums[i].Name
		}
		if qj == "" {
			qj = spec.Enums[j].Name
		}
		return qi < qj
	})
	for idx := range spec.Enums {
		e := &spec.Enums[idx]
		sort.Slice(e.Values, func(i, j int) bool {
			return e.Values[i].Value < e.Values[j].Value
		})
	}
}

// Save writes the APISpec to api-spec.json in the given directory.
// The spec is sorted before serialization to ensure deterministic output.
func Save(dataDir string, spec *APISpec) error {
	SortSpec(spec)
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal api-spec.json: %w", err)
	}
	path := filepath.Join(dataDir, "api-spec.json")
	return os.WriteFile(path, data, 0o644)
}

// Load reads api-spec.json from the given directory.
func Load(dataDir string) (*APISpec, error) {
	path := filepath.Join(dataDir, "api-spec.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read api-spec.json: %w", err)
	}
	var spec APISpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("failed to parse api-spec.json: %w", err)
	}
	return &spec, nil
}
