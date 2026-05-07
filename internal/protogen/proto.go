package protogen

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bufbuild/protocompile"
	"github.com/bufbuild/protocompile/reporter"
	"github.com/goccy/wasmify/internal/apispec"
	"github.com/goccy/wasmify/internal/state"
)

// optionsProtoContent is the canonical wasmify/options.proto, embedded
// at build time so SaveOptionsProto and ValidateProto can share one
// source of truth with the on-disk file the rest of the toolchain
// consumes.
//
//go:embed options.proto
var optionsProtoContent string

// messageNameMap holds the disambiguated qualName → proto message name mapping.
// Set by GenerateProto before any name resolution.
var messageNameMap map[string]string

// wrapperMessages collects wrapper message definitions for nested lists and maps.
// name → full message definition. Initialized in GenerateProto.
var wrapperMessages map[string]string

// debugHandleServiceEmit toggles verbose prints in the handle-service
// emission loop. Flip to true from an internal debug binary to trace
// which classes enter writeHandleService; must be false in releases.
var debugHandleServiceEmit = false

// buildMessageNameMap creates a mapping from C++ qualified names to unique
// proto message names. When multiple classes have the same simple name
// (e.g., Foo::_Internal and Bar::_Internal), it prefixes with ancestor
// class names until the name is unique.
// BuildMessageNameMapForDebug exposes buildMessageNameMap for internal
// debugging tools. Not part of the stable API.
func BuildMessageNameMapForDebug(spec *apispec.APISpec) map[string]string {
	return buildMessageNameMap(spec)
}

// SetDebugHandleServiceEmit toggles verbose stderr logging in the
// handle-service emission loop. Only call from internal debug tooling.
func SetDebugHandleServiceEmit(v bool) { debugHandleServiceEmit = v }

func buildMessageNameMap(spec *apispec.APISpec) map[string]string {
	result := make(map[string]string)

	// First pass: compute base names and detect collisions
	baseName := make(map[string]string) // qualName → base proto name
	nameUsers := make(map[string][]string) // base name → list of qualNames

	allQualNames := make([]string, 0)
	for _, c := range spec.Classes {
		allQualNames = append(allQualNames, c.QualName)
	}
	for _, e := range spec.Enums {
		allQualNames = append(allQualNames, e.QualName)
	}

	for _, qn := range allQualNames {
		bn := baseProtoName(qn)
		baseName[qn] = bn
		nameUsers[bn] = append(nameUsers[bn], qn)
	}

	// Second pass: disambiguate collisions by including parent parts
	for _, qn := range allQualNames {
		bn := baseName[qn]
		if len(nameUsers[bn]) <= 1 {
			result[qn] = bn
			continue
		}
		// Use more parts of the qualified name until unique
		result[qn] = disambiguateProtoName(qn, nameUsers[bn])
	}

	return result
}

// baseProtoName extracts the simple proto name from a C++ qualified name.
func baseProtoName(qualName string) string {
	name := stripTemplateWrappers(qualName)
	// Strip cv-qualifiers, pointer/reference markers, and elaborated-
	// type-specifier keywords. Without the elaborated-type strip,
	// clang spellings like `class Foo` reach the parts-split below
	// with the keyword still attached and the resulting "class Foo"
	// fails isValidProtoIdent (the space makes it non-identifier),
	// silently degrading the proto field type to `bytes`.
	name = stripCppTypeQualifiers(name)
	name = strings.TrimSuffix(name, " const")
	parts := strings.Split(name, "::")
	name = parts[len(parts)-1]
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "<", "")
	name = strings.ReplaceAll(name, ">", "")
	if name == "" {
		return "Unknown"
	}
	return name
}

// disambiguateProtoName generates a unique name by prepending parent
// components from the qualified name.
// e.g., "ns::FooProto::_Internal" → "FooProto_Internal"
//
//	"ns::BarProto::_Internal" → "BarProto_Internal"
func disambiguateProtoName(qualName string, conflicting []string) string {
	name := stripTemplateWrappers(qualName)
	name = strings.TrimPrefix(name, "const ")
	name = strings.TrimSuffix(name, " const")
	parts := strings.Split(name, "::")

	// Try progressively more parts from the right until unique among conflicts
	for n := 2; n <= len(parts); n++ {
		candidate := strings.Join(parts[len(parts)-n:], "_")
		candidate = strings.TrimRight(candidate, "* &")
		candidate = strings.TrimSpace(candidate)
		candidate = strings.ReplaceAll(candidate, "<", "")
		candidate = strings.ReplaceAll(candidate, ">", "")

		// Check if this candidate is unique among the conflicting set
		unique := true
		for _, other := range conflicting {
			if other == qualName {
				continue
			}
			otherParts := strings.Split(stripTemplateWrappers(other), "::")
			if n <= len(otherParts) {
				otherCandidate := strings.Join(otherParts[len(otherParts)-n:], "_")
				otherCandidate = strings.TrimRight(otherCandidate, "* &")
				otherCandidate = strings.TrimSpace(otherCandidate)
				otherCandidate = strings.ReplaceAll(otherCandidate, "<", "")
				otherCandidate = strings.ReplaceAll(otherCandidate, ">", "")
				if candidate == otherCandidate {
					unique = false
					break
				}
			}
		}
		if unique {
			return candidate
		}
	}

	// Fallback: use full qualified name with :: → _
	full := strings.ReplaceAll(qualName, "::", "_")
	full = strings.TrimRight(full, "* &")
	full = strings.ReplaceAll(full, "<", "")
	full = strings.ReplaceAll(full, ">", "")
	return full
}

// GenerateProto generates a .proto file from an APISpec.
func GenerateProto(spec *apispec.APISpec, packageName string) string {
	return GenerateProtoWithConfig(spec, packageName, BridgeConfig{})
}

// reclassifyExtraStringTypes walks every TypeRef in the spec and
// switches its Kind to TypeString when the underlying name matches a
// library-specific string alias listed in BridgeConfig.ExtraStringTypes.
// The clang parser is library-agnostic and may have classified these
// as TypeHandle (when used via pointer) or TypeValue; downstream proto
// helpers (output-param detection, repeated-string view encoding, etc.)
// only fire on TypeString. Reclassifying once here keeps the rest of
// the generator free of per-call ExtraStringTypes lookups.
func reclassifyExtraStringTypes(spec *apispec.APISpec, extras []string) {
	if len(extras) == 0 || spec == nil {
		return
	}
	set := make(map[string]bool, len(extras))
	for _, e := range extras {
		set[e] = true
	}
	var visit func(ref *apispec.TypeRef)
	visit = func(ref *apispec.TypeRef) {
		if ref == nil {
			return
		}
		if ref.Kind != apispec.TypeString {
			name := ref.Name
			if set[name] || set[strings.TrimSpace(ref.QualType)] {
				ref.Kind = apispec.TypeString
			}
		}
		if ref.Inner != nil {
			visit(ref.Inner)
		}
	}
	for i := range spec.Functions {
		fn := &spec.Functions[i]
		visit(&fn.ReturnType)
		for j := range fn.Params {
			visit(&fn.Params[j].Type)
		}
	}
	for i := range spec.Classes {
		c := &spec.Classes[i]
		for j := range c.Fields {
			visit(&c.Fields[j].Type)
		}
		for j := range c.Methods {
			m := &c.Methods[j]
			visit(&m.ReturnType)
			for k := range m.Params {
				visit(&m.Params[k].Type)
			}
		}
	}
}

func GenerateProtoWithConfig(spec *apispec.APISpec, packageName string, cfg BridgeConfig) string {
	// Initialize bridge config globals so isBridgeableClass works correctly.
	bridgeConfig = cfg
	// Reclassify TypeRefs whose name is registered as a library-specific
	// string alias (BridgeConfig.ExtraStringTypes, e.g. absl::string_view
	// or absl::Cord). The clang parser is library-agnostic and classifies
	// these as TypeHandle/TypeValue based on syntactic shape; the proto/
	// bridge layer treats them as proto `string` so output-param detection,
	// repeated-string spans, etc. behave correctly.
	reclassifyExtraStringTypes(spec, cfg.ExtraStringTypes)
	skipBridgeHeadersMap = make(map[string]bool)
	for _, h := range cfg.SkipHeaders {
		skipBridgeHeadersMap[h] = true
	}
	skipBridgeClassesMap = make(map[string]bool)
	for _, c := range cfg.SkipClasses {
		skipBridgeClassesMap[c] = true
	}
	specNamespace = spec.Namespace
	if specNamespace == "" {
		specNamespace = packageName
	}
	classQualNames = buildClassQualNameMap(spec)
	classSourceFiles = buildClassSourceFileMap(spec)
	enumQualNames = make(map[string]bool)
	for _, e := range spec.Enums {
		if e.QualName != "" {
			enumQualNames[e.QualName] = true
		}
	}
	classAbstract = buildClassAbstractMap(spec)
	classByQualName = make(map[string]*apispec.Class, len(spec.Classes))
	for i := range spec.Classes {
		classByQualName[spec.Classes[i].QualName] = &spec.Classes[i]
	}
	classNoDefaultCtor = buildClassNoDefaultCtorMap(spec)
	classDeletedCopy = buildClassDeletedCopyMap(spec)
	classNoNew = buildClassNoNewMap(spec)
	// valueClasses lets filterBridgeMethods accept `vector<ValueClass>`
	// params even when ValueClass has no public default constructor
	// (bridge will emplace_back from its fields instead of
	// default-construct-then-assign). Build the map here so the proto
	// path sees the same filter decisions as the bridge path.
	valueClasses = buildValueClassMap(spec)
	var b strings.Builder

	// Initialize wrapper message registry
	wrapperMessages = make(map[string]string)

	b.WriteString("syntax = \"proto3\";\n\n")
	fmt.Fprintf(&b, "package wasmify.%s;\n\n", packageName)
	goPkg := cfg.GoPackage
	if goPkg == "" {
		goPkg = "github.com/goccy/wasmify/gen/" + packageName
	}
	fmt.Fprintf(&b, "option go_package = %q;\n\n", goPkg)

	// Wasmify custom options
	b.WriteString("import \"wasmify/options.proto\";\n\n")

	// Record the real C++ namespace so resolveAbstractHandle in the
	// generated Go can map runtime C++ typeid strings (e.g.
	// "googlesql::ASTQueryStatement") back to the Go factory table.
	// Without this option the plugin derives the namespace from the
	// proto package name, which is arbitrary (often "api" or the
	// proto file name) and does not match the real C++ namespace.
	cppNS := spec.Namespace
	if cppNS == "" {
		// apispec's top-level Namespace is only populated by some
		// producers; others carry the namespace on each class.
		// Pick the most common namespace across project-source
		// classes (skip external libraries pulled in via
		// IncludeExternalHeaders so a few protobuf/abseil headers
		// don't outvote the actual project's namespace).
		counts := make(map[string]int)
		for _, c := range spec.Classes {
			if c.Namespace == "" {
				continue
			}
			if !isProjectSource(c.SourceFile) {
				continue
			}
			counts[c.Namespace]++
		}
		bestN := 0
		for ns, n := range counts {
			if n > bestN {
				bestN = n
				cppNS = ns
			}
		}
		if cppNS == "" {
			// No project classes had a namespace — fall back to
			// any namespace at all.
			for _, c := range spec.Classes {
				if c.Namespace != "" {
					cppNS = c.Namespace
					break
				}
			}
		}
		if cppNS == "" {
			for _, f := range spec.Functions {
				if f.Namespace != "" {
					cppNS = f.Namespace
					break
				}
			}
		}
	}
	if cppNS != "" {
		fmt.Fprintf(&b, "option (wasmify.wasm_cpp_namespace) = %q;\n\n", cppNS)
	}

	// Build a disambiguated qualName → proto message name mapping.
	// When multiple C++ classes map to the same proto name (e.g.,
	// Foo::_Internal and Bar::_Internal both → "_Internal"),
	// prefix with the parent class name to disambiguate.
	messageNameMap = buildMessageNameMap(spec)

	// Populate the nested-enum alias map from the spec so TypeEnum
	// references using the C++ class-nested spelling ("X::Y") map back
	// to their proto-mangled counterpart ("XEnums_Y") during emission.
	// The bridge pass re-populates this map from its own enumQualNames
	// set when it runs; we seed it here because GenerateProto runs
	// before GenerateBridge.
	ResetNestedEnumAliases()
	for _, e := range spec.Enums {
		if e.QualName == "" {
			continue
		}
		marker := "Enums_"
		idx := strings.Index(e.QualName, marker)
		if idx < 0 {
			continue
		}
		prefix := e.QualName[:idx]
		suffix := e.QualName[idx+len(marker):]
		if prefix == "" || suffix == "" {
			continue
		}
		nestedFull := prefix + "::" + suffix
		RegisterNestedEnumAlias(nestedFull, e.QualName)
		if specNS := strings.Index(prefix, "::"); specNS >= 0 {
			// Namespace-less form ("ResolvedX::JoinType" when prefix is
			// "ns::ResolvedX").
			short := prefix[specNS+2:] + "::" + suffix
			RegisterNestedEnumAlias(short, e.QualName)
		}
	}

	// Collect handle types for service generation.
	// Use the same filter as bridge.go (isBridgeableClass) to ensure
	// service IDs match between proto and C++ dispatch table.
	handleClasses := make(map[string]*apispec.Class)
	for i := range spec.Classes {
		c := &spec.Classes[i]
		if !c.IsHandle {
			continue
		}
		if !isBridgeableClass(c) {
			continue
		}
		handleClasses[c.QualName] = c
	}

	// Collect all declared message names (from classes + well-known)
	declaredMessages := make(map[string]bool)
	declaredMessages["Empty"] = true // always generated
	for _, c := range spec.Classes {
		declaredMessages[protoMessageName(c.QualName)] = true
	}
	for _, e := range spec.Enums {
		declaredMessages[protoMessageName(e.QualName)] = true
	}

	// Use the same filter pipeline as bridge.go:
	// disambiguateOverloads first (to resolve name collisions),
	// then filterBridgeFunctions (isSkippedMethod + toProtoRPCName + isBridgeableFunction).
	disambiguatedFunctions := filterBridgeFunctions(disambiguateOverloads(spec.Functions))

	// Collect all referenced handle type names that need to be declared
	referencedHandles := collectReferencedHandleTypes(spec, disambiguatedFunctions)

	// Track emitted message/enum/service names to prevent duplicates
	emitted := make(map[string]bool)

	// Generate enums
	for _, e := range spec.Enums {
		name := protoMessageName(e.QualName)
		if name == "" || !isValidProtoIdent(name) {
			continue
		}
		if emitted[name] {
			continue
		}
		emitted[name] = true
		writeEnum(&b, &e)
		b.WriteString("\n")
	}

	// Generate messages for classes
	for _, c := range spec.Classes {
		// Skip anonymous types (e.g., anonymous unions from clang AST)
		if strings.Contains(c.QualName, "(anonymous") {
			continue
		}
		name := protoMessageName(c.QualName)
		if !isValidProtoIdent(name) {
			continue
		}
		if emitted[name] {
			continue
		}
		emitted[name] = true
		writeClassMessage(&b, &c, spec)
		b.WriteString("\n")
	}

	// Generate placeholder handle messages for referenced but undeclared types
	var placeholderNames []string
	for name := range referencedHandles {
		if !declaredMessages[name] && !emitted[name] {
			placeholderNames = append(placeholderNames, name)
		}
	}
	sort.Strings(placeholderNames)
	for _, name := range placeholderNames {
		if !isValidProtoIdent(name) {
			continue
		}
		emitted[name] = true
		fmt.Fprintf(&b, "message %s {\n", name)
		b.WriteString("  option (wasmify.wasm_handle) = true;\n")
		b.WriteString("  uint64 ptr = 1;\n")
		b.WriteString("}\n\n")
		declaredMessages[name] = true
	}

	// Generate request/response messages for free functions
	for _, fn := range disambiguatedFunctions {
		rpcName, _ := toProtoRPCName(fn.Name)
		if rpcName == "" {
			continue
		}
		// Handle collision with declared messages
		if declaredMessages[rpcName] {
			rpcName = rpcName + "Func"
		}
		writeRequestResponseWithName(&b, &fn, rpcName, emitted)
	}

	// Generate Empty message
	if !emitted["Empty"] {
		emitted["Empty"] = true
		b.WriteString("message Empty {}\n\n")
	}

	// Generate top-level service (free functions)
	serviceID := 0
	if len(disambiguatedFunctions) > 0 {
		serviceName := toUpperCamel(packageName)
		// Avoid collision with message/enum/service names
		if declaredMessages[serviceName] || emitted[serviceName] {
			serviceName = serviceName + "Functions"
			if emitted[serviceName] {
				serviceName = toUpperCamel(packageName) + "API"
			}
		}
		fmt.Fprintf(&b, "service %s {\n", serviceName)
		fmt.Fprintf(&b, "  option (wasmify.wasm_service_id) = %d;\n", serviceID)
		sourceFiles := collectSourceFiles(disambiguatedFunctions)
		if len(sourceFiles) > 0 {
			fmt.Fprintf(&b, "  option (wasmify.wasm_service_source_file) = \"%s\";\n", strings.Join(sourceFiles, ","))
		}
		methodID := 0
		for _, fn := range disambiguatedFunctions {
			rpcName, originalName := toProtoRPCName(fn.Name)
			if rpcName == "" {
				continue
			}
			if declaredMessages[rpcName] {
				if originalName == "" {
					originalName = fn.Name
				}
				rpcName = rpcName + "Func"
			}
			fmt.Fprintf(&b, "  rpc %s(%sRequest) returns (%sResponse) {\n",
				rpcName, rpcName, rpcName)
			fmt.Fprintf(&b, "    option (wasmify.wasm_method_id) = %d;\n", methodID)
			if originalName != "" {
				fmt.Fprintf(&b, "    option (wasmify.wasm_original_name) = \"%s\";\n", originalName)
			}
			if fn.Comment != "" {
				fmt.Fprintf(&b, "    option (wasmify.wasm_method_comment) = %s;\n", protoStringLiteral(fn.Comment))
			}
			b.WriteString("  }\n")
			methodID++
		}
		b.WriteString("}\n\n")
		serviceID++
	}

	// Generate per-handle services
	var classNames []string
	for name := range handleClasses {
		classNames = append(classNames, name)
	}
	sort.Strings(classNames)

	var callbackCandidates []*apispec.Class
	for _, qualName := range classNames {
		c := handleClasses[qualName]
		if debugHandleServiceEmit {
			fmt.Fprintf(os.Stderr, "DEBUG writeHandleService qual=%s name=%s msgName=%s methods=%d\n",
				c.QualName, c.Name, protoMessageName(c.QualName), len(c.Methods))
		}
		writeHandleService(&b, c, handleClasses, emitted, declaredMessages, serviceID)
		serviceID++
		if isCallbackCandidate(c) {
			callbackCandidates = append(callbackCandidates, c)
		}
	}

	// Callback services. These describe the C++→Go virtual-dispatch
	// protocol: each RPC is a pure-virtual method whose body the plugin
	// will forward to a user-supplied Go implementation via the
	// imported `wasmify_callback_invoke`. Callback services are NOT
	// host→wasm exports (they are the opposite direction), so they
	// receive service IDs past the regular handle-service range but
	// have no corresponding `w_<svc>_<mid>` wasm export.
	for _, c := range callbackCandidates {
		writeCallbackService(&b, c, emitted, serviceID)
		serviceID++
	}

	// Emit wrapper messages (lists, maps) — after all services so all wrappers are collected
	var wrapperNames []string
	for name := range wrapperMessages {
		wrapperNames = append(wrapperNames, name)
	}
	sort.Strings(wrapperNames)
	for _, name := range wrapperNames {
		if !emitted[name] {
			emitted[name] = true
			b.WriteString(wrapperMessages[name])
			b.WriteString("\n")
		}
	}

	return b.String()
}

// overloadSortKey produces a deterministic sort key for an overload. The key
// orders by (param count ASC, then each param's qualified type spelling, then
// the return type spelling), so the overload with fewest parameters always
// receives the base name, and regenerations of the same source yield the
// same numeric suffixes regardless of clang's traversal order.
func overloadSortKey(fn apispec.Function) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%04d|", len(fn.Params))
	for _, p := range fn.Params {
		b.WriteString(p.Type.QualType)
		if p.Type.QualType == "" {
			b.WriteString(p.Type.Name)
		}
		b.WriteByte(',')
	}
	b.WriteByte('|')
	if fn.ReturnType.QualType != "" {
		b.WriteString(fn.ReturnType.QualType)
	} else {
		b.WriteString(fn.ReturnType.Name)
	}
	return b.String()
}

// disambiguateOverloads renames overloaded functions by appending a numeric
// suffix (AnalyzeType, AnalyzeType → AnalyzeType, AnalyzeType2). Overloads of
// the same name are ranked by overloadSortKey so the fewest-args variant keeps
// the bare name. The unmodified C++ name is preserved in OriginalName.
func disambiguateOverloads(functions []apispec.Function) []apispec.Function {
	result := make([]apispec.Function, len(functions))
	copy(result, functions)

	nameToIndices := make(map[string][]int)
	for i := range result {
		nameToIndices[result[i].Name] = append(nameToIndices[result[i].Name], i)
	}

	for _, indices := range nameToIndices {
		if len(indices) <= 1 {
			continue
		}
		sort.SliceStable(indices, func(a, b int) bool {
			return overloadSortKey(result[indices[a]]) < overloadSortKey(result[indices[b]])
		})
		for rank, idx := range indices {
			if rank == 0 {
				continue
			}
			if result[idx].OriginalName == "" {
				result[idx].OriginalName = result[idx].Name
			}
			result[idx].Name = fmt.Sprintf("%s%d", result[idx].Name, rank+1)
		}
	}
	return result
}

// collectReferencedTypes finds all handle and value type names referenced in function params/returns
// that need to be declared as messages.
func collectReferencedHandleTypes(spec *apispec.APISpec, functions []apispec.Function) map[string]bool {
	// Build a set of proto-level names that are actually declared as
	// enums. The clang parser occasionally misclassifies enum-typed
	// function parameters as `kind=value` (e.g. setters on protobuf
	// wrapper classes like `ResolvedJoinScanProto::set_join_type`
	// whose signature is ::ns::Enum_Name). Those ended up as
	// placeholder `message X { uint64 ptr = 1; }` declarations that
	// shadowed the real enum emission. The set below lets us skip
	// them so writeEnum's output survives.
	enumProtoNames := make(map[string]bool)
	for _, e := range spec.Enums {
		if n := protoMessageName(e.QualName); n != "" {
			enumProtoNames[n] = true
		}
	}
	refs := make(map[string]bool)
	add := func(raw string) {
		name := protoMessageName(raw)
		if name == "Empty" || name == "Unknown" {
			return
		}
		if enumProtoNames[name] {
			return
		}
		refs[name] = true
	}
	collectTypeRef := func(t apispec.TypeRef) {
		if t.Kind == apispec.TypeHandle || t.Kind == apispec.TypeValue {
			add(t.Name)
		}
		if t.Inner != nil && (t.Inner.Kind == apispec.TypeHandle || t.Inner.Kind == apispec.TypeValue) {
			add(t.Inner.Name)
		}
	}

	for _, fn := range functions {
		collectTypeRef(fn.ReturnType)
		for _, p := range fn.Params {
			collectTypeRef(p.Type)
		}
	}
	for _, c := range spec.Classes {
		for _, f := range c.Fields {
			collectTypeRef(f.Type)
		}
		for _, m := range c.Methods {
			collectTypeRef(m.ReturnType)
			for _, p := range m.Params {
				collectTypeRef(p.Type)
			}
		}
	}
	return refs
}

// writeEnum writes a Protobuf enum definition.
func writeEnum(b *strings.Builder, e *apispec.Enum) {
	name := protoMessageName(e.QualName)
	fmt.Fprintf(b, "enum %s {\n", name)
	prefix := toScreamingSnake(name)
	// Check if we need allow_alias (duplicate values)
	valuesSeen := make(map[int64]bool)
	valuesSeen[0] = true // UNSPECIFIED = 0
	needAlias := false
	for _, v := range e.Values {
		enumVal := v.Value + 1
		if enumVal > 2147483647 || enumVal < -2147483648 {
			continue
		}
		if valuesSeen[enumVal] {
			needAlias = true
			break
		}
		valuesSeen[enumVal] = true
	}
	if needAlias {
		b.WriteString("  option allow_alias = true;\n")
	}
	if e.Comment != "" {
		fmt.Fprintf(b, "  option (wasmify.wasm_enum_comment) = %s;\n", protoStringLiteral(e.Comment))
	}

	fmt.Fprintf(b, "  %s_UNSPECIFIED = 0;\n", prefix)
	// Track identifiers we've already emitted in this enum so we never write
	// two entries with the same name — protoc's C++-style enum-value scoping
	// rejects that. The auto-injected `<PREFIX>_UNSPECIFIED` is seeded here
	// and a C++ enum value that canonicalizes to the same identifier (e.g.
	// an original value literally named `UNSPECIFIED`) is skipped below.
	emittedIdents := map[string]bool{prefix + "_UNSPECIFIED": true}
	for _, v := range e.Values {
		enumVal := v.Value + 1
		// Proto3 enum values must fit in int32 range [-2147483648, 2147483647]
		if enumVal > 2147483647 || enumVal < -2147483648 {
			continue
		}
		// If the value name is already SCREAMING_SNAKE_CASE (all uppercase
		// + underscores), use as-is. Otherwise convert from CamelCase.
		valueName := v.Name
		if !isScreamingSnake(valueName) {
			valueName = toScreamingSnake(valueName)
		}
		if !isValidProtoIdent(valueName) {
			continue
		}
		// If the raw value name already embeds the enum type prefix —
		// common for protobuf-generated enum values that carry the
		// enclosing enum name as part of their identifier (so they're
		// unique across the proto file) — don't prepend `<PREFIX>_`
		// again. Otherwise we'd produce
		// RESOLVED_JOIN_SCAN_ENUMS_JOIN_TYPE_RESOLVED_JOIN_SCAN_ENUMS_JOIN_TYPE_INNER
		// when the source value is already
		// RESOLVED_JOIN_SCAN_ENUMS_JOIN_TYPE_INNER.
		var ident string
		if strings.HasPrefix(valueName, prefix+"_") {
			ident = valueName
		} else {
			ident = prefix + "_" + valueName
		}
		if emittedIdents[ident] {
			// Duplicate (typically: C++ value literally named "UNSPECIFIED"
			// colliding with our proto3 zero sentinel). Skip it; the auto-
			// injected entry stands in as the canonical representation.
			continue
		}
		emittedIdents[ident] = true
		if v.Comment != "" {
			fmt.Fprintf(b, "  %s = %d [(wasmify.wasm_enum_value_comment) = %s];\n", ident, enumVal, protoStringLiteral(v.Comment))
		} else {
			fmt.Fprintf(b, "  %s = %d;\n", ident, enumVal)
		}
	}
	b.WriteString("}\n")
}

// writeClassMessage writes a Protobuf message for a class.
// resolveNearestParent finds the nearest ancestor of c that exists in spec.
// If c.Parent is not in the spec (filtered out), walks up the original
// class hierarchy to find one that is present.
func resolveNearestParent(c *apispec.Class, spec *apispec.APISpec) string {
	classMap := make(map[string]*apispec.Class)
	for i := range spec.Classes {
		classMap[spec.Classes[i].QualName] = &spec.Classes[i]
	}

	// First try the immediate parent from the original api-spec.
	if c.Parent != "" {
		if _, ok := classMap[c.Parent]; ok {
			return c.Parent
		}
	}
	// Fallback: walk through c.Parents list (ordered from immediate to root).
	// c.Parents is the full chain from the unfiltered api-spec, so we may
	// find an ancestor that is present after filtering.
	for _, p := range c.Parents {
		if _, ok := classMap[p]; ok {
			return p
		}
	}
	return c.Parent // fallback to original
}

func writeClassMessage(b *strings.Builder, c *apispec.Class, spec *apispec.APISpec) {
	name := protoMessageName(c.QualName)

	fmt.Fprintf(b, "message %s {\n", name)
	if c.IsHandle {
		b.WriteString("  option (wasmify.wasm_handle) = true;\n")
		if c.IsAbstract {
			b.WriteString("  option (wasmify.wasm_abstract) = true;\n")
		}
		if c.Parent != "" {
			// Resolve to the nearest parent that exists in the current spec.
			// Intermediate classes may have been filtered out by export filter.
			resolvedParent := resolveNearestParent(c, spec)
			if resolvedParent != "" {
				parentName := protoMessageName(resolvedParent)
				fmt.Fprintf(b, "  option (wasmify.wasm_parent) = \"%s\";\n", parentName)
			}
		}
		// Emit every secondary parent (multiple-inheritance bases beyond
		// the primary c.Parent). The Go plugin embeds each of these in
		// addition to the primary parent so method promotion reaches
		// every inherited method.
		if len(c.Parents) > 1 {
			classMap := map[string]*apispec.Class{}
			for i := range spec.Classes {
				classMap[spec.Classes[i].QualName] = &spec.Classes[i]
			}
			primary := c.Parent
			for _, p := range c.Parents {
				if p == primary || p == "" {
					continue
				}
				// Only emit parents that survive the export filter.
				if _, ok := classMap[p]; !ok {
					continue
				}
				fmt.Fprintf(b, "  option (wasmify.wasm_parents) = \"%s\";\n", protoMessageName(p))
			}
		}
		if c.SourceFile != "" {
			fmt.Fprintf(b, "  option (wasmify.wasm_source_file) = \"%s\";\n", c.SourceFile)
		}
		if c.Comment != "" {
			fmt.Fprintf(b, "  option (wasmify.wasm_message_comment) = %s;\n", protoStringLiteral(c.Comment))
		}
		b.WriteString("  uint64 ptr = 1;\n")
	} else {
		if c.Comment != "" {
			fmt.Fprintf(b, "  option (wasmify.wasm_message_comment) = %s;\n", protoStringLiteral(c.Comment))
		}
		// Value type: map fields
		fieldNum := 1
		for _, f := range c.Fields {
			protoType := typeRefToProto(f.Type)
			fieldName := sanitizeFieldName(toSnakeCase(f.Name), fieldNum)
			if f.Comment != "" {
				fmt.Fprintf(b, "  %s %s = %d [(wasmify.wasm_field_comment) = %s];\n", protoType, fieldName, fieldNum, protoStringLiteral(f.Comment))
			} else {
				fmt.Fprintf(b, "  %s %s = %d;\n", protoType, fieldName, fieldNum)
			}
			fieldNum++
		}
	}
	b.WriteString("}\n")
}

// writeRequestField emits one proto-field line on the request side of an
// RPC, optionally annotated with [(wasmify.wasm_take_ownership) = true]
// when the C++ counterpart will absorb the wrapped pointer (unique_ptr
// passed by value or rvalue-ref). The Go-side wrapper reads that option
// to clear the argument's ptr after the invoke so the per-instance Go
// finalizer does not double-free memory the C++ side has already
// deleted.
func writeRequestField(b *strings.Builder, protoType, fieldName string, fieldNum int, takeOwnership bool) {
	if takeOwnership {
		fmt.Fprintf(b, "  %s %s = %d [(wasmify.wasm_take_ownership) = true];\n", protoType, fieldName, fieldNum)
		return
	}
	fmt.Fprintf(b, "  %s %s = %d;\n", protoType, fieldName, fieldNum)
}

// writeRequestFieldOwnershipWhen emits a proto-field line annotated
// with [(wasmify.wasm_take_ownership_when) = "<selector>"]. The
// plugin reads this extension and emits a runtime guard
// `if <selector> { handle.clearPtr() }` after the invoke. Used for
// the C++ idiom where a method takes both a raw `T*` and a `bool`
// selector parameter (e.g. `AddColumn(const Column*, bool
// is_owned)`); the proto schema cannot mark the handle field as
// unconditionally ownership-transferring because passing the
// selector as false is a legitimate borrowed-pass-through.
func writeRequestFieldOwnershipWhen(b *strings.Builder, protoType, fieldName string, fieldNum int, selector string) {
	fmt.Fprintf(b, "  %s %s = %d [(wasmify.wasm_take_ownership_when) = %q];\n", protoType, fieldName, fieldNum, selector)
}

// paramTakesOwnership reports whether the C++ counterpart of p takes
// ownership of the wrapped pointer. Today that means a `unique_ptr<T>`
// parameter passed by value or rvalue-ref — the bridge constructs a
// fresh `std::unique_ptr<T>` from the raw handle pointer (see
// handleArgExpr in bridge.go), and the destructor of that unique_ptr
// runs whether or not the callee threw, so ownership transfers
// unconditionally at invoke time. `shared_ptr<T>` is reference-counted
// and is NOT marked: the Go side keeps its copy of the heap-allocated
// shared_ptr alive across the call, so neither side's release alone
// frees the underlying object.
func paramTakesOwnership(p apispec.Param) bool {
	qt := strings.TrimSpace(p.Type.QualType)
	if smartPointerInner(qt) != "" && !isSharedPointerType(qt) {
		return true
	}
	// `std::vector<std::unique_ptr<T>>` parameters: the bridge body
	// wraps each wire-decoded raw pointer in a fresh unique_ptr at
	// emplace_back time (see readVectorElementExpr in bridge.go), so
	// the C++ side takes ownership of every element on call. The Go
	// wrapper for each element must therefore drop its `ptr` after
	// the invoke, which the plugin emits when the proto field carries
	// `wasm_take_ownership`. Without this branch the annotation never
	// reaches the wire schema, the plugin emits no clearPtr loop, and
	// each element double-frees once its Go finaliser runs against
	// memory the C++ destructor has already reclaimed.
	if p.Type.Kind == apispec.TypeVector && p.Type.Inner != nil {
		innerQt := strings.TrimSpace(p.Type.Inner.QualType)
		if isUniquePointerType(innerQt) {
			return true
		}
	}
	return false
}

// methodParamTakesOwnership extends paramTakesOwnership with the
// project's config-driven escape hatch for C++ APIs whose
// implementation captures a raw `T*` parameter into a smart
// pointer (typically via `absl::WrapUnique` or
// `std::unique_ptr<T>(p)` inside the .cc body) — ownership-
// transfer semantics that the C++ type system cannot express and
// that the generator does NOT attempt to detect by name.
//
// The user lists such methods in
// `bridge.OwnershipTransferMethods`. Each entry is matched by
// fully-qualified method name; an optional Signature
// (parameter qual_types) picks a specific overload. When the
// matched method is invoked, handle parameters that are NOT
// already ownership-transferring through the C++ type system
// are treated as if they carried `wasm_take_ownership` —
// PROVIDED the matched overload does not also include a `bool`
// parameter (which marks the runtime-conditional shape; see
// conditionalOwnershipSelector).
func methodParamTakesOwnership(m apispec.Function, p apispec.Param) bool {
	if paramTakesOwnership(p) {
		return true
	}
	if p.Type.Kind != apispec.TypeHandle {
		return false
	}
	entry := matchOwnershipTransferEntry(m)
	if entry == nil {
		return false
	}
	if hasBoolParam(m) {
		// Runtime-conditional ownership transfer; emitted via
		// wasm_take_ownership_when by conditionalOwnershipSelector.
		return false
	}
	return true
}

// conditionalOwnershipSelector returns the snake_case proto
// field name of the bool parameter that gates the runtime
// ownership transfer for the matched overload, or "" when no
// matched overload requires runtime gating.
//
// The bool is identified by TYPE: the matched overload (per
// OwnershipTransferMethods) must include exactly one bool
// parameter, which the generator treats as the selector. Listing
// a method with multiple bool parameters is an error -- the user
// must provide a Signature that pins down which overload they
// mean and that overload must have a single bool.
func conditionalOwnershipSelector(m apispec.Function) string {
	entry := matchOwnershipTransferEntry(m)
	if entry == nil {
		return ""
	}
	if !hasBoolParam(m) {
		return ""
	}
	for _, p := range m.Params {
		if isBoolPrimitive(p.Type) {
			return toSnakeCase(p.Name)
		}
	}
	return ""
}

// matchOwnershipTransferEntry walks
// `bridge.OwnershipTransferMethods` and returns the entry whose
// Method matches m.QualName AND whose Signature (when present)
// matches m's parameter qual_types in order. Returns nil when no
// entry matches.
func matchOwnershipTransferEntry(m apispec.Function) *state.OwnershipTransferEntry {
	if m.QualName == "" {
		return nil
	}
	for i := range bridgeConfig.OwnershipTransferMethods {
		e := &bridgeConfig.OwnershipTransferMethods[i]
		if e.Method != m.QualName {
			continue
		}
		if len(e.Signature) == 0 {
			return e
		}
		if signatureMatches(e.Signature, m.Params) {
			return e
		}
	}
	return nil
}

// signatureMatches reports whether the configured signature
// matches the actual parameter qual_types in order. Whitespace
// is collapsed on both sides so user-written and clang-emitted
// spellings line up.
func signatureMatches(sig []string, params []apispec.Param) bool {
	if len(sig) != len(params) {
		return false
	}
	for i, want := range sig {
		got := params[i].Type.QualType
		if normaliseQualType(want) != normaliseQualType(got) {
			return false
		}
	}
	return true
}

// normaliseQualType collapses internal whitespace runs so
// "const Foo *" and "const Foo*" compare equal.
func normaliseQualType(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// hasBoolParam reports whether m has at least one parameter of
// primitive bool type. Used as the type-level signal for the
// runtime-conditional ownership transfer pattern.
func hasBoolParam(m apispec.Function) bool {
	for _, p := range m.Params {
		if isBoolPrimitive(p.Type) {
			return true
		}
	}
	return false
}

// isBoolPrimitive reports whether t is the primitive bool type
// (not a reference / pointer / smart-pointer of bool).
func isBoolPrimitive(t apispec.TypeRef) bool {
	if t.Kind != apispec.TypePrimitive {
		return false
	}
	if t.IsPointer || t.IsRef {
		return false
	}
	qt := strings.TrimSpace(t.QualType)
	return qt == "bool" || t.Name == "bool"
}


// protoStringLiteral renders s as a proto3 string literal: wraps in
// double quotes and escapes embedded backslashes, double quotes, and
// newlines. Comments lifted from C++ headers can span multiple lines,
// which the proto parser refuses inside a normal string literal.
func protoStringLiteral(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// sanitizeFieldName ensures a field name is valid for proto3.
// If empty or invalid, returns a fallback like "field_N".
func sanitizeFieldName(name string, fieldNum int) string {
	if name == "" {
		return fmt.Sprintf("field_%d", fieldNum)
	}
	// Remove leading underscores that might cause issues, but keep trailing ones
	// Proto field names must match [a-zA-Z_][a-zA-Z0-9_]*
	var result []byte
	for i, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || (i > 0 && c >= '0' && c <= '9') {
			result = append(result, byte(c))
		}
	}
	if len(result) == 0 {
		return fmt.Sprintf("field_%d", fieldNum)
	}
	return string(result)
}

// isOutputParam detects if a parameter is an output parameter.
// In C/C++, output parameters are typically:
// - Non-const pointers to handle/value types (T*)
// - Pointers to smart pointers (unique_ptr<T>*)
// - Non-const pointers to primitive types used for "out" values (bool*)
func isOutputParam(p apispec.Param) bool {
	t := p.Type
	// Reference-to-pointer (`T*&`): handle output via reference. The
	// clang AST parser reports IsRef=true, IsPointer=false because
	// the outermost type is the reference; the qualified type still
	// contains the trailing `*&`. Treat exactly like `T**` for
	// callback / dispatch purposes — the bridge declares a local
	// `T*` and binds it to the reference, and the callee writes
	// through.
	if t.IsRef && t.Kind == apispec.TypeHandle {
		qt := strings.TrimSpace(t.QualType)
		if strings.Contains(qt, "*&") || strings.Contains(qt, "* &") {
			return true
		}
	}
	// Must be a pointer
	if !t.IsPointer {
		return false
	}
	// T** pattern: outer pointer is always non-const (otherwise the
	// callee could not write to it). Recognize as output regardless of
	// IsConst (which refers to the inner pointee's constness in clang AST).
	qt := strings.TrimSpace(t.QualType)
	qtNoConst := strings.TrimSuffix(qt, "const")
	qtNoConst = strings.TrimSpace(qtNoConst)
	if strings.HasSuffix(qtNoConst, "**") {
		return true
	}
	// Const pointers are input parameters (const T*)
	if t.IsConst {
		return false
	}
	// Smart pointer output: unique_ptr<T>* is an output param (caller
	// receives ownership). Plain handle pointer T* is an INPUT param
	// (caller passes existing object pointer — like ASTNode*).
	// T** (pointer-to-pointer) is an output param — the callee writes
	// the pointer value into the caller-provided storage.
	if t.Kind == apispec.TypeHandle {
		qt := strings.TrimSpace(t.QualType)
		if qt == "" {
			qt = t.Name
		}
		// Only smart pointers are output params for handle types
		if strings.Contains(qt, "unique_ptr") || strings.Contains(qt, "shared_ptr") {
			return true
		}
		// T** pattern (pointer-to-pointer): count asterisks at the end
		// of the qualified type, ignoring const qualifiers.
		stripped := strings.TrimSuffix(qt, "const")
		stripped = strings.TrimSpace(stripped)
		return strings.HasSuffix(stripped, "**")
	}
	// Pointer to primitive (e.g., bool* at_end_of_input)
	if t.Kind == apispec.TypePrimitive {
		return true
	}
	// Pointer to value type
	if t.Kind == apispec.TypeValue {
		return true
	}
	// Pointer to string (e.g., std::string* out)
	if t.Kind == apispec.TypeString {
		return true
	}
	// Pointer to vector (e.g., std::vector<T>* out)
	if t.Kind == apispec.TypeVector {
		return true
	}
	// Pointer to enum (e.g., EnumType* out)
	if t.Kind == apispec.TypeEnum {
		return true
	}
	return false
}

// writeRequestResponse writes request/response messages for a free function.
// Output parameters (non-const pointer params) are placed in the Response message
// instead of the Request message.
func writeRequestResponse(b *strings.Builder, fn *apispec.Function, emitted map[string]bool) {
	rpcName := toUpperCamel(fn.Name)
	writeRequestResponseWithName(b, fn, rpcName, emitted)
}

// writeRequestResponseWithName writes request/response messages using a specified RPC name.
func writeRequestResponseWithName(b *strings.Builder, fn *apispec.Function, rpcName string, emitted map[string]bool) {

	// Separate input and output parameters
	var inputParams []apispec.Param
	var outputParams []apispec.Param
	for _, p := range fn.Params {
		if isOutputParam(p) {
			outputParams = append(outputParams, p)
		} else {
			inputParams = append(inputParams, p)
		}
	}

	// Request (input params only)
	reqName := rpcName + "Request"
	if !emitted[reqName] {
		emitted[reqName] = true
		fmt.Fprintf(b, "message %s {\n", reqName)
		for i, p := range inputParams {
			protoType := typeRefToProto(p.Type)
			fieldName := toSnakeCase(p.Name)
			if fieldName == "" {
				fieldName = fmt.Sprintf("arg%d", i)
			}
			writeRequestField(b, protoType, fieldName, i+1, paramTakesOwnership(p))
		}
		b.WriteString("}\n\n")
	}

	// Response (return value + output params)
	respName := rpcName + "Response"
	if !emitted[respName] {
		emitted[respName] = true
		fmt.Fprintf(b, "message %s {\n", respName)
		fieldNum := 1
		if fn.ReturnType.Kind != apispec.TypeVoid {
			protoType := typeRefToProto(fn.ReturnType)
			fmt.Fprintf(b, "  %s result = %d;\n", protoType, fieldNum)
			fieldNum++
		}
		usedFields := map[string]bool{"error": true, "result": true}
		for _, p := range outputParams {
			protoType := typeRefToProto(p.Type)
			fieldName := toSnakeCase(p.Name)
			if fieldName == "" {
				fieldName = fmt.Sprintf("out_%d", fieldNum)
			}
			// Avoid collision with reserved fields (error, result)
			if usedFields[fieldName] {
				fieldName = "out_" + fieldName
			}
			usedFields[fieldName] = true
			fmt.Fprintf(b, "  %s %s = %d;\n", protoType, fieldName, fieldNum)
			fieldNum++
		}
		b.WriteString("  string error = 15;\n")
		b.WriteString("}\n\n")
	}
}

// rpcEntry holds the resolved RPC name and optional original C++ name for a method.
type rpcEntry struct {
	rpcName       string
	originalName  string
	method        apispec.Function
	isConstructor bool // true if the method is a constructor
}

// writeHandleService writes a Protobuf service for a handle type (class).
func writeHandleService(b *strings.Builder, c *apispec.Class, allHandles map[string]*apispec.Class, emitted map[string]bool, declaredMessages map[string]bool, serviceID int) {
	serviceName := protoMessageName(c.QualName) + "Service"
	msgName := protoMessageName(c.QualName)

	// Filter and disambiguate methods using the same logic as bridge.go
	// to ensure proto services and C++ dispatch have identical method sets.
	methods := disambiguateMethodNames(filterBridgeMethods(c.Methods))

	// Build the list of RPC entries (resolving names once)
	var rpcEntries []rpcEntry
	var ctorEntries []rpcEntry
	usedRPCNames := make(map[string]bool)

	// serviceMessageRefs is the set of UNQUALIFIED message names that
	// the generator must avoid reusing as RPC names on this service.
	// Two distinct collision shapes both surface as a build error:
	//
	//   - Proto3 lookup: a service-scoped RPC name shadows top-level
	//     message names referenced by other RPCs of the same service
	//     (notably `Empty`, returned by every Free RPC, and msgName,
	//     returned by every constructor/static_factory). protoc
	//     reports "is a method, not a message".
	//
	//   - Go embedding: the generated handle struct embeds its parent
	//     struct(s) by pointer; Go forbids declaring a method whose
	//     name matches an embedded field's name on the same struct
	//     ("field and method with the same name X"). So an RPC named
	//     after the handle's parent class would compile-fail in Go
	//     even though proto3 accepts it.
	//
	// Per-RPC request/response messages carry the `<msgName>` prefix
	// and never collide with bare RPC names, so they are not in this
	// set.
	serviceMessageRefs := map[string]bool{
		"Empty": true,
		msgName: true,
	}
	if c.Parent != "" {
		serviceMessageRefs[protoMessageName(c.Parent)] = true
	}
	for _, p := range c.Parents {
		if p != "" {
			serviceMessageRefs[protoMessageName(p)] = true
		}
	}

	// First pass: collect constructor entries with "New"/"NewN" naming.
	// Keep ctorCount so the second ctor gets "New2", etc.
	// Abstract classes cannot be instantiated — skip all constructors.
	ctorCount := 0
	for _, m := range methods {
		if !m.IsConstructor {
			continue
		}
		if c.IsAbstract {
			continue
		}
		if m.Access != "" && m.Access != "public" {
			continue
		}
		ctorCount++
		rpcName := "New"
		if ctorCount > 1 {
			rpcName = fmt.Sprintf("New%d", ctorCount)
		}
		if usedRPCNames[rpcName] {
			rpcName = rpcName + "Ctor"
		}
		usedRPCNames[rpcName] = true
		// Original name is the class's simple name (possibly disambiguated)
		ctorEntries = append(ctorEntries, rpcEntry{
			rpcName:       rpcName,
			originalName:  m.Name,
			method:        m,
			isConstructor: true,
		})
	}

	// Second pass: collect regular method entries
	for _, m := range methods {
		if m.IsConstructor {
			continue
		}
		if m.IsStatic {
			// Allow static factory methods (same logic as bridge.go)
			if !isStaticFactory(m) {
				continue
			}
		}
		if isSkippedMethod(m.Name) {
			continue
		}
		rpcName, originalName := toProtoRPCName(m.Name)
		if rpcName == "" {
			continue
		}
		// Two collision shapes need a rename:
		//   1. Another RPC on this service already took rpcName.
		//   2. rpcName matches a message that the service references
		//      as a request/response type (notably `Empty`, used as
		//      every Free RPC's return). Proto3 looks up unqualified
		//      type names inside the enclosing service first, finds
		//      the same-named RPC, and rejects the schema with
		//      "is a method, not a message". A bare top-level
		//      message (one not referenced by any RPC of this
		//      service) does NOT trigger this — those names are
		//      free.
		// In either case the bridge dispatch table walks the method
		// list unconditionally and assigns a case per method, so
		// dropping methods would misalign proto method_id → bridge
		// case ID; append "Method" (with a numeric suffix on chained
		// collisions) to keep the mapping 1:1.
		collides := usedRPCNames[rpcName] || serviceMessageRefs[rpcName]
		if collides {
			if originalName == "" {
				originalName = m.Name
			}
			base := rpcName + "Method"
			rpcName = base
			for i := 2; usedRPCNames[rpcName]; i++ {
				rpcName = fmt.Sprintf("%s%d", base, i)
			}
		}
		usedRPCNames[rpcName] = true
		rpcEntries = append(rpcEntries, rpcEntry{rpcName: rpcName, originalName: originalName, method: m})
	}

	fmt.Fprintf(b, "service %s {\n", serviceName)
	fmt.Fprintf(b, "  option (wasmify.wasm_service_id) = %d;\n", serviceID)
	if c.SourceFile != "" {
		fmt.Fprintf(b, "  option (wasmify.wasm_service_source_file) = \"%s\";\n", c.SourceFile)
	}

	methodID := 0

	// Constructors first (for discoverability: users typically create a
	// handle before calling anything on it).
	for _, entry := range ctorEntries {
		reqMsg := fmt.Sprintf("%s%sRequest", msgName, entry.rpcName)
		fmt.Fprintf(b, "  rpc %s(%s) returns (%s) {\n", entry.rpcName, reqMsg, msgName)
		fmt.Fprintf(b, "    option (wasmify.wasm_method_id) = %d;\n", methodID)
		b.WriteString("    option (wasmify.wasm_method_type) = \"constructor\";\n")
		if entry.originalName != "" && entry.originalName != c.Name {
			fmt.Fprintf(b, "    option (wasmify.wasm_original_name) = \"%s\";\n", entry.originalName)
		}
		if entry.method.Comment != "" {
			fmt.Fprintf(b, "    option (wasmify.wasm_method_comment) = %s;\n", protoStringLiteral(entry.method.Comment))
		}
		b.WriteString("  }\n")
		methodID++
	}

	// Static factory methods (like constructors but call ClassName::Method())
	var staticEntries []rpcEntry
	var regularEntries []rpcEntry
	for _, entry := range rpcEntries {
		if entry.method.IsStatic && isStaticFactory(entry.method) {
			staticEntries = append(staticEntries, entry)
		} else {
			regularEntries = append(regularEntries, entry)
		}
	}

	for _, entry := range staticEntries {
		reqMsg := fmt.Sprintf("%s%sRequest", msgName, entry.rpcName)
		fmt.Fprintf(b, "  rpc %s(%s) returns (%s) {\n", entry.rpcName, reqMsg, msgName)
		fmt.Fprintf(b, "    option (wasmify.wasm_method_id) = %d;\n", methodID)
		b.WriteString("    option (wasmify.wasm_method_type) = \"static_factory\";\n")
		if entry.originalName != "" {
			fmt.Fprintf(b, "    option (wasmify.wasm_original_name) = \"%s\";\n", entry.originalName)
		}
		if entry.method.Comment != "" {
			fmt.Fprintf(b, "    option (wasmify.wasm_method_comment) = %s;\n", protoStringLiteral(entry.method.Comment))
		}
		b.WriteString("  }\n")
		methodID++
	}

	// Methods
	for _, entry := range regularEntries {
		reqMsg := fmt.Sprintf("%s%sRequest", msgName, entry.rpcName)
		respMsg := fmt.Sprintf("%s%sResponse", msgName, entry.rpcName)
		fmt.Fprintf(b, "  rpc %s(%s) returns (%s) {\n", entry.rpcName, reqMsg, respMsg)
		fmt.Fprintf(b, "    option (wasmify.wasm_method_id) = %d;\n", methodID)
		if entry.originalName != "" {
			fmt.Fprintf(b, "    option (wasmify.wasm_original_name) = \"%s\";\n", entry.originalName)
		}
		if entry.method.Comment != "" {
			fmt.Fprintf(b, "    option (wasmify.wasm_method_comment) = %s;\n", protoStringLiteral(entry.method.Comment))
		}
		b.WriteString("  }\n")
		methodID++
	}

	// Field accessors (getters) — resolve collisions
	type getterEntry struct {
		rpcName string
		field   apispec.Field
	}
	var resolvedGetterNames []getterEntry
	for _, f := range c.Fields {
		getterName := "Get" + toUpperCamel(f.Name)
		originalFieldName := ""
		if usedRPCNames[getterName] {
			// Try alternatives
			alt := "Getter" + toUpperCamel(f.Name)
			if !usedRPCNames[alt] {
				originalFieldName = f.Name
				getterName = alt
			} else {
				alt = "Field" + toUpperCamel(f.Name)
				if !usedRPCNames[alt] {
					originalFieldName = f.Name
					getterName = alt
				} else {
					alt = getterName + "Value"
					originalFieldName = f.Name
					getterName = alt
				}
			}
		}
		usedRPCNames[getterName] = true
		resolvedGetterNames = append(resolvedGetterNames, getterEntry{rpcName: getterName, field: f})
		respMsg := fmt.Sprintf("%s%sResponse", msgName, getterName)
		fmt.Fprintf(b, "  rpc %s(%s) returns (%s) {\n", getterName, msgName, respMsg)
		fmt.Fprintf(b, "    option (wasmify.wasm_method_id) = %d;\n", methodID)
		b.WriteString("    option (wasmify.wasm_method_type) = \"getter\";\n")
		if originalFieldName != "" {
			fmt.Fprintf(b, "    option (wasmify.wasm_original_name) = \"%s\";\n", originalFieldName)
		}
		if f.Comment != "" {
			fmt.Fprintf(b, "    option (wasmify.wasm_method_comment) = %s;\n", protoStringLiteral(f.Comment))
		}
		b.WriteString("  }\n")
		methodID++
	}

	// Downcast API is intentionally NOT emitted. Go's type assertion
	// (`v.(*ConcreteT)`) handles abstract -> concrete conversion
	// natively at zero cost; a `ToXxx()` RPC would only add a bridge
	// round-trip for what the Go runtime already tracks. See the
	// "do not emit Downcast APIs" rule in CLAUDE.md.

	// Callback factory. One RPC per base ctor variant — for an
	// abstract class with no explicit ctor (typical: implicit
	// default ctor only) the variant list is empty and a single
	// `FromCallback` RPC carrying just `callback_id` is emitted.
	// For a concrete class listed in `bridge.CallbackClasses`, one
	// `FromCallback`, `FromCallback2`, ... is emitted, each
	// mirroring one base ctor's parameter list. The trampoline
	// (see bridge.go) constructs an instance by forwarding those
	// args through to the base ctor, then attaches the callback id
	// for downstream virtual dispatch.
	emitFromCallback := isCallbackCandidate(c)
	var fromCallbackVariants []apispec.Function
	if emitFromCallback {
		fromCallbackVariants = collectTrampolineCtors(c)
		variantCount := len(fromCallbackVariants)
		if variantCount == 0 {
			variantCount = 1
		}
		for i := 0; i < variantCount; i++ {
			rpcName := "FromCallback"
			if i > 0 {
				rpcName = fmt.Sprintf("FromCallback%d", i+1)
			}
			if usedRPCNames[rpcName] {
				rpcName = "New" + rpcName
			}
			usedRPCNames[rpcName] = true
			reqMsg := fmt.Sprintf("%s%sRequest", msgName, rpcName)
			fmt.Fprintf(b, "  rpc %s(%s) returns (%s) {\n", rpcName, reqMsg, msgName)
			fmt.Fprintf(b, "    option (wasmify.wasm_method_id) = %d;\n", methodID)
			b.WriteString("    option (wasmify.wasm_method_type) = \"callback_factory\";\n")
			b.WriteString("  }\n")
			methodID++
		}
	}

	// Free — only emitted when the class has a public destructor.
	// Factory-owned types (e.g. googlesql::ArrayType whose lifetime
	// is managed by TypeFactory) have protected/private destructors
	// and cannot be `delete`d from outside. For those we skip the
	// Free RPC entirely; Go callers see no Close/Free method, and
	// the handle simply stays borrowed for the process lifetime (or
	// until the owning factory releases it).
	if c.HasPublicDtor {
		freeName := "Free"
		freeOriginal := ""
		if usedRPCNames[freeName] {
			freeOriginal = "Free"
			freeName = "Release"
		}
		fmt.Fprintf(b, "  rpc %s(%s) returns (Empty) {\n", freeName, msgName)
		fmt.Fprintf(b, "    option (wasmify.wasm_method_id) = %d;\n", methodID)
		b.WriteString("    option (wasmify.wasm_method_type) = \"free\";\n")
		if freeOriginal != "" {
			fmt.Fprintf(b, "    option (wasmify.wasm_original_name) = \"%s\";\n", freeOriginal)
		}
		b.WriteString("  }\n")
	}

	b.WriteString("}\n\n")

	if emitFromCallback {
		variantCount := len(fromCallbackVariants)
		if variantCount == 0 {
			// No explicit base ctors → one default-shaped variant
			// carrying just the callback_id.
			reqMsg := fmt.Sprintf("%sFromCallbackRequest", msgName)
			if !emitted[reqMsg] {
				emitted[reqMsg] = true
				fmt.Fprintf(b, "message %s {\n", reqMsg)
				b.WriteString("  int32 callback_id = 1;\n")
				b.WriteString("}\n\n")
			}
		} else {
			// One Request message per ctor variant. callback_id
			// always lands at field 1; the ctor's args follow
			// at field 2..N. The bridge body reads them at the
			// matching field numbers (see writeHandleDispatch's
			// FromCallback emit loop).
			for i, ctor := range fromCallbackVariants {
				rpcName := "FromCallback"
				if i > 0 {
					rpcName = fmt.Sprintf("FromCallback%d", i+1)
				}
				reqMsg := fmt.Sprintf("%s%sRequest", msgName, rpcName)
				if emitted[reqMsg] {
					continue
				}
				emitted[reqMsg] = true
				fmt.Fprintf(b, "message %s {\n", reqMsg)
				b.WriteString("  int32 callback_id = 1;\n")
				for j, p := range ctor.Params {
					protoType := typeRefToProto(p.Type)
					fieldName := toSnakeCase(p.Name)
					if fieldName == "" {
						fieldName = fmt.Sprintf("arg%d", j)
					}
					fmt.Fprintf(b, "  %s %s = %d;\n", protoType, fieldName, j+2)
				}
				b.WriteString("}\n\n")
			}
		}
	}

	// Generate constructor request messages. Constructor requests have no
	// handle field; parameters start at field 1. The response is the handle
	// message itself, which is already declared elsewhere.
	//
	// Each `unique_ptr<T>` (by value) ctor parameter must carry
	// `wasm_take_ownership`: the C++ side wraps the wire-decoded raw
	// pointer in a fresh smart pointer at the call site, and that
	// smart pointer's destructor will eventually delete the
	// underlying object. Without the annotation the plugin does not
	// emit clearPtr on the Go-side wrapper, the wrapper's finalizer
	// later issues a Free RPC against the same address, and the
	// wasm allocator's freelist double-frees — surfacing as a
	// delayed `out of bounds memory access` from a completely
	// unrelated call path several iterations later. The free-
	// function and method paths already route through
	// writeRequestField; the ctor path used to bypass it.
	for _, entry := range ctorEntries {
		rpcName := entry.rpcName
		m := entry.method
		reqMsg := fmt.Sprintf("%s%sRequest", msgName, rpcName)
		if !emitted[reqMsg] {
			emitted[reqMsg] = true
			fmt.Fprintf(b, "message %s {\n", reqMsg)
			for i, p := range m.Params {
				protoType := typeRefToProto(p.Type)
				fieldName := toSnakeCase(p.Name)
				if fieldName == "" {
					fieldName = fmt.Sprintf("arg%d", i)
				}
				writeRequestField(b, protoType, fieldName, i+1, paramTakesOwnership(p))
			}
			b.WriteString("}\n\n")
		}
	}

	// Generate method request/response messages
	// Combine static + regular entries for message generation
	allMethodEntries := append(staticEntries, regularEntries...)
	for _, entry := range allMethodEntries {
		rpcName := entry.rpcName
		m := entry.method
		isStatic := m.IsStatic && isStaticFactory(m)
		reqMsg := fmt.Sprintf("%s%sRequest", msgName, rpcName)
		respMsg := fmt.Sprintf("%s%sResponse", msgName, rpcName)

		// Split C++ params into input vs output. Output-pointer params
		// (e.g. `ArrayType ** out_result`) are not sent from Go — the
		// bridge writes their values into the response. Treating them
		// as request fields would force callers to pass a Go-allocated
		// wrapper whose rawPtr() chain panics on zero-init, and the
		// bridge ignores the value anyway. Mirror the free-function
		// path, which already splits these correctly.
		var reqParams []apispec.Param
		var respParams []apispec.Param
		for _, p := range m.Params {
			if isOutputParam(p) {
				respParams = append(respParams, p)
			} else {
				reqParams = append(reqParams, p)
			}
		}

		// Request: for regular methods, first field is the handle, then parameters.
		// For static factory methods, no handle field — parameters start at field 1.
		if !emitted[reqMsg] {
			emitted[reqMsg] = true
			fmt.Fprintf(b, "message %s {\n", reqMsg)
			fieldOffset := 1
			if !isStatic {
				fmt.Fprintf(b, "  %s handle = 1;\n", msgName)
				fieldOffset = 2
			}
			for i, p := range reqParams {
				protoType := typeRefToProto(p.Type)
				fieldName := toSnakeCase(p.Name)
				if fieldName == "" {
					fieldName = fmt.Sprintf("arg%d", i)
				}
				selector := conditionalOwnershipSelector(m)
				if selector != "" && p.Type.Kind == apispec.TypeHandle && !methodParamTakesOwnership(m, p) {
					writeRequestFieldOwnershipWhen(b, protoType, fieldName, i+fieldOffset, selector)
				} else {
					writeRequestField(b, protoType, fieldName, i+fieldOffset, methodParamTakesOwnership(m, p))
				}
			}
			b.WriteString("}\n\n")
		}

		// Response: return value plus any output-pointer params.
		if !emitted[respMsg] {
			emitted[respMsg] = true
			fmt.Fprintf(b, "message %s {\n", respMsg)
			fieldNum := 1
			if m.ReturnType.Kind != apispec.TypeVoid {
				protoType := typeRefToProto(m.ReturnType)
				fmt.Fprintf(b, "  %s result = %d;\n", protoType, fieldNum)
				fieldNum++
			}
			usedFields := map[string]bool{"error": true, "result": true}
			for _, p := range respParams {
				protoType := typeRefToProto(p.Type)
				fieldName := toSnakeCase(p.Name)
				if fieldName == "" {
					fieldName = fmt.Sprintf("out_%d", fieldNum)
				}
				if usedFields[fieldName] {
					fieldName = "out_" + fieldName
				}
				usedFields[fieldName] = true
				fmt.Fprintf(b, "  %s %s = %d;\n", protoType, fieldName, fieldNum)
				fieldNum++
			}
			b.WriteString("  string error = 15;\n")
			b.WriteString("}\n\n")
		}
	}

	// Getter response messages (using resolvedGetterNames populated during service generation)
	for _, gn := range resolvedGetterNames {
		respMsg := fmt.Sprintf("%s%sResponse", msgName, gn.rpcName)
		if !emitted[respMsg] {
			emitted[respMsg] = true
			fmt.Fprintf(b, "message %s {\n", respMsg)
			protoType := typeRefToProto(gn.field.Type)
			fmt.Fprintf(b, "  %s value = 1;\n", protoType)
			b.WriteString("}\n\n")
		}
	}
}

// typeRefToProto converts a TypeRef to a Protobuf type string.
// isValidProtoIdent checks if a name is a valid proto identifier
// (only letters, digits, underscores, and dots for fully-qualified names).
func isValidProtoIdent(name string) bool {
	for _, c := range name {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '.' {
			return false
		}
	}
	return name != ""
}

// nestedEnumAliasMap maps C++-visible nested enum names like
// "ResolvedJoinScan::JoinType" or "ns::ResolvedJoinScan::JoinType" to the
// proto-mangled enum that actually exists in the proto schema
// ("ns::ResolvedJoinScanEnums_JoinType"). Populated when bridge builds its
// enumQualNames index so both layers agree.
var nestedEnumAliasMap map[string]string

// RegisterNestedEnumAlias records an alias. Called by bridge.go during
// enumQualNames construction when a proto-style enum name ("X_Enums_Y")
// matches the convention used for class-nested alias enums.
func RegisterNestedEnumAlias(cppNested, protoMangled string) {
	if nestedEnumAliasMap == nil {
		nestedEnumAliasMap = map[string]string{}
	}
	nestedEnumAliasMap[cppNested] = protoMangled
}

// ResetNestedEnumAliases clears the alias map between generator runs.
func ResetNestedEnumAliases() { nestedEnumAliasMap = nil }

func mapNestedEnumToProtoEnum(name string) string {
	if m := nestedEnumAliasMap[name]; m != "" {
		return m
	}
	return ""
}

func typeRefToProto(ref apispec.TypeRef) string {
	switch ref.Kind {
	case apispec.TypePrimitive:
		return primitiveToPbType(ref.Name)
	case apispec.TypeString:
		return "string"
	case apispec.TypeVoid:
		return "Empty"
	case apispec.TypeEnum:
		// Only use the name if it's been qualified (postProcessEnumTypes
		// qualifies unambiguous short names). If it's still unqualified,
		// the enum is either ambiguous or we can't find it - fall back to bytes.
		n := ref.Name
		if !strings.Contains(n, "::") {
			// Ambiguous or unresolved short name - can't safely reference
			return "bytes"
		}
		// A C++ class sometimes defines a nested enum ("ASTClass::Enum")
		// whose values alias a proto-generated flat enum
		// ("ASTClassEnums_Enum"). The bridge keeps the C++-visible nested
		// spelling in ref.Name because setters take the nested type, but
		// the proto file only declares the flat enum. Translate here so
		// RPC response messages reference the real proto enum.
		if mapped := mapNestedEnumToProtoEnum(n); mapped != "" {
			n = mapped
		}
		name := protoMessageName(n)
		if !isValidProtoIdent(name) {
			return "bytes"
		}
		return name
	case apispec.TypeVector:
		if ref.Inner != nil {
			if ref.Inner.Kind == apispec.TypeVector {
				// Nested vector: create wrapper message
				innerElemType := "bytes"
				if ref.Inner.Inner != nil {
					innerElemType = typeRefToProto(*ref.Inner.Inner)
				}
				// Extract clean type name for the wrapper
				cleanType := strings.TrimPrefix(innerElemType, "repeated ")
				wrapperName := cleanType + "List"
				if wrapperMessages != nil {
					if _, exists := wrapperMessages[wrapperName]; !exists {
						var wb strings.Builder
						fmt.Fprintf(&wb, "message %s {\n", wrapperName)
						wb.WriteString("  option (wasmify.wasm_list_type) = true;\n")
						fmt.Fprintf(&wb, "  repeated %s items = 1;\n", innerElemType)
						wb.WriteString("}\n")
						wrapperMessages[wrapperName] = wb.String()
					}
				}
				return "repeated " + wrapperName
			}
			inner := typeRefToProto(*ref.Inner)
			if !isValidProtoIdent(inner) {
				return "bytes"
			}
			return "repeated " + inner
		}
		return "bytes"
	case apispec.TypeHandle:
		// Set-like containers (configured via SetLikeTypePrefixes) are
		// reported as kind=handle by clang because the set is itself a
		// class template instance — but the wire format is a `repeated
		// <Elem>` matching what the bridge body builds element-by-
		// element via setLikeContainerInfo + readSetElementExpr. Catch
		// the shape here and emit the matching proto schema; without
		// this check the field falls through to `bytes` and the wire
		// encoding disagrees with the bridge body's per-element reads.
		if keyType, valType, ok := parseMapType(ref.Name); ok {
			return handleMapType(ref.Name, keyType, valType)
		}
		name := protoMessageName(ref.Name)
		if !isValidProtoIdent(name) {
			return "bytes"
		}
		return name
	case apispec.TypeValue:
		// Status-or-value wrappers (e.g. `absl::StatusOr<T>`)
		// surface here because the parser classifies them as
		// value types. The proto schema represents them as the
		// inner T's message — the Status half is carried by the
		// standard `string error = 15` field on every callback
		// response, so on the wire there's nothing to add.
		if inner, ok := statusOrInnerType(ref); ok {
			resolved := resolveTypeName(strings.TrimSpace(inner))
			if resolved == "" {
				resolved = inner
			}
			if messageNameMap != nil {
				if name, exists := messageNameMap[resolved]; exists {
					if isValidProtoIdent(name) {
						return name
					}
				}
			}
			if name := protoMessageName(resolved); isValidProtoIdent(name) {
				return name
			}
			return "bytes"
		}
		// Library-specific string aliases (ExtraStringTypes, e.g.
		// absl::string_view, absl::Cord) that the parser doesn't
		// recognise as TypeString land here. Map them to proto
		// `string` so the round-trip matches the caller's intent.
		for _, extra := range bridgeConfig.ExtraStringTypes {
			if ref.Name == extra || strings.TrimSpace(ref.QualType) == extra {
				return "string"
			}
		}
		// ValueViewTypes (e.g. absl::Span<T>) are non-owning views
		// over a contiguous sequence of T; on the wire it's
		// indistinguishable from repeated T. Emit `repeated <Elem>`
		// so the plugin generates a slice-based Go signature and the
		// bridge can deserialise element-by-element.
		//
		// The inner type can be:
		//   1. A primitive or string type — `repeated <proto-scalar>`.
		//   2. A project-defined class that the proto layer declares
		//      (i.e. has an entry in messageNameMap) — `repeated
		//      <message>`. Unbridgeable classes (no entry in
		//      messageNameMap), nested classes the proto generator
		//      skipped (`Class::Nested`), etc., are intentionally
		//      not handled here and fall through to the regular
		//      message-or-bytes path below.
		if matchesValueViewType(ref.QualType) {
			if elem := extractTemplateArgFromQualType(ref.QualType); elem != "" {
				raw := strings.TrimSpace(elem)
				raw = strings.TrimSuffix(raw, "*")
				raw = strings.TrimSpace(raw)
				raw = strings.TrimPrefix(raw, "const ")
				raw = strings.TrimSpace(raw)
				// Primitive / string element: route through the
				// generic C++→proto scalar mapping. This covers
				// `absl::Span<const std::string>` as `repeated
				// string`, which is the canonical multi-segment
				// path / name idiom in any C++ codebase.
				if scalar := cppTypeNameToProto(raw); scalar != "" && scalar != "bytes" && isKnownProtoType(scalar) {
					return "repeated " + scalar
				}
				resolved := resolveTypeName(raw)
				if messageNameMap != nil {
					if _, ok := messageNameMap[resolved]; ok {
						name := protoMessageName(resolved)
						if isValidProtoIdent(name) {
							return "repeated " + name
						}
					}
				}
			}
		}
		// Check if it's a map type before treating as message
		if keyType, valType, ok := parseMapType(ref.Name); ok {
			return handleMapType(ref.Name, keyType, valType)
		}
		name := protoMessageName(ref.Name)
		if !isValidProtoIdent(name) {
			return "bytes"
		}
		return name
	case apispec.TypeUnknown:
		// Check for map types in unknown types
		if keyType, valType, ok := parseMapType(ref.Name); ok {
			return handleMapType(ref.Name, keyType, valType)
		}
		return "bytes"
	default:
		return "bytes"
	}
}

// primitiveToPbType maps C primitive type names to Protobuf types.
func primitiveToPbType(name string) string {
	switch name {
	case "bool":
		return "bool"
	case "int", "int32_t":
		return "int32"
	case "unsigned int", "uint32_t":
		return "uint32"
	case "long", "long long", "int64_t", "ssize_t", "ptrdiff_t", "intptr_t":
		return "int64"
	case "unsigned long", "unsigned long long", "uint64_t", "size_t", "uintptr_t":
		return "uint64"
	case "short", "int16_t":
		return "int32" // no int16 in proto3
	case "unsigned short", "uint16_t":
		return "uint32"
	case "char", "int8_t", "signed char":
		return "int32"
	case "unsigned char", "uint8_t":
		return "uint32"
	case "float":
		return "float"
	case "double", "long double":
		return "double"
	default:
		return "int64"
	}
}

// disambiguateMethodNames renames overloaded methods by appending a numeric
// suffix. Same semantics as disambiguateOverloads — overloads ranked by
// overloadSortKey so the fewest-args variant keeps the bare name. Static
// factory methods participate too: otherwise a class like
// ParseResumeLocation that has two static FromString overloads emits only
// the first in the proto (the second loses its rpc-name collision) while
// the bridge keeps both cases, leaving method IDs desynced between Go and
// C++. Destructors and other skipped names are excluded.
func disambiguateMethodNames(methods []apispec.Function) []apispec.Function {
	result := make([]apispec.Function, len(methods))
	copy(result, methods)

	nameToIndices := make(map[string][]int)
	for i := range result {
		if isSkippedMethod(result[i].Name) {
			continue
		}
		nameToIndices[result[i].Name] = append(nameToIndices[result[i].Name], i)
	}

	for _, indices := range nameToIndices {
		if len(indices) <= 1 {
			continue
		}
		sort.SliceStable(indices, func(a, b int) bool {
			return overloadSortKey(result[indices[a]]) < overloadSortKey(result[indices[b]])
		})
		for rank, idx := range indices {
			if rank == 0 {
				continue
			}
			if result[idx].OriginalName == "" {
				result[idx].OriginalName = result[idx].Name
			}
			result[idx].Name = fmt.Sprintf("%s%d", result[idx].Name, rank+1)
		}
	}
	return result
}

// isSkippedMethod returns true for C++ methods that cannot be represented as
// proto RPCs: destructors only. Operators are now renamed instead of skipped.
func isSkippedMethod(name string) bool {
	if strings.HasPrefix(name, "~") {
		return true
	}
	if strings.HasPrefix(name, "operator") {
		return true
	}
	return false
}

// operatorToProtoName converts a C++ operator name to a valid proto identifier.
func operatorToProtoName(name string) string {
	// Conversion operators: "operator bool", "operator int", etc.
	if strings.HasPrefix(name, "operator ") {
		typeName := strings.TrimPrefix(name, "operator ")
		typeName = strings.TrimSpace(typeName)
		return "OperatorConvertTo" + toUpperCamel(typeName)
	}

	mapping := map[string]string{
		"operator==": "OperatorEqual",
		"operator!=": "OperatorNotEqual",
		"operator<":  "OperatorLess",
		"operator<=": "OperatorLessEqual",
		"operator>":  "OperatorGreater",
		"operator>=": "OperatorGreaterEqual",
		"operator+":  "OperatorAdd",
		"operator-":  "OperatorSubtract",
		"operator*":  "OperatorMultiply",
		"operator/":  "OperatorDivide",
		"operator%":  "OperatorModulo",
		"operator[]": "OperatorIndex",
		"operator()": "OperatorCall",
		"operator<<": "OperatorShiftLeft",
		"operator>>": "OperatorShiftRight",
		"operator&":  "OperatorBitwiseAnd",
		"operator|":  "OperatorBitwiseOr",
		"operator^":  "OperatorBitwiseXor",
		"operator~":  "OperatorBitwiseNot",
		"operator!":  "OperatorNot",
		"operator&&": "OperatorLogicalAnd",
		"operator||": "OperatorLogicalOr",
		"operator=":  "OperatorAssign",
		"operator+=": "OperatorAddAssign",
		"operator-=": "OperatorSubtractAssign",
		"operator->": "OperatorArrow",
	}

	if result, ok := mapping[name]; ok {
		return result
	}

	// Fallback: sanitize to valid proto ident
	result := strings.TrimPrefix(name, "operator")
	var sanitized []byte
	for _, c := range result {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
			sanitized = append(sanitized, byte(c))
		}
	}
	if len(sanitized) == 0 {
		return "OperatorUnknown"
	}
	return "Operator" + string(sanitized)
}

// toProtoRPCName converts a C++ method name to a valid proto RPC name.
// Returns (rpcName, originalName). If no renaming was needed, originalName is "".
// Returns ("", "") for methods that should be skipped (destructors).
func toProtoRPCName(name string) (string, string) {
	if strings.HasPrefix(name, "~") {
		return "", ""
	}
	if strings.HasPrefix(name, "operator") {
		// Handle disambiguated operator names like "operator==2", "operator<<3"
		// Strip trailing digits to find the base operator, then re-append
		baseName := name
		suffix := ""
		for i := len(name) - 1; i >= 0; i-- {
			if name[i] >= '0' && name[i] <= '9' {
				continue
			}
			if i < len(name)-1 {
				baseName = name[:i+1]
				suffix = name[i+1:]
			}
			break
		}
		protoName := operatorToProtoName(baseName)
		return protoName + suffix, name
	}
	rpcName := toUpperCamel(name)
	if !isValidProtoIdent(rpcName) {
		// Sanitize
		var sanitized []byte
		for _, c := range rpcName {
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
				sanitized = append(sanitized, byte(c))
			}
		}
		if len(sanitized) == 0 {
			return "", ""
		}
		return string(sanitized), name
	}
	return rpcName, ""
}

// parseMapType detects C++ map/set types from a TypeRef.Name.
// For maps: returns (keyType, valueType, true)
// For sets: returns (elemType, "", true)
func parseMapType(name string) (string, string, bool) {
	// Strip const, pointers, references for matching
	cleaned := strings.TrimPrefix(name, "const ")
	cleaned = strings.TrimSuffix(cleaned, " const")
	cleaned = strings.TrimRight(cleaned, "* &")
	cleaned = strings.TrimSpace(cleaned)

	// Map prefixes (key, value). std entries are unconditional; any
	// library-specific variants (absl::flat_hash_map, google::protobuf::Map,
	// …) come from BridgeConfig.MapLikeTypePrefixes.
	mapPrefixes := []string{
		"std::unordered_map<",
		"unordered_map<",
		"std::map<",
		"map<",
	}
	for _, p := range bridgeConfig.MapLikeTypePrefixes {
		mapPrefixes = append(mapPrefixes, p+"<")
	}

	// Set prefixes (single element). std entries unconditional;
	// library-specific variants come from BridgeConfig.SetLikeTypePrefixes.
	setPrefixes := []string{
		"std::unordered_set<",
		"unordered_set<",
		"std::set<",
		"set<",
	}
	for _, p := range bridgeConfig.SetLikeTypePrefixes {
		setPrefixes = append(setPrefixes, p+"<")
	}

	for _, prefix := range mapPrefixes {
		if strings.HasPrefix(cleaned, prefix) {
			inner := cleaned[len(prefix):]
			inner = strings.TrimSuffix(inner, ">")
			inner = strings.TrimSpace(inner)
			key, val, ok := splitTemplateArgs(inner)
			if !ok {
				return "", "", false
			}
			return strings.TrimSpace(key), strings.TrimSpace(val), true
		}
	}

	for _, prefix := range setPrefixes {
		if strings.HasPrefix(cleaned, prefix) {
			inner := cleaned[len(prefix):]
			inner = strings.TrimSuffix(inner, ">")
			inner = strings.TrimSpace(inner)
			return inner, "", true
		}
	}

	return "", "", false
}

// splitTemplateArgs splits template arguments at the top-level comma,
// respecting nested <> brackets.
func splitTemplateArgs(s string) (string, string, bool) {
	depth := 0
	for i, c := range s {
		switch c {
		case '<':
			depth++
		case '>':
			depth--
		case ',':
			if depth == 0 {
				return s[:i], strings.TrimSpace(s[i+1:]), true
			}
		}
	}
	return "", "", false
}

// cppTypeNameToProto converts a raw C++ type name to a proto type.
func cppTypeNameToProto(name string) string {
	// Strip cv-qualifiers, pointer/reference markers, and elaborated-
	// type-specifier keywords (`class`, `struct`, `enum`, `union`).
	// Sharing stripCppTypeQualifiers with the bridge side keeps the
	// proto schema and the C++ bridge agreed on the bare type name.
	cleaned := stripCppTypeQualifiers(name)
	// Some clang spellings put `const` after the type ("Foo const")
	// rather than before; strip that suffix too because
	// stripCppTypeQualifiers only handles the prefix form.
	cleaned = strings.TrimSuffix(cleaned, " const")
	cleaned = strings.TrimSpace(cleaned)

	// Check primitives
	if pt := tryPrimitiveToPbType(cleaned); pt != "" {
		return pt
	}

	// Check string types. std:: entries are unconditional;
	// library-specific ones (absl::string_view, absl::Cord, …)
	// are registered through BridgeConfig.ExtraStringTypes.
	switch cleaned {
	case "std::string", "string", "std::string_view", "const char":
		return "string"
	}
	for _, extra := range bridgeConfig.ExtraStringTypes {
		if cleaned == extra {
			return "string"
		}
	}

	// Otherwise treat as a message name
	msgName := protoMessageName(cleaned)
	if isValidProtoIdent(msgName) {
		return msgName
	}
	return "bytes"
}

// tryPrimitiveToPbType maps C primitive type names to Protobuf types.
// Returns "" if the type is not a known primitive.
func tryPrimitiveToPbType(name string) string {
	switch name {
	case "bool":
		return "bool"
	case "int", "int32_t":
		return "int32"
	case "unsigned int", "uint32_t":
		return "uint32"
	case "long", "long long", "int64_t", "ssize_t", "ptrdiff_t", "intptr_t":
		return "int64"
	case "unsigned long", "unsigned long long", "uint64_t", "size_t", "uintptr_t":
		return "uint64"
	case "short", "int16_t":
		return "int32"
	case "unsigned short", "uint16_t":
		return "uint32"
	case "char", "int8_t", "signed char":
		return "int32"
	case "unsigned char", "uint8_t":
		return "uint32"
	case "float":
		return "float"
	case "double", "long double":
		return "double"
	default:
		return ""
	}
}

// isProtoMapKeyType checks if a proto type is valid as a proto3 map key.
func isProtoMapKeyType(protoType string) bool {
	switch protoType {
	case "int32", "int64", "uint32", "uint64", "sint32", "sint64",
		"fixed32", "fixed64", "sfixed32", "sfixed64", "bool", "string":
		return true
	}
	return false
}

// isKnownProtoType checks if a proto type name is either a built-in proto type
// or a message/enum that exists in the messageNameMap.
func isKnownProtoType(protoType string) bool {
	// Built-in proto scalar types
	switch protoType {
	case "double", "float", "int32", "int64", "uint32", "uint64",
		"sint32", "sint64", "fixed32", "fixed64", "sfixed32", "sfixed64",
		"bool", "string", "bytes":
		return true
	}
	// Check if it's a known message/enum from the spec
	for _, name := range messageNameMap {
		if name == protoType {
			return true
		}
	}
	return false
}

// handleMapType creates a proto type expression for a C++ map/set type.
func handleMapType(fullName, keyType, valType string) string {
	keyProto := cppTypeNameToProto(keyType)

	// Set types (only one template arg) → repeated
	if valType == "" {
		if isValidProtoIdent(keyProto) && isKnownProtoType(keyProto) {
			return "repeated " + keyProto
		}
		return "bytes"
	}

	valProto := cppTypeNameToProto(valType)

	// Verify both types are known
	if !isKnownProtoType(keyProto) || !isKnownProtoType(valProto) {
		return "bytes"
	}

	// If key is a valid map key type, use native proto3 map
	if isProtoMapKeyType(keyProto) && isValidProtoIdent(valProto) {
		return fmt.Sprintf("map<%s, %s>", keyProto, valProto)
	}

	// Otherwise create a map entry wrapper message
	if !isValidProtoIdent(keyProto) || !isValidProtoIdent(valProto) {
		return "bytes"
	}

	entryName := keyProto + "To" + valProto + "Entry"
	if wrapperMessages != nil {
		if _, exists := wrapperMessages[entryName]; !exists {
			var wb strings.Builder
			fmt.Fprintf(&wb, "message %s {\n", entryName)
			wb.WriteString("  option (wasmify.wasm_map_type) = true;\n")
			fmt.Fprintf(&wb, "  %s key = 1;\n", keyProto)
			fmt.Fprintf(&wb, "  %s value = 2;\n", valProto)
			wb.WriteString("}\n")
			wrapperMessages[entryName] = wb.String()
		}
	}
	return "repeated " + entryName
}

// protoMessageName converts a C++ qualified name to a valid Protobuf message name.
// e.g., "ns::ResolvedAST" -> "ResolvedAST"
//
//	"ns::functions::DateDiff" -> "DateDiff"
//	"std::unique_ptr<const AnalyzerOutput>" -> "AnalyzerOutput"
func protoMessageName(qualName string) string {
	// Use disambiguated name if available.
	if messageNameMap != nil {
		if name, ok := messageNameMap[qualName]; ok {
			return name
		}
	}
	// The caller may have handed us a short name or a template-wrapped
	// spelling ("std::unique_ptr<const Column>") — either way the stripped
	// bare name is what matters for lookup. Resolve it to a qualified
	// name via the classQualNames/aliases map populated by the bridge
	// pass and retry the disambiguation map. Without this step a
	// parameter type like `std::unique_ptr<const Column>` on
	// SimpleTable::AddColumn resolves to the proto name "Column" —
	// which is already taken by the unrelated googlesql::reflection::Column
	// — so the generated Go binding ends up typed with the wrong
	// struct and SimpleColumn cannot satisfy it.
	bare := baseProtoName(qualName)
	if messageNameMap != nil {
		if resolved := resolveTypeName(bare); resolved != "" && resolved != bare {
			if name, ok := messageNameMap[resolved]; ok {
				return name
			}
		}
	}
	return bare
}

// stripTemplateWrappers unwraps known template types to extract the inner type.
// e.g., "std::unique_ptr<const AnalyzerOutput>" -> "AnalyzerOutput"
//
//	"std::vector<std::string>" is NOT unwrapped (handled separately as vector)
func stripTemplateWrappers(name string) string {
	// Known wrapper templates to strip
	wrappers := []string{
		"std::unique_ptr<",
		"unique_ptr<",
		"std::shared_ptr<",
		"shared_ptr<",
	}
	for _, prefix := range wrappers {
		if idx := strings.Index(name, prefix); idx >= 0 {
			inner := name[idx+len(prefix):]
			// Remove trailing >
			if last := strings.LastIndex(inner, ">"); last >= 0 {
				inner = inner[:last]
			}
			inner = strings.TrimSpace(inner)
			// Remove const
			inner = strings.TrimPrefix(inner, "const ")
			inner = strings.TrimSuffix(inner, " const")
			return strings.TrimSpace(inner)
		}
	}
	return name
}

// toUpperCamel converts snake_case or lowerCamelCase to UpperCamelCase.
func toUpperCamel(s string) string {
	if s == "" {
		return s
	}
	// Handle snake_case
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

// toSnakeCase converts camelCase to snake_case.
func toSnakeCase(s string) string {
	// Standard camelCase / PascalCase → snake_case with proper handling
	// of all-caps sequences so "INNER" stays "inner" (not "i_n_n_e_r")
	// and "HTTPServer" becomes "http_server" (not "h_t_t_p_server").
	//
	// Rules:
	//   - Insert `_` before an uppercase letter that is preceded by a
	//     lowercase letter or digit (camel boundary).
	//   - Insert `_` before an uppercase letter that is followed by a
	//     lowercase letter AND preceded by another uppercase letter
	//     (acronym-end boundary, e.g. "HTTPSe" → "http_se").
	runes := []rune(s)
	var result strings.Builder
	for i, r := range runes {
		isUpper := r >= 'A' && r <= 'Z'
		if isUpper && i > 0 {
			prev := runes[i-1]
			prevLower := prev >= 'a' && prev <= 'z'
			prevDigit := prev >= '0' && prev <= '9'
			prevUpper := prev >= 'A' && prev <= 'Z'
			nextLower := false
			if i+1 < len(runes) {
				nl := runes[i+1]
				nextLower = nl >= 'a' && nl <= 'z'
			}
			if prevLower || prevDigit || (prevUpper && nextLower) {
				result.WriteByte('_')
			}
		}
		if isUpper {
			result.WriteRune(r + 32)
		} else {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// toScreamingSnake converts a name to SCREAMING_SNAKE_CASE.
// isScreamingSnake returns true if s consists of uppercase letters, digits,
// and underscores only (e.g., "PARAMETER_NAMED", "TYPE_INT32").
func isScreamingSnake(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

func toScreamingSnake(s string) string {
	snake := toSnakeCase(s)
	return strings.ToUpper(snake)
}

// collectSourceFiles returns unique, sorted source file paths from functions.
func collectSourceFiles(functions []apispec.Function) []string {
	seen := make(map[string]bool)
	var result []string
	for _, fn := range functions {
		if fn.SourceFile != "" && !seen[fn.SourceFile] {
			seen[fn.SourceFile] = true
			result = append(result, fn.SourceFile)
		}
	}
	sort.Strings(result)
	return result
}

// SaveProto writes the .proto file to the given directory.
func SaveProto(dataDir string, content string, filename string) error {
	protoDir := filepath.Join(dataDir, "proto")
	if err := os.MkdirAll(protoDir, 0o755); err != nil {
		return fmt.Errorf("failed to create proto directory: %w", err)
	}
	path := filepath.Join(protoDir, filename)
	return os.WriteFile(path, []byte(content), 0o644)
}

// SaveOptionsProto writes the wasmify/options.proto file.
func SaveOptionsProto(dataDir string) error {
	wasmifyDir := filepath.Join(dataDir, "proto", "wasmify")
	if err := os.MkdirAll(wasmifyDir, 0o755); err != nil {
		return fmt.Errorf("failed to create wasmify proto directory: %w", err)
	}
	path := filepath.Join(wasmifyDir, "options.proto")
	return os.WriteFile(path, []byte(optionsProtoContent), 0o644)
}

// ValidateProto compiles the generated .proto content using protocompile to verify
// it is syntactically and semantically valid. The optionsProtoContent is the content
// of wasmify/options.proto that the generated proto imports.
func ValidateProto(protoContent string, packageName string) error {
	protoFileName := packageName + ".proto"

	// Create an in-memory source resolver. options.proto is the
	// canonical schema embedded into this binary at build time so
	// SaveOptionsProto and ValidateProto stay in lockstep.
	sources := map[string]string{
		protoFileName:          protoContent,
		"wasmify/options.proto": optionsProtoContent,
	}

	compiler := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{
			Accessor: protocompile.SourceAccessorFromMap(sources),
		}),
		Reporter: reporter.NewReporter(nil, nil),
	}

	_, err := compiler.Compile(context.Background(), protoFileName)
	if err != nil {
		return fmt.Errorf("proto validation failed: %w", err)
	}
	return nil
}

// isCallbackCandidate reports whether the class should have a callback
// service emitted alongside its handle service. The rule: the class is
// abstract (cannot be instantiated directly) and at least one of its
// methods is pure-virtual (`= 0`). These are the classes a user must
// provide a Go-side implementation of if they want to pass an instance
// into a bridged function.
func isCallbackCandidate(c *apispec.Class) bool {
	// Delegate to the bridge-side predicate so the proto service
	// emission and the C++ trampoline emission agree on which classes
	// are callback candidates — otherwise the proto would describe a
	// callback service that never gets a trampoline backing it.
	return isCallbackCandidateForBridge(c)
}

// pureVirtualMethods returns the set of virtuals (pure + declarable
// non-pure) that the trampoline will override and the callback
// service will surface as RPCs. Keeping the proto schema and C++
// trampoline single-sourced avoids method-ID drift.
//
// Name preserved for callers; "pure" in the name is historical —
// the set now includes non-pure virtuals whose signatures are
// marshalable.
func pureVirtualMethods(c *apispec.Class) []apispec.Function {
	return collectTrampolineMethods(c)
}

// writeCallbackService emits a `<Class>CallbackService` describing the
// C++→Go virtual-dispatch protocol. Each pure-virtual method is one
// RPC; the plugin turns the service into a Go interface + adapter
// (implementing the runtime CallbackHandler contract), and the C++
// bridge turns it into a trampoline class whose vtable forwards each
// virtual through wasmify_callback_invoke.
func writeCallbackService(b *strings.Builder, c *apispec.Class, emitted map[string]bool, serviceID int) {
	msgName := protoMessageName(c.QualName)
	serviceName := msgName + "CallbackService"
	methods := pureVirtualMethods(c)

	fmt.Fprintf(b, "service %s {\n", serviceName)
	b.WriteString("  option (wasmify.wasm_callback) = true;\n")
	fmt.Fprintf(b, "  option (wasmify.wasm_service_id) = %d;\n", serviceID)
	if c.SourceFile != "" {
		fmt.Fprintf(b, "  option (wasmify.wasm_service_source_file) = \"%s\";\n", c.SourceFile)
	}

	type cbEntry struct {
		rpcName      string
		originalName string
		method       apispec.Function
	}
	var entries []cbEntry
	usedNames := make(map[string]bool)
	for _, m := range methods {
		rpcName, originalName := toProtoRPCName(m.Name)
		if rpcName == "" {
			continue
		}
		// RPC-name collisions inside a single callback service would only
		// happen via disambiguation of overloads, which is already applied
		// upstream. If any collision still slips through, append "Method".
		if usedNames[rpcName] {
			if originalName == "" {
				originalName = m.Name
			}
			rpcName = rpcName + "Method"
		}
		usedNames[rpcName] = true
		entries = append(entries, cbEntry{rpcName: rpcName, originalName: originalName, method: m})
	}

	for i, e := range entries {
		// Callback request/response messages carry a "Callback" infix to
		// keep them distinct from the handle service's RPC messages —
		// pure-virtuals appear in both services (the handle service
		// treats them as regular methods), so sharing message names
		// would silently pull a `handle` field into the callback wire
		// format and break the Go adapter's param list.
		reqMsg := fmt.Sprintf("%sCallback%sRequest", msgName, e.rpcName)
		respMsg := fmt.Sprintf("%sCallback%sResponse", msgName, e.rpcName)
		fmt.Fprintf(b, "  rpc %s(%s) returns (%s) {\n", e.rpcName, reqMsg, respMsg)
		fmt.Fprintf(b, "    option (wasmify.wasm_method_id) = %d;\n", i)
		if e.originalName != "" {
			fmt.Fprintf(b, "    option (wasmify.wasm_original_name) = \"%s\";\n", e.originalName)
		}
		b.WriteString("  }\n")
	}
	b.WriteString("}\n\n")

	// Request/response messages. Names are unique (Callback infix) so
	// we don't need to worry about collision with the handle service's
	// messages.
	for _, e := range entries {
		reqMsg := fmt.Sprintf("%sCallback%sRequest", msgName, e.rpcName)
		respMsg := fmt.Sprintf("%sCallback%sResponse", msgName, e.rpcName)

		if !emitted[reqMsg] {
			emitted[reqMsg] = true
			fmt.Fprintf(b, "message %s {\n", reqMsg)
			for i, p := range e.method.Params {
				// Output-shaped handle params (`T**` and
				// `unique_ptr<T>* / shared_ptr<T>*`) move to the
				// response — skip them from the request. See
				// isCallbackOutputParam for the recognition rule.
				if isCallbackOutputParam(p.Type) {
					continue
				}
				protoType := typeRefToProto(p.Type)
				if isViewOfStringType(p.Type) {
					protoType = "repeated string"
				}
				fieldName := toSnakeCase(p.Name)
				if fieldName == "" {
					fieldName = fmt.Sprintf("arg%d", i)
				}
				fmt.Fprintf(b, "  %s %s = %d;\n", protoType, fieldName, i+1)
			}
			b.WriteString("}\n\n")
		}

		if !emitted[respMsg] {
			emitted[respMsg] = true
			fmt.Fprintf(b, "message %s {\n", respMsg)
			// Error-only return types (e.g. absl::Status) emit only
			// the error field — the "result" is the OK/error status
			// itself, conveyed by absence (OK) or presence (message)
			// of field 15.
			if e.method.ReturnType.Kind != apispec.TypeVoid &&
				!isErrorOnlyReturnType(e.method.ReturnType) {
				protoType := typeRefToProto(e.method.ReturnType)
				fmt.Fprintf(b, "  %s result = 1;\n", protoType)
			}
			// Output handle params surface as additional response
			// fields — the Go impl returns them rather than mutating
			// caller memory. Both `T**` and the smart-pointer-by-
			// pointer idiom (`unique_ptr<T>*`, `shared_ptr<T>*`) are
			// recognised here; the response field type is the bare
			// inner T's message name.
			for i, p := range e.method.Params {
				if !isCallbackOutputParam(p.Type) {
					continue
				}
				bare := callbackOutputInnerName(p.Type)
				protoType := protoMessageName(bare)
				fieldName := toSnakeCase(p.Name)
				if fieldName == "" {
					fieldName = fmt.Sprintf("arg%d", i)
				}
				prefix := ""
				if isCallbackOutputContainer(p.Type) {
					prefix = "repeated "
				}
				// Mark smart-pointer-by-pointer output (`unique_ptr<T>*`
				// / `shared_ptr<T>*`) with `wasm_take_ownership` so the
				// plugin emits a `clearPtr()` after writing the
				// returned handle to the wire — the C++ trampoline
				// wraps the raw pointer in a fresh smart pointer and
				// will delete the underlying object via its destructor.
				// Without the clear, the Go GC would later double-free
				// the same address through the wrapper's finalizer.
				//
				// Raw-pointer outputs (`T**` / `T*&`) leave the option
				// off — those are borrowed views, the C++ side does
				// not delete, and the Go wrapper retains ownership.
				suffix := ""
				if isCallbackOutputOwning(p.Type) {
					suffix = " [(wasmify.wasm_take_ownership) = true]"
				}
				fmt.Fprintf(b, "  %s%s %s = %d%s;\n", prefix, protoType, fieldName, i+1, suffix)
			}
			b.WriteString("  string error = 15;\n")
			b.WriteString("}\n\n")
		}
	}
}

