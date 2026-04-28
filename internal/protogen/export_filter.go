package protogen

import (
	"strings"

	"github.com/goccy/wasmify/internal/apispec"
)

// ApplyExportFilter filters the api-spec to only include the specified
// functions and their transitive type dependencies. If no ExportFunctions
// are specified, the spec is returned unchanged.
//
// Starting from each exported function, the filter recursively collects:
// - Parameter and return types
// - Parent classes (inheritance chain)
// - Method parameter and return types on collected classes
// - Field types on collected classes
// - Inner types of smart pointers and vectors
// - Referenced enums
func ApplyExportFilter(spec *apispec.APISpec, cfg BridgeConfig) *apispec.APISpec {
	if len(cfg.ExportFunctions) == 0 {
		return spec
	}

	// Build lookup maps
	funcByQual := map[string]*apispec.Function{}
	for i := range spec.Functions {
		funcByQual[spec.Functions[i].QualName] = &spec.Functions[i]
	}
	classByQual := map[string]*apispec.Class{}
	classByShort := map[string]*apispec.Class{} // short name → class (top-level ns preferred)
	classAllByShort := map[string][]*apispec.Class{} // short name → all classes with that name
	for i := range spec.Classes {
		c := &spec.Classes[i]
		classByQual[c.QualName] = c
		classAllByShort[c.Name] = append(classAllByShort[c.Name], c)
		if existing, exists := classByShort[c.Name]; !exists {
			classByShort[c.Name] = c
		} else if spec.Namespace != "" {
			// Prefer the class in the project's top-level namespace
			// (e.g., ns::Column over ns::reflection::Column)
			ns := spec.Namespace
			if strings.HasPrefix(c.QualName, ns+"::") &&
				!strings.Contains(c.QualName[len(ns)+2:], "::") &&
				strings.Contains(existing.QualName[len(ns)+2:], "::") {
				classByShort[c.Name] = c
			}
		}
	}
	enumByQual := map[string]*apispec.Enum{}
	enumByShort := map[string]*apispec.Enum{}
	for i := range spec.Enums {
		e := &spec.Enums[i]
		enumByQual[e.QualName] = e
		if _, exists := enumByShort[e.Name]; !exists {
			enumByShort[e.Name] = e
		}
	}

	// Seed: collect explicitly exported functions and classes
	needFuncs := map[string]bool{}
	needClasses := map[string]bool{}
	needEnums := map[string]bool{}

	for _, name := range cfg.ExportFunctions {
		if _, ok := funcByQual[name]; ok {
			needFuncs[name] = true
		} else if _, ok := classByQual[name]; ok {
			// ExportFunctions can also list class qualified names to
			// include classes (and their constructors) that aren't
			// transitively reachable from free functions.
			needClasses[name] = true
		}
	}

	// Resolve type dependencies recursively
	resolveType := func(ref apispec.TypeRef) {
		collectTypeDeps(ref, classByQual, classByShort, classAllByShort, enumByQual, enumByShort, needClasses, needEnums)
	}

	changed := true
	for changed {
		changed = false

		// Collect deps from exported functions
		for name := range needFuncs {
			fn := funcByQual[name]
			if fn == nil {
				continue
			}
			resolveType(fn.ReturnType)
			for _, p := range fn.Params {
				resolveType(p.Type)
			}
		}

		// Collect deps from exported classes (methods, fields, parents)
		prevClassCount := len(needClasses)
		prevEnumCount := len(needEnums)

		// Snapshot current classes to iterate
		currentClasses := make([]string, 0, len(needClasses))
		for name := range needClasses {
			currentClasses = append(currentClasses, name)
		}

		for _, name := range currentClasses {
			c := classByQual[name]
			if c == nil {
				continue
			}
			// Parent classes — resolve short names to qualified names
			addParent := func(name string) {
				if _, ok := classByQual[name]; ok {
					needClasses[name] = true
				} else if c, ok := classByShort[name]; ok {
					needClasses[c.QualName] = true
				}
			}
			if c.Parent != "" {
				addParent(c.Parent)
			}
			for _, p := range c.Parents {
				addParent(p)
			}
			// Methods
			for _, m := range c.Methods {
				resolveType(m.ReturnType)
				for _, p := range m.Params {
					resolveType(p.Type)
				}
			}
			// Fields
			for _, f := range c.Fields {
				resolveType(f.Type)
			}
		}

		// For abstract classes in needClasses, include all concrete
		// subclasses so that runtime type dispatch (typeid) can resolve
		// to them.
		for _, name := range currentClasses {
			c := classByQual[name]
			if c == nil || !c.IsAbstract {
				continue
			}
			for i := range spec.Classes {
				sub := &spec.Classes[i]
				if needClasses[sub.QualName] {
					continue
				}
				if hasAncestor(sub, name, classByQual, classByShort) {
					needClasses[sub.QualName] = true
				}
			}
		}

		if len(needClasses) != prevClassCount || len(needEnums) != prevEnumCount {
			changed = true
		}
	}

	// Collect classes explicitly listed in ExportFunctions (not free functions).
	// These are promoted to handle types so they get their own service with
	// constructors, methods, and Free RPC.
	explicitClasses := map[string]bool{}
	for _, name := range cfg.ExportFunctions {
		if _, ok := funcByQual[name]; !ok {
			if _, ok := classByQual[name]; ok {
				explicitClasses[name] = true
			}
		}
	}

	// Build filtered spec
	filtered := &apispec.APISpec{
		Namespace:  spec.Namespace,
		SourceFile: spec.SourceFile,
	}

	for _, fn := range spec.Functions {
		if needFuncs[fn.QualName] {
			filtered.Functions = append(filtered.Functions, fn)
		}
	}
	for i := range spec.Classes {
		c := spec.Classes[i]
		if needClasses[c.QualName] {
			// Promote explicitly-exported classes to handle type so they
			// get a service with constructors/methods in the bridge.
			if explicitClasses[c.QualName] && !c.IsHandle {
				c.IsHandle = true
			}
			filtered.Classes = append(filtered.Classes, c)
		}
	}
	for _, e := range spec.Enums {
		if needEnums[e.QualName] {
			filtered.Enums = append(filtered.Enums, e)
			continue
		}
		// Prefix-match opt-in: keep the enum if its qualified name
		// starts with any entry from cfg.ExportEnumPrefixes — users
		// set this when Go-side consumers depend on enum constants
		// that wouldn't otherwise be reached by the transitive walk
		// from ExportFunctions.
		for _, p := range cfg.ExportEnumPrefixes {
			if p != "" && strings.HasPrefix(e.QualName, p) {
				filtered.Enums = append(filtered.Enums, e)
				break
			}
		}
	}

	return filtered
}

// hasAncestor checks if class c has ancestorQual in its parent chain.
func hasAncestor(c *apispec.Class, ancestorQual string, classByQual map[string]*apispec.Class, classByShort map[string]*apispec.Class) bool {
	visited := map[string]bool{}
	current := c.Parent
	for current != "" && !visited[current] {
		visited[current] = true
		// Resolve short name to qualified
		var resolved string
		if _, ok := classByQual[current]; ok {
			resolved = current
		} else if p, ok := classByShort[current]; ok {
			resolved = p.QualName
		} else {
			break
		}
		if resolved == ancestorQual {
			return true
		}
		if parent, ok := classByQual[resolved]; ok {
			current = parent.Parent
		} else {
			break
		}
	}
	// Also check Parents list
	for _, p := range c.Parents {
		var resolved string
		if _, ok := classByQual[p]; ok {
			resolved = p
		} else if pp, ok := classByShort[p]; ok {
			resolved = pp.QualName
		}
		if resolved == ancestorQual {
			return true
		}
	}
	return false
}

// collectTypeDeps adds class/enum dependencies from a TypeRef.
func collectTypeDeps(ref apispec.TypeRef, classByQual, classByShort map[string]*apispec.Class, classAllByShort map[string][]*apispec.Class, enumByQual, enumByShort map[string]*apispec.Enum, needClasses, needEnums map[string]bool) {
	name := ref.Name
	qt := ref.QualType

	// Strip qualifiers
	clean := strings.TrimSpace(name)
	clean = strings.TrimPrefix(clean, "const ")
	clean = strings.TrimSuffix(clean, "*")
	clean = strings.TrimSuffix(clean, "&")
	clean = strings.TrimSpace(clean)

	addClass := func(n string) {
		if c, ok := classByQual[n]; ok {
			needClasses[c.QualName] = true
		} else if all, ok := classAllByShort[n]; ok && len(all) > 0 {
			// When a short name maps to multiple classes, add all of them
			// to ensure the correct one is available after filtering.
			for _, c := range all {
				needClasses[c.QualName] = true
			}
		}
	}

	addEnum := func(n string) {
		if e, ok := enumByQual[n]; ok {
			needEnums[e.QualName] = true
		} else if e, ok := enumByShort[n]; ok {
			needEnums[e.QualName] = true
		}
	}

	switch ref.Kind {
	case apispec.TypeHandle, apispec.TypeValue:
		addClass(clean)
		// Also check if it's an enum misclassified as value type
		// (protobuf-generated enums are sometimes reported as kind=value)
		addEnum(clean)
		// Try qualified name from QualType
		qtClean := strings.TrimSpace(qt)
		qtClean = strings.TrimPrefix(qtClean, "const ")
		qtClean = strings.TrimSuffix(qtClean, "*")
		qtClean = strings.TrimSuffix(qtClean, "&")
		qtClean = strings.TrimSpace(qtClean)
		addClass(qtClean)
		addEnum(qtClean)
		// Smart pointer inner type
		if strings.Contains(qt, "unique_ptr<") || strings.Contains(qt, "shared_ptr<") {
			inner := extractInnerType(qt)
			if inner != "" {
				addClass(inner)
			}
		}

	case apispec.TypeEnum:
		addEnum(clean)
		qtClean := strings.TrimSpace(qt)
		addEnum(qtClean)

	case apispec.TypeVector:
		if ref.Inner != nil {
			collectTypeDeps(*ref.Inner, classByQual, classByShort, classAllByShort, enumByQual, enumByShort, needClasses, needEnums)
		}
	}
}

// extractInnerType extracts the type from unique_ptr<const T> or shared_ptr<T>.
func extractInnerType(qt string) string {
	start := strings.Index(qt, "<")
	if start < 0 {
		return ""
	}
	end := strings.LastIndex(qt, ">")
	if end <= start {
		return ""
	}
	inner := strings.TrimSpace(qt[start+1 : end])
	inner = strings.TrimPrefix(inner, "const ")
	inner = strings.TrimSuffix(inner, "*")
	inner = strings.TrimSpace(inner)
	return inner
}
