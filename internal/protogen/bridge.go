package protogen

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/wasmify/internal/apispec"
	"github.com/goccy/wasmify/internal/state"
)

// BridgeConfig is re-exported from internal/state. The struct definition
// lives there because the same shape is persisted under the `bridge`
// key of wasmify.json; aliasing here keeps the historical
// `protogen.BridgeConfig` symbol valid for callers and tests.
type BridgeConfig = state.BridgeConfig

// ErrorReturnSpec mirrors BridgeConfig — re-exported from internal/state.
type ErrorReturnSpec = state.ErrorReturnSpec

// toCIdentifier converts a string to a valid C identifier.
func toCIdentifier(s string) string {
	if strings.HasPrefix(s, "operator") {
		rpcName, _ := toProtoRPCName(s)
		if rpcName != "" {
			s = rpcName
		}
	}
	var result strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
			result.WriteRune(c)
		} else {
			result.WriteByte('_')
		}
	}
	return result.String()
}

// toMethodConstName converts a name to SCREAMING_SNAKE_CASE C identifier.
func toMethodConstName(name string) string {
	return strings.ToUpper(toSnakeCase(toCIdentifier(name)))
}

// filterBridgeFunctions returns functions suitable for bridge generation.
// Filters out operators, destructors, and functions not defined in project sources
// or using external types that can't be bridged.
func filterBridgeFunctions(functions []apispec.Function) []apispec.Function {
	var result []apispec.Function
	for _, fn := range functions {
		if isSkippedMethod(fn.Name) {
			continue
		}
		rpcName, _ := toProtoRPCName(fn.Name)
		if rpcName == "" {
			continue
		}
		if !isBridgeableFunction(fn) {
			continue
		}
		result = append(result, fn)
	}
	return result
}

// filterBridgeMethods returns methods of a class suitable for bridge generation.
// Unlike filterBridgeFunctions, it also filters out static methods and skipped names.
func filterBridgeMethods(methods []apispec.Function) []apispec.Function {
	var result []apispec.Function
	for _, m := range methods {
		if m.IsStatic {
			// Allow static factory methods that return the class's own handle type.
			// These are treated like constructors (e.g., FromStringView).
			if isStaticFactory(m) {
				result = append(result, m)
			}
			continue
		}
		if isSkippedMethod(m.Name) {
			continue
		}
		if m.IsRvalueRef {
			continue
		}
		if m.Access != "" && m.Access != "public" {
			continue
		}
		// Constructors require that the class supports heap allocation via
		// `new T(...)`. Skip if the class (or any ancestor) has deleted
		// operator new.
		if m.IsConstructor {
			qual := m.QualName
			// strip the trailing "::ClassName" to get the containing class name
			if idx := strings.LastIndex(qual, "::"); idx >= 0 {
				qual = qual[:idx]
			}
			if classNoNew != nil && classNoNew[qual] {
				continue
			}
		}
		rpcName, _ := toProtoRPCName(m.Name)
		if rpcName == "" {
			continue
		}
		if !isReturnableType(m.ReturnType) {
			continue
		}
		skip := false
		for _, p := range m.Params {
			if !isUsableType(p.Type) {
				skip = true
				break
			}
			if !isInstantiableType(p.Type) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		result = append(result, m)
	}
	return result
}

// filterBridgeFields filters fields to ensure valid getters can be generated.
func filterBridgeFields(fields []apispec.Field) []apispec.Field {
	var result []apispec.Field
	for _, f := range fields {
		if f.Name == "" {
			continue // no getter name → skip
		}
		// Only public fields can be accessed from outside the class
		if f.Access != "" && f.Access != "public" {
			continue
		}
		if !isUsableType(f.Type) {
			continue
		}
		result = append(result, f)
	}
	return result
}

// GenerateBridge generates C++ bridge code (api_bridge.cc) from an APISpec.
// projectRoot is the root directory of the C++ project, used to convert
// absolute header paths to project-relative include paths.
//
// BridgeConfig (the type the bridge accepts as configuration) is defined
// in package internal/state and re-exported above as a type alias so the
// same shape is shared between the in-memory generator and the on-disk
// wasmify.json schema.

// DefaultBridgeConfig returns a config with sensible defaults.
// The SkipStaticMethods list includes protobuf internal methods that match
// the static-factory heuristic but are not user-facing constructors.
func DefaultBridgeConfig() BridgeConfig {
	return BridgeConfig{
		ErrorTypes:       make(map[string]string),
		ErrorReconstruct: make(map[string]ErrorReturnSpec),
		SkipStaticMethods: []string{
			"default_instance",
			"internal_default_instance",
			"descriptor",
			"GetDescriptor",
			"GetReflection",
			"GetMetadata",
		},
	}
}

// classifyLibrary returns a library identifier from a source file path.
// The project's own sources return "" (always enabled).
// Protobuf generated files (.pb.h) return "protobuf".
// External bazel deps return the dep name (e.g., "abseil-cpp", "re2").
// Project subdirectories that are self-contained modules return the subdir
// name (e.g., "reference_impl", "tools", "testdata").
func classifyLibrary(sourceFile string) string {
	if sourceFile == "" {
		return ""
	}
	// External bazel dependencies (e.g., external/abseil-cpp~, external/boringssl~)
	normalized := normalizeHeaderPath(sourceFile, "")
	if strings.HasPrefix(normalized, "external/") {
		parts := strings.SplitN(normalized[len("external/"):], "/", 2)
		if len(parts) > 0 {
			// Clean up bazel module suffix (e.g., "abseil-cpp~" → "abseil-cpp")
			lib := strings.TrimRight(parts[0], "~")
			return lib
		}
	}
	// Protobuf generated code (.pb.h) that belongs to the project namespace
	// is NOT classified as an external library. Only protobuf's own headers
	// (under external/protobuf or google/protobuf/) are "protobuf" library.
	return ""
}

// isLibraryEnabled checks if a library is enabled in the bridge config.
// The empty string (project's own code) is always enabled.
// Libraries not listed in ExportDependentLibraries are enabled by default for
// backward compatibility. Only libraries explicitly set to false are disabled.
func isLibraryEnabled(lib string) bool {
	if lib == "" {
		return true // project's own code
	}
	if bridgeConfig.ExportDependentLibraries == nil {
		return true // no config → all enabled
	}
	enabled, exists := bridgeConfig.ExportDependentLibraries[lib]
	if !exists {
		return true // not listed → enabled by default
	}
	return enabled
}

// DiscoverLibraries scans an APISpec and returns a map of library names
// found in class source files, all set to false. cmdGenProto writes
// the result back to wasmify.json's `bridge.ExportDependentLibraries`
// section so users can selectively flip a library to true.
func DiscoverLibraries(spec *apispec.APISpec) map[string]bool {
	libs := make(map[string]bool)
	for _, c := range spec.Classes {
		lib := classifyLibrary(c.SourceFile)
		if lib != "" {
			libs[lib] = false
		}
	}
	return libs
}

// bridgeConfig is the active config, set by GenerateBridge.
var bridgeConfig BridgeConfig

// valueTypeParsers collects the set of value-type classes that appear as a
// parameter of some generated method or free function. For each such class we
// emit a `parse_<Mangled>(ProtoReader, <Type>& out)` helper in the bridge .cc
// that reads the corresponding submessage and populates the C++ struct. The
// set is populated during writeCallBody / writeConstructorBody / writeStatic-
// FactoryBody (at demotion time) and flushed after all dispatches are emitted.
var valueTypeParsers []*apispec.Class
var valueTypeParsersSeen map[string]bool

func recordValueTypeParserNeeded(c *apispec.Class) {
	if c == nil || c.QualName == "" {
		return
	}
	if valueTypeParsersSeen == nil {
		valueTypeParsersSeen = map[string]bool{}
	}
	if valueTypeParsersSeen[c.QualName] {
		return
	}
	valueTypeParsersSeen[c.QualName] = true
	valueTypeParsers = append(valueTypeParsers, c)
}

// valueTypeParserName returns the C++ identifier used for a value-type
// parser helper. Colons are replaced with underscores so
// `googlesql::BuiltinFunctionOptions` becomes `parse_googlesql__BuiltinFunctionOptions`.
func valueTypeParserName(qualName string) string {
	return "parse_" + strings.ReplaceAll(qualName, "::", "__")
}

// writeValueTypeParserForwards emits `static void parse_X(ProtoReader, T&);`
// declarations so dispatch bodies can reference the helpers before their
// definitions (placed at the end of the bridge .cc).
func writeValueTypeParserForwards(b *strings.Builder) {
	if len(valueTypeParsers) == 0 {
		return
	}
	b.WriteString("// Forward declarations of value-type submessage parsers.\n")
	for _, c := range valueTypeParsers {
		fmt.Fprintf(b, "static void %s(ProtoReader reader, %s& out);\n",
			valueTypeParserName(c.QualName), c.QualName)
	}
	b.WriteString("\n")
}

// writeValueTypeParserDefinitions emits the body of each parse_X helper.
// The helper walks the submessage and populates fields matching the order
// writeClassMessage used when emitting the proto (class.Fields[i] → field
// number i+1). For each field it picks the read strategy matching the
// clang-reported Kind; unsupported shapes (vectors, maps, etc.) are left
// as default-initialized.
func writeValueTypeParserDefinitions(b *strings.Builder, spec *apispec.APISpec) {
	if len(valueTypeParsers) == 0 {
		return
	}
	b.WriteString("// ======================================\n")
	b.WriteString("// Value-type submessage parsers\n")
	b.WriteString("// ======================================\n\n")
	for _, c := range valueTypeParsers {
		fmt.Fprintf(b, "static void %s(ProtoReader reader, %s& out) {\n",
			valueTypeParserName(c.QualName), c.QualName)
		b.WriteString("    while (reader.has_data() && reader.next()) {\n")
		b.WriteString("        switch (reader.field()) {\n")
		for i, f := range c.Fields {
			fieldNum := i + 1
			writeValueTypeFieldCase(b, f, fieldNum, spec)
		}
		b.WriteString("        default: reader.skip(); break;\n")
		b.WriteString("        }\n")
		b.WriteString("    }\n")
		b.WriteString("}\n\n")
	}
}

// writeValueTypeFieldCase emits a single `case N:` branch for the parser
// body. target is the struct-member accessor (`out.<field>`). Only field
// kinds the bridge already knows how to encode on the write side are
// supported — everything else falls through to `reader.skip()` so the
// C++ default-initialized value is used.
func writeValueTypeFieldCase(b *strings.Builder, f apispec.Field, fieldNum int, spec *apispec.APISpec) {
	target := "out." + f.Name
	fmt.Fprintf(b, "        case %d: {\n", fieldNum)
	defer b.WriteString("            break;\n        }\n")
	switch f.Type.Kind {
	case apispec.TypePrimitive:
		cpp := cppPrimitiveType(f.Type.Name)
		switch cpp {
		case "bool":
			fmt.Fprintf(b, "            %s = reader.read_bool();\n", target)
		case "float":
			fmt.Fprintf(b, "            %s = reader.read_float();\n", target)
		case "double":
			fmt.Fprintf(b, "            %s = reader.read_double();\n", target)
		case "int32_t":
			fmt.Fprintf(b, "            %s = reader.read_int32();\n", target)
		case "uint32_t":
			fmt.Fprintf(b, "            %s = reader.read_uint32();\n", target)
		case "int64_t":
			fmt.Fprintf(b, "            %s = reader.read_int64();\n", target)
		case "uint64_t":
			fmt.Fprintf(b, "            %s = reader.read_uint64();\n", target)
		default:
			fmt.Fprintf(b, "            %s = static_cast<%s>(reader.read_varint());\n", target, cpp)
		}
	case apispec.TypeString:
		if f.Type.IsPointer {
			// `std::string* field` on a struct is typically an output-parameter
			// buffer (e.g. ValueEqualityCheckOptions::reason). We can't
			// meaningfully bridge the pointee back to Go because the C++ side
			// writes to it asynchronously, so skip the field and leave the
			// default nullptr.
			fmt.Fprintf(b, "            reader.skip(); // %s: std::string* not bridgeable\n", f.Name)
			return
		}
		fmt.Fprintf(b, "            %s = reader.read_string();\n", target)
	case apispec.TypeEnum:
		fmt.Fprintf(b, "            %s = static_cast<%s>(reader.read_int32() - 1);\n", target, cppTypeName(f.Type))
	case apispec.TypeHandle:
		// Handle-typed field stored by value inside the struct (C++ member
		// is a `T` or `const T&`). The Go marshaler encodes a handle sub-
		// submessage (field 1 = uint64 ptr); we dereference the pointer
		// and copy-construct into the struct member. We only skip when the
		// class explicitly deletes its copy constructor (`HasDeletedCopyCtor`);
		// the broader classDeletedCopy heuristic also flags opaque classes
		// with private-only state as non-copyable, but those classes are
		// frequently copy-constructible in practice (e.g. LanguageOptions).
		inner := resolveTypeName(cppTypeName(f.Type))
		if !f.Type.IsPointer {
			if c, ok := valueClasses[inner]; ok && c != nil && c.HasDeletedCopyCtor {
				fmt.Fprintf(b, "            reader.skip(); // %s: class has deleted copy ctor\n", f.Name)
				return
			}
		}
		b.WriteString("            uint64_t _ptr = read_handle_ptr(reader);\n")
		constQual := ""
		if f.Type.IsConst {
			constQual = "const "
		}
		if f.Type.IsPointer {
			fmt.Fprintf(b, "            %s = reinterpret_cast<%s%s*>(_ptr);\n", target, constQual, inner)
		} else {
			fmt.Fprintf(b, "            if (_ptr != 0) %s = *reinterpret_cast<%s%s*>(_ptr);\n", target, constQual, inner)
		}
	case apispec.TypeValue:
		inner := resolveTypeName(cppTypeName(f.Type))
		if c, ok := valueClasses[inner]; ok && !c.IsHandle {
			recordValueTypeParserNeeded(c)
			parser := valueTypeParserName(inner)
			fmt.Fprintf(b, "            ProtoReader _sub = reader.read_submessage();\n")
			fmt.Fprintf(b, "            %s(_sub, %s);\n", parser, target)
			return
		}
		fmt.Fprintf(b, "            reader.skip(); // %s: value-type parser unavailable\n", f.Name)
	default:
		fmt.Fprintf(b, "            reader.skip(); // %s: unsupported shape\n", f.Name)
	}
}

func GenerateBridge(spec *apispec.APISpec, packageName string, projectRoot string) string {
	return GenerateBridgeWithConfig(spec, packageName, projectRoot, DefaultBridgeConfig())
}

// GenerateBridgeWithConfig generates C++ bridge code using project-specific config.
func GenerateBridgeWithConfig(spec *apispec.APISpec, packageName string, projectRoot string, cfg BridgeConfig) string {
	bridgeConfig = cfg
	skipBridgeHeadersMap = make(map[string]bool)
	for _, h := range cfg.SkipHeaders {
		skipBridgeHeadersMap[h] = true
	}
	skipBridgeClassesMap = make(map[string]bool)
	for _, c := range cfg.SkipClasses {
		skipBridgeClassesMap[c] = true
	}
	var b strings.Builder

	// Build class name → QualName lookup for resolving short names
	specNamespace = spec.Namespace
	if specNamespace == "" {
		// Fallback: infer from package name
		specNamespace = packageName
	}
	classQualNames = buildClassQualNameMap(spec)
	// Build class → source file map for filtering external types
	classSourceFiles = buildClassSourceFileMap(spec)
	enumQualNames = make(map[string]bool)
	ResetNestedEnumAliases()
	for _, e := range spec.Enums {
		if e.QualName != "" {
			enumQualNames[e.QualName] = true
			// googlesql (and similar projects that host enum definitions
			// in a .proto file) expose protobuf-mangled enum names like
			// "ns::ResolvedXEnums_JoinType" to C++ via a separately-
			// defined nested enum ("ns::ResolvedX::JoinType") whose
			// values alias the proto enum's values. Clang records method
			// return / parameter types via the nested spelling, so the
			// bridge keeps that form for C++ emission. Register both
			// forms in enumQualNames so type-usability checks succeed
			// regardless of which spelling clang used, and remember the
			// alias mapping so proto emission can switch to the
			// mangled form.
			if alias := protoEnumToNestedCppName(e.QualName); alias != "" {
				enumQualNames[alias] = true
				RegisterNestedEnumAlias(alias, e.QualName)
				if specNamespace != "" && strings.HasPrefix(alias, specNamespace+"::") {
					short := strings.TrimPrefix(alias, specNamespace+"::")
					enumQualNames[short] = true
					RegisterNestedEnumAlias(short, e.QualName)
				}
			}
		}
	}
	// Build class → is_abstract map for filtering non-instantiable types
	classAbstract = buildClassAbstractMap(spec)
	// Full class lookup for inheritance traversal.
	classByQualName = make(map[string]*apispec.Class, len(spec.Classes))
	for i := range spec.Classes {
		classByQualName[spec.Classes[i].QualName] = &spec.Classes[i]
	}
	// Build class → no-default-ctor map for filtering non-instantiable types
	classNoDefaultCtor = buildClassNoDefaultCtorMap(spec)
	// Build class → deleted copy ctor map for filtering pass-by-value types
	classDeletedCopy = buildClassDeletedCopyMap(spec)
	// Build class → deleted operator new map for filtering arena-only types
	classNoNew = buildClassNoNewMap(spec)
	// Build value-class field lookup for TypeValue serialization
	valueClasses = buildValueClassMap(spec)
	// Reset the value-type parser registry for this run. Populated during
	// demotion in writeCallBody / writeConstructorBody / writeStaticFactoryBody.
	valueTypeParsers = nil
	valueTypeParsersSeen = nil

	// Collect data first (needed by header generation)
	freeFunctions := filterBridgeFunctions(disambiguateOverloads(spec.Functions))
	handleClasses := collectHandleClasses(spec)
	var classNames []string
	for name := range handleClasses {
		classNames = append(classNames, name)
	}
	sort.Strings(classNames)

	writeHeader(&b, spec, packageName, projectRoot, freeFunctions, handleClasses)
	writeProtoHelpers(&b)

	writeServiceMethodIDs(&b, freeFunctions, handleClasses, classNames, packageName)
	writeCallbackTrampolines(&b, handleClasses, classNames)

	// Dispatch functions. Two passes: the first collects value-type params
	// (so we know which `parse_<T>` helpers to emit); the second produces
	// the actual dispatch bodies. We capture first-pass output into a
	// throwaway builder and discard it — the parser-registry state is the
	// only thing we keep. The final output below re-runs the emit.
	//
	// Service-id assignment must match proto.go: free functions take id 0
	// when present, otherwise handle classes start at 0 in alphabetical
	// order. The numbering ends up encoded into the per-method export
	// names (`w_<svc>_<mid>`).
	freeServiceID := -1
	if len(freeFunctions) > 0 {
		freeServiceID = 0
	}
	handleServiceID := func(idx int) int {
		if freeServiceID >= 0 {
			return idx + 1
		}
		return idx
	}
	{
		var scratch strings.Builder
		if freeServiceID >= 0 {
			writeFreeFunctionDispatch(&scratch, freeFunctions, packageName, spec, freeServiceID)
		}
		for i, qualName := range classNames {
			writeHandleDispatch(&scratch, handleClasses[qualName], handleClasses, spec, handleServiceID(i))
		}
	}
	// Forward-declare every value-type parser so dispatch bodies can call
	// them regardless of emission order; the definitions come after.
	writeValueTypeParserForwards(&b)
	// Dispatch functions (final pass — its writeCallBody calls re-route
	// value-type params through `parse_<T>` helpers we forward-declared).
	if freeServiceID >= 0 {
		writeFreeFunctionDispatch(&b, freeFunctions, packageName, spec, freeServiceID)
	}
	for i, qualName := range classNames {
		c := handleClasses[qualName]
		writeHandleDispatch(&b, c, handleClasses, spec, handleServiceID(i))
	}
	// Full definitions of the value-type parsers. Placed last so they can
	// reference each other and any helpers declared above.
	writeValueTypeParserDefinitions(&b, spec)

	writeMainDispatcher(&b, freeFunctions, classNames, packageName, handleClasses)
	writeInitShutdown(&b)
	b.WriteString("} // extern \"C\"\n")

	return b.String()
}

func writeHeader(b *strings.Builder, spec *apispec.APISpec, packageName string, projectRoot string,
	freeFunctions []apispec.Function, handleClasses map[string]*apispec.Class) {
	b.WriteString("// Auto-generated by wasmify gen-proto. DO NOT EDIT.\n")
	b.WriteString("#include <cstdlib>\n")
	b.WriteString("#include <cstring>\n")
	b.WriteString("#include <cstdint>\n")
	b.WriteString("#include <string>\n")
	b.WriteString("#include <vector>\n\n")

	// Collect headers only for types used in the bridge dispatch
	headers := collectBridgeSourceFiles(freeFunctions, handleClasses, projectRoot)
	for _, h := range headers {
		fmt.Fprintf(b, "#include \"%s\"\n", h)
	}
	if len(headers) > 0 {
		b.WriteString("\n")
	}

	b.WriteString("#define WASM_EXPORT(name) __attribute__((export_name(#name)))\n")
	b.WriteString("#define WASM_IMPORT(module, name) __attribute__((import_module(#module), import_name(#name)))\n\n")

	b.WriteString("extern \"C\" {\n\n")

	// Memory management
	b.WriteString("WASM_EXPORT(wasm_alloc)\nvoid* wasm_alloc(int32_t size) { return malloc(size); }\n\n")
	b.WriteString("WASM_EXPORT(wasm_free)\nvoid wasm_free(void* ptr) { free(ptr); }\n\n")

	// Callback import
	b.WriteString("WASM_IMPORT(wasmify, callback_invoke)\n")
	b.WriteString("int64_t wasmify_callback_invoke(int32_t callback_id, int32_t method_id, void* req, int32_t req_len);\n\n")
}

// writeProtoHelpers emits lightweight proto3 wire format reader/writer in C++.
func writeProtoHelpers(b *strings.Builder) {
	b.WriteString(`// ======================================
// Proto3 wire format helpers
// ======================================

static int64_t encode_result(void* ptr, int32_t len) {
    return (static_cast<int64_t>(reinterpret_cast<uintptr_t>(ptr)) << 32) | static_cast<uint32_t>(len);
}

class ProtoReader {
public:
    const uint8_t* pos_;
    const uint8_t* end_;
    uint32_t last_field_;
    uint32_t last_wire_;

    ProtoReader(const void* data, int32_t len)
        : pos_(static_cast<const uint8_t*>(data)),
          end_(pos_ + len), last_field_(0), last_wire_(0) {}

    bool has_data() const { return pos_ < end_; }

    bool next() {
        if (pos_ >= end_) return false;
        uint64_t tag;
        if (!read_varint_raw(&tag)) return false;
        last_field_ = static_cast<uint32_t>(tag >> 3);
        last_wire_ = static_cast<uint32_t>(tag & 7);
        return true;
    }

    uint32_t field() const { return last_field_; }
    uint32_t wire() const { return last_wire_; }

    uint64_t read_varint() {
        uint64_t v = 0;
        read_varint_raw(&v);
        return v;
    }

    int32_t read_int32() { return static_cast<int32_t>(read_varint()); }
    int64_t read_int64() { return static_cast<int64_t>(read_varint()); }
    uint32_t read_uint32() { return static_cast<uint32_t>(read_varint()); }
    uint64_t read_uint64() { return read_varint(); }
    bool read_bool() { return read_varint() != 0; }

    float read_float() {
        float v = 0;
        if (pos_ + 4 <= end_) { memcpy(&v, pos_, 4); pos_ += 4; }
        return v;
    }

    double read_double() {
        double v = 0;
        if (pos_ + 8 <= end_) { memcpy(&v, pos_, 8); pos_ += 8; }
        return v;
    }

    std::string read_string() {
        uint64_t len = read_varint();
        if (pos_ + len > end_) { pos_ = end_; return ""; }
        std::string s(reinterpret_cast<const char*>(pos_), static_cast<size_t>(len));
        pos_ += len;
        return s;
    }

    // Returns a sub-reader for an embedded message
    ProtoReader read_submessage() {
        uint64_t len = read_varint();
        if (pos_ + len > end_) { pos_ = end_; return ProtoReader(nullptr, 0); }
        ProtoReader sub(pos_, static_cast<int32_t>(len));
        pos_ += len;
        return sub;
    }

    void skip() {
        switch (last_wire_) {
        case 0: read_varint(); break;
        case 1: pos_ += 8; break;
        case 2: { uint64_t len = read_varint(); pos_ += len; } break;
        case 5: pos_ += 4; break;
        default: pos_ = end_; break;
        }
        if (pos_ > end_) pos_ = end_;
    }

private:
    bool read_varint_raw(uint64_t* val) {
        *val = 0;
        for (int shift = 0; shift < 64 && pos_ < end_; shift += 7) {
            uint8_t byte = *pos_++;
            *val |= static_cast<uint64_t>(byte & 0x7F) << shift;
            if ((byte & 0x80) == 0) return true;
        }
        return false;
    }
};

// ProtoWriter defers its initial heap allocation until the first write.
// Serialising an empty response therefore costs zero malloc/free pairs;
// the previous eager allocation leaked a 128-byte buffer on every call
// that returned no bytes (setters, voids, empty getters), which for a
// single-run parse/analyze loop piled up millions of bytes of wasm heap
// until the module ran out of memory.
class ProtoWriter {
public:
    uint8_t* data_;
    int32_t size_;
    int32_t cap_;

    ProtoWriter() : data_(nullptr), size_(0), cap_(0) {}

    // Destructor frees any heap buffer the writer still owns. Two
    // usage shapes own data_ differently. (1) The wasm export
    // return path ends with finish(); finish() transfers ownership
    // of data_ to the host (host calls wasm_free after reading) and
    // NULLs data_ here so this destructor skips the free. (2) The
    // callback trampoline (C++ to Go) hands data_/size_ to
    // wasmify_callback_invoke; the host READS but does not take
    // ownership, so when the writer goes out of scope this
    // destructor must release data_. Without (2), every callback
    // dispatch leaks 128+ bytes of wasm heap; after enough rounds
    // dlmalloc fragments and subsequent allocations either return
    // NULL or trip OOB inside the allocator's freelist walk.
    ~ProtoWriter() {
        if (data_ != nullptr) {
            free(data_);
            data_ = nullptr;
        }
    }

    // Disable copy: data_ is malloc'd; copying would alias the
    // pointer and double-free at scope exit.
    ProtoWriter(const ProtoWriter&) = delete;
    ProtoWriter& operator=(const ProtoWriter&) = delete;

    void ensure(int32_t need) {
        if (data_ == nullptr) {
            cap_ = need > 128 ? need : 128;
            data_ = static_cast<uint8_t*>(malloc(cap_));
            return;
        }
        while (size_ + need > cap_) {
            cap_ *= 2;
            data_ = static_cast<uint8_t*>(realloc(data_, cap_));
        }
    }

    void write_raw(const void* src, int32_t len) {
        ensure(len);
        memcpy(data_ + size_, src, len);
        size_ += len;
    }

    void write_varint(uint64_t val) {
        ensure(10);
        while (val > 0x7F) {
            data_[size_++] = static_cast<uint8_t>((val & 0x7F) | 0x80);
            val >>= 7;
        }
        data_[size_++] = static_cast<uint8_t>(val);
    }

    void write_tag(uint32_t field, uint32_t wire) {
        write_varint((static_cast<uint64_t>(field) << 3) | wire);
    }

    void write_int32(uint32_t field, int32_t val) {
        if (val == 0) return;
        write_tag(field, 0);
        write_varint(static_cast<uint64_t>(static_cast<uint32_t>(val)));
    }

    void write_int64(uint32_t field, int64_t val) {
        if (val == 0) return;
        write_tag(field, 0);
        write_varint(static_cast<uint64_t>(val));
    }

    void write_uint32(uint32_t field, uint32_t val) {
        if (val == 0) return;
        write_tag(field, 0);
        write_varint(val);
    }

    void write_uint64(uint32_t field, uint64_t val) {
        if (val == 0) return;
        write_tag(field, 0);
        write_varint(val);
    }

    void write_bool(uint32_t field, bool val) {
        if (!val) return;
        write_tag(field, 0);
        write_varint(1);
    }

    void write_float(uint32_t field, float val) {
        uint32_t bits;
        memcpy(&bits, &val, 4);
        if (bits == 0) return;
        write_tag(field, 5);
        write_raw(&val, 4);
    }

    void write_double(uint32_t field, double val) {
        uint64_t bits;
        memcpy(&bits, &val, 8);
        if (bits == 0) return;
        write_tag(field, 1);
        write_raw(&val, 8);
    }

    void write_string(uint32_t field, const std::string& s) {
        if (s.empty()) return;
        write_tag(field, 2);
        write_varint(s.size());
        write_raw(s.data(), static_cast<int32_t>(s.size()));
    }

    void write_string(uint32_t field, const char* s, uint32_t len) {
        if (len == 0) return;
        write_tag(field, 2);
        write_varint(len);
        write_raw(s, len);
    }

    void write_submessage(uint32_t field, const ProtoWriter& sub) {
        if (sub.size_ == 0) return;
        write_tag(field, 2);
        write_varint(sub.size_);
        write_raw(sub.data_, sub.size_);
    }

    void write_handle(uint32_t field, uint64_t ptr) {
        if (ptr == 0) return;
        // Handle is a submessage with field 1 = uint64 ptr.
        // sub's destructor releases sub.data_ when it goes out of
        // scope; we MUST NOT free it here as well — that double-free
        // corrupts dlmalloc's free list and surfaces as a delayed
        // "out of bounds memory access" on a later wasm_alloc.
        ProtoWriter sub;
        sub.write_uint64(1, ptr);
        write_submessage(field, sub);
    }

    // ---- Repeated primitive helpers (packed encoding) ----
    // Note: this class lives inside extern "C" {}, so we cannot declare
    // member templates. Each packed variant is written out explicitly.
    // Each helper leaves sub.data_ to its destructor — write_submessage
    // copies the bytes into *this, so the temporary's buffer must be
    // freed exactly once.

    void write_repeated_int32(uint32_t field, const std::vector<int32_t>& vec) {
        if (vec.empty()) return;
        ProtoWriter sub;
        for (int32_t v : vec) sub.write_varint(static_cast<uint64_t>(static_cast<uint32_t>(v)));
        write_submessage(field, sub);
    }
    void write_repeated_int64(uint32_t field, const std::vector<int64_t>& vec) {
        if (vec.empty()) return;
        ProtoWriter sub;
        for (int64_t v : vec) sub.write_varint(static_cast<uint64_t>(v));
        write_submessage(field, sub);
    }
    void write_repeated_uint32(uint32_t field, const std::vector<uint32_t>& vec) {
        if (vec.empty()) return;
        ProtoWriter sub;
        for (uint32_t v : vec) sub.write_varint(static_cast<uint64_t>(v));
        write_submessage(field, sub);
    }
    void write_repeated_uint64(uint32_t field, const std::vector<uint64_t>& vec) {
        if (vec.empty()) return;
        ProtoWriter sub;
        for (uint64_t v : vec) sub.write_varint(v);
        write_submessage(field, sub);
    }
    void write_repeated_bool(uint32_t field, const std::vector<bool>& vec) {
        if (vec.empty()) return;
        ProtoWriter sub;
        for (bool v : vec) sub.write_varint(v ? 1u : 0u);
        write_submessage(field, sub);
    }

    void write_repeated_double(uint32_t field, const std::vector<double>& vec) {
        if (vec.empty()) return;
        write_tag(field, 2);
        write_varint(static_cast<uint64_t>(vec.size()) * 8u);
        for (double v : vec) write_raw(&v, 8);
    }
    void write_repeated_float(uint32_t field, const std::vector<float>& vec) {
        if (vec.empty()) return;
        write_tag(field, 2);
        write_varint(static_cast<uint64_t>(vec.size()) * 4u);
        for (float v : vec) write_raw(&v, 4);
    }

    // Repeated string/bytes are NOT packed in proto3; each element emits
    // its own length-delimited tag.
    void write_repeated_string(uint32_t field, const std::vector<std::string>& vec) {
        for (const auto& s : vec) {
            write_tag(field, 2);
            write_varint(s.size());
            write_raw(s.data(), static_cast<int32_t>(s.size()));
        }
    }

    // Repeated handle: each element becomes a length-delimited submessage
    // with field 1 = uint64 ptr. Null pointers are skipped. Two overloads
    // cover the common vector<T*> and vector<const T*> cases; note that we
    // cannot use a member template here (extern "C" block).
    void write_repeated_handle(uint32_t field, const std::vector<void*>& vec) {
        for (void* p : vec) {
            if (p == nullptr) continue;
            write_handle(field, reinterpret_cast<uint64_t>(p));
        }
    }

    void write_error(const char* msg) {
        write_string(15, msg, static_cast<uint32_t>(strlen(msg)));
    }

    void write_error(const std::string& msg) {
        write_string(15, msg);
    }

    int64_t finish() {
        // Empty response: the host short-circuits on length and never
        // calls wasm_free, so any lazy-allocated buffer would leak.
        // Free it here and return nullptr so the host skips cleanly.
        if (size_ == 0) {
            if (data_ != nullptr) {
                free(data_);
                data_ = nullptr;
                cap_ = 0;
            }
            return encode_result(nullptr, 0);
        }
        // Transfer ownership of data_ to the host. Clear our pointer
        // so the destructor does not double-free what the host now
        // owns and will release via wasm_free after reading.
        uint8_t* released = data_;
        int32_t released_size = size_;
        data_ = nullptr;
        size_ = 0;
        cap_ = 0;
        return encode_result(released, released_size);
    }
};

// Read handle ptr from a submessage (field 1 = uint64)
static uint64_t read_handle_ptr(ProtoReader& reader) {
    ProtoReader sub = reader.read_submessage();
    uint64_t ptr = 0;
    while (sub.has_data() && sub.next()) {
        if (sub.field() == 1) ptr = sub.read_uint64();
        else sub.skip();
    }
    return ptr;
}

// Read handle ptr from top-level message (field 1 = uint64)
static uint64_t read_handle_direct(const void* req, int32_t req_len) {
    ProtoReader reader(req, req_len);
    uint64_t ptr = 0;
    while (reader.has_data() && reader.next()) {
        if (reader.field() == 1) ptr = reader.read_uint64();
        else reader.skip();
    }
    return ptr;
}

`)
}

// ======================================
// Type-to-C++ helpers
// ======================================

// specNamespace is the top-level namespace of the project (from APISpec.Namespace).
// Used as a fallback for qualifying unresolved type names in template arguments.
var specNamespace string

// classQualNames maps short class name → fully qualified name.
// Populated per-generation by buildClassQualNameMap.
var classQualNames map[string]string

// ambiguousShortNames holds short class names that appear in multiple
// namespaces (e.g., both ns::tokens::Token and ns::formatter::Token).
// Types with ambiguous names cannot be safely resolved without context.
var ambiguousShortNames map[string]bool

// isProjectSource returns true if the source file path indicates an API
// defined in the project itself (not an external dependency).
// Excludes: external/* (bazel external deps), system headers, bazel-out dirs.
func isProjectSource(sourceFile string) bool {
	if sourceFile == "" {
		// No source info → we can't include the header, exclude from bridge
		return false
	}
	// Normalize the path first (strip execroot, bazel-out, etc.)
	normalized := normalizeHeaderPath(sourceFile, "")
	// External Bazel dependencies
	if strings.HasPrefix(normalized, "external/") {
		return false
	}
	// System/standard library headers
	if strings.Contains(normalized, "/c++/v1/") || strings.Contains(normalized, "/include/c++/") {
		return false
	}
	if strings.HasPrefix(normalized, "/usr/") || strings.HasPrefix(normalized, "/Library/") {
		return false
	}
	return true
}

// isUsableType returns true if a type can be used in the bridge.
// All C++ types are bridgeable — the bridge uses void*/uint64 handles for
// class types and generates the appropriate code pattern (cast, move, release)
// for each case. This function only rejects truly unusable patterns:
// system-level opaque pointers and types from non-project sources.
func isUsableType(ref apispec.TypeRef) bool {
	// void* params are opaque pointers with no type info
	if ref.Kind == apispec.TypeVoid && ref.IsPointer {
		return false
	}
	switch ref.Kind {
	case apispec.TypeVoid, apispec.TypePrimitive, apispec.TypeString, apispec.TypeEnum:
		return true
	case apispec.TypeVector:
		if ref.Inner != nil {
			return isUsableType(*ref.Inner)
		}
		return false
	case apispec.TypeHandle, apispec.TypeValue:
		if classSourceFiles == nil {
			return true
		}
		// Set-like container parameters: when a method's signature
		// uses an `std::set` / `std::unordered_set` / configured
		// `BridgeConfig.SetLikeTypePrefixes` template, the proto
		// schema emits `repeated <ElemHandle>` for it (handled by
		// parseMapType / handleMapType), and the bridge body
		// materialises the container element-by-element from the
		// wire (see setLikeContainerInfo). The filter must
		// reflect the same view: usable iff the element type is
		// itself usable. Without this, every method taking a
		// set-of-handles silently disappears from the binding even
		// though the proto and bridge sides know how to handle it.
		if elem, _, isSet := parseMapType(ref.Name); isSet && looksLikeSetTypeName(ref.Name) {
			if elem == "" {
				return false
			}
			elemRef := typeRefFromTemplateArg(strings.TrimSpace(elem))
			return isUsableType(elemRef)
		}
		bareName := cppTypeName(ref)
		qual := resolveTypeName(bareName)
		src, known := classSourceFiles[qual]
		if !known {
			// Check if it's a known enum (enums are not in classSourceFiles).
			// Protobuf-generated enums (e.g., ParameterMode) are valid types
			// even though they live in .pb.h files.
			if enumQualNames != nil && enumQualNames[qual] {
				return true
			}
			if isAllowedExternalType(ref.Name) || isAllowedExternalType(bareName) {
				// External type is allowed, but check if template arguments
				// contain types that are NOT in the api-spec. E.g.,
				// `absl::Span<const NameAndAnnotatedType>` where NameAndAnnotatedType
				// is a nested typedef not in classSourceFiles.
				if strings.Contains(ref.QualType, "<") {
					inner := extractTemplateArgFromQualType(ref.QualType)
					if inner != "" {
						innerClean := strings.TrimSpace(inner)
						innerClean = strings.TrimPrefix(innerClean, "const ")
						innerClean = strings.TrimSuffix(innerClean, "*")
						innerClean = strings.TrimSpace(innerClean)
						// Reject if inner type is unknown or is a nested class
						// (e.g., SimpleSQLView::NameAndType) — scoped typedefs
						// can't be used outside their parent class.
						if len(innerClean) > 0 && innerClean[0] >= 'A' && innerClean[0] <= 'Z' {
							resolved := resolveTypeName(innerClean)
							if _, innerKnown := classSourceFiles[resolved]; !innerKnown {
								return false
							}
							// Nested class: ns::Class::Nested has 3+ parts
							if strings.Count(resolved, "::") >= 2 {
								return false
							}
						}
					}
				}
				return true
			}
			// Smart pointers wrapping any type are always usable
			if isSmartPointerType(bareName) || isSmartPointerType(ref.QualType) {
				return true
			}
			return false
		}
		if !isProjectSource(src) {
			// Allow when explicitly opted in via either
			// `bridge.IncludeExternalClasses` (for parsed external
			// classes whose methods we want bridged) or
			// `bridge.ExternalTypes` (for opaque external handles
			// where we only need parameter passing).
			if isIncludedExternalClass(qual) || isAllowedExternalType(qual) {
				return true
			}
			return false
		}
		// Skip types from disabled libraries
		lib := classifyLibrary(src)
		if !isLibraryEnabled(lib) {
			return false
		}
		return true
	case apispec.TypeUnknown:
		return false
	}
	return false
}

// isInstantiableType returns true if a value of this type can be declared
// as a local variable (e.g., for output parameters). Abstract classes, and
// classes without an accessible default constructor (e.g., copy-only or
// delete-default) cannot be declared as locals.
func isInstantiableType(ref apispec.TypeRef) bool {
	// vector<T> where T has a deleted copy ctor triggers template
	// instantiation of vector's copy constructor at compile time even
	// if the copy is never called at runtime. Filter such vectors.
	if ref.Kind == apispec.TypeVector && ref.Inner != nil {
		innerQual := resolveTypeName(cppTypeName(*ref.Inner))
		// `vector<T*>` (or any pointer-element form) stores pointer
		// slots, never T values. Properties of T that block by-value
		// vector use — abstract, deleted copy ctor, no default ctor —
		// are irrelevant when the element is a pointer. The bridge
		// reads handle pointers from the proto wire and pushes them
		// back as raw `T*` without ever constructing T.
		innerIsPointer := ref.Inner.IsPointer
		if !innerIsPointer {
			if classDeletedCopy != nil && classDeletedCopy[innerQual] {
				return false
			}
			// Abstract classes cannot be stored by value in a vector.
			if classAbstract != nil && classAbstract[innerQual] {
				return false
			}
			// Classes without a public default constructor cannot be
			// default-constructed in a vector... unless the bridge can
			// emplace_back them from their fields. A POD-shaped value
			// class (public fields, has a matching constructor) can be
			// built element-by-element even when a vector-wide
			// default-construct is impossible. valueClasses captures the
			// field layout; we rely on bridge emission to produce
			// emplace_back instead of default-construct-then-fill. Handle
			// types are likewise fine: Go passes existing handles, so the
			// bridge push_back's *deref'd_ptr via the class's copy ctor
			// without ever default-constructing an element.
			if classNoDefaultCtor != nil && classNoDefaultCtor[innerQual] &&
				ref.Inner.Kind != apispec.TypeHandle {
				if c, ok := valueClasses[innerQual]; !ok || len(c.Fields) == 0 {
					return false
				}
			}
		}
		innerName := ref.Inner.Name
		if innerName == "" {
			innerName = ref.Inner.QualType
		}
		// Smart-pointer inner types: unique_ptr is allowed because
		// it's move-only and the bridge transports each element as a
		// raw uint64 handle that the C++ side wraps via
		// `std::unique_ptr<T>(reinterpret_cast<T*>(h))`. shared_ptr
		// stays rejected — the bridge has no shared-ownership
		// transport across the wasm boundary today (each side would
		// need to keep its own live shared_ptr to participate in the
		// refcount, and that round-trip isn't wired).
		if isSharedPointerType(innerName) || isSharedPointerType(cppTypeName(*ref.Inner)) {
			return false
		}
		if matchErrorType(innerName) != "" {
			return false
		}
	}
	return true
}

// classSourceFiles maps fully qualified class name → source file.
// Populated per-generation alongside classQualNames.
var classSourceFiles map[string]string
var enumQualNames map[string]bool // set of known enum qualified names

// protoEnumToNestedCppName maps a protobuf-style enum qual name like
// "ns::ResolvedJoinScanEnums_JoinType" to the C++ nested-scope alias
// "ns::ResolvedJoinScan::JoinType" that the surrounding class exposes
// via `using`. Returns "" when the pattern does not match.
func protoEnumToNestedCppName(qual string) string {
	marker := "Enums_"
	idx := strings.Index(qual, marker)
	if idx < 0 {
		return ""
	}
	prefix := qual[:idx]     // e.g. "ns::ResolvedJoinScan"
	suffix := qual[idx+len(marker):]
	if prefix == "" || suffix == "" {
		return ""
	}
	return prefix + "::" + suffix
}

// classAbstract maps fully qualified class name → is abstract.
var classAbstract map[string]bool

// classByQualName is the full class map for traversal operations that
// need to walk inheritance chains (e.g., collecting every pure virtual
// a derived trampoline must override — including those declared on
// ancestor classes).
var classByQualName map[string]*apispec.Class

// classNoDefaultCtor maps fully qualified class name → has no default constructor.
// Classes that cannot be default-constructed (no ctor or deleted default) cannot
// be declared as local variables in the bridge.
var classNoDefaultCtor map[string]bool

// classDeletedCopy maps fully qualified class name → copy constructor is deleted.
// Such classes cannot be passed by value or copied.
var classDeletedCopy map[string]bool

// classNoNew maps fully qualified class name → operator new is deleted.
// Such classes (e.g., arena-allocated protobuf messages) cannot be
// heap-allocated, so the bridge cannot use `new T(x)` to copy results.
var classNoNew map[string]bool

// valueClasses maps fully qualified class name → *Class for classes/structs
// that appear as TypeValue (POD-style, passed by value). Used by the bridge
// generator to look up fields when serializing value-type returns into a
// nested ProtoWriter.
var valueClasses map[string]*apispec.Class

// buildValueClassMap populates valueClasses from the spec. Classes that are
// used as handles are still included because a class can simultaneously be
// referenced by value (e.g., a Result struct) and by pointer (handle type)
// — the generator picks the serialization path based on the TypeRef.Kind.
func buildValueClassMap(spec *apispec.APISpec) map[string]*apispec.Class {
	m := make(map[string]*apispec.Class, len(spec.Classes))
	for i := range spec.Classes {
		c := &spec.Classes[i]
		if c.QualName != "" {
			m[c.QualName] = c
		}
		if c.Name != "" && m[c.Name] == nil {
			m[c.Name] = c
		}
	}
	return m
}

// lookupValueClass resolves a TypeRef name (which may be short or fully
// qualified) to the matching class entry in valueClasses.
func lookupValueClass(name string) *apispec.Class {
	if valueClasses == nil {
		return nil
	}
	if c, ok := valueClasses[name]; ok {
		return c
	}
	// Strip leading const/volatile and trailing pointer/reference markers
	n := strings.TrimSpace(name)
	n = strings.TrimPrefix(n, "const ")
	n = strings.TrimSpace(strings.TrimRight(n, "*& "))
	if c, ok := valueClasses[n]; ok {
		return c
	}
	// Try resolving short name via classQualNames
	if q, ok := classQualNames[n]; ok {
		if c, ok := valueClasses[q]; ok {
			return c
		}
	}
	return nil
}

// isBridgeableFunction checks if a function can be included in the bridge.
// Requires: defined in project source AND all params/return are usable types.
// Both input and output params must be instantiable (we declare them as locals
// in the bridge dispatch body, which requires a default constructor).
func isBridgeableFunction(fn apispec.Function) bool {
	if !isProjectSource(fn.SourceFile) {
		return false
	}
	if !isReturnableType(fn.ReturnType) {
		return false
	}
	for _, p := range fn.Params {
		if !isUsableType(p.Type) {
			return false
		}
	}
	return true
}

// isInstantiableTypeForLocal returns true if a value of this type can be
// declared as a local variable in the bridge. For value/handle types stored
// by value, this requires a default constructor. Handle types stored as
// uint64_t (non-pointer, non-ref) are always instantiable in the bridge
// since we declare them as uint64_t, not the actual C++ type.
func isInstantiableTypeForLocal(ref apispec.TypeRef) bool {
	// All handle types are stored as uint64_t in the bridge (see
	// cppLocalType), regardless of whether the original C++ type is
	// by-value, by-pointer, or by-reference. Default-constructing a
	// uint64_t always works.
	if ref.Kind == apispec.TypeHandle {
		return true
	}
	return isInstantiableType(ref)
}

// isReturnableType returns true if a value of this type can be returned
// and serialized by the bridge. All types are returnable — the bridge
// generates the appropriate pattern (pointer cast, .release(), std::move,
// heap copy) for each case.
func isReturnableType(ref apispec.TypeRef) bool {
	// Primitive pointers (int*, bool*) have ambiguous ownership
	if ref.Kind == apispec.TypePrimitive && ref.IsPointer {
		return false
	}
	return isUsableType(ref)
}

// isBridgeableClass checks if a class should be exposed as a handle service.
// Requires: defined in project source, not abstract, has proper namespace,
// and has an accessible destructor (so Free RPC can delete instances).
func isBridgeableClass(c *apispec.Class) bool {
	includedExternal := isIncludedExternalClass(c.QualName)
	if !isProjectSource(c.SourceFile) {
		// External classes are bridgeable when:
		//   - explicitly opted in via bridge.IncludeExternalClasses
		//     (legacy escape hatch, still honoured), OR
		//   - listed in bridge.ExternalTypes -- the same class also
		//     appears as an allowed parameter / return type, so its
		//     constructors and methods should be reachable without a
		//     parallel config knob.
		if !includedExternal && !isAllowedExternalType(c.QualName) {
			return false
		}
	}
	// Abstract classes ARE bridgeable — they provide inherited methods
	// (e.g., ResolvedFunctionCallBase::function()) that concrete subclass
	// handles need to access via polymorphic dispatch. Constructors are
	// filtered separately (proto.go and bridge.go skip ctors for abstract).
	// Classes without a namespace (global scope) may lose namespace info
	// during clang AST parsing; they're unreliable for bridge generation.
	if c.Namespace == "" {
		return false
	}
	// Skip internal implementation-detail classes (e.g.,
	// `mylib_base::internal_associative_view::iterator`) whose types
	// may not be directly usable outside their containing template class.
	if strings.Contains(c.Namespace, "internal_") {
		return false
	}
	// Skip classes from disabled libraries — unless the user
	// explicitly opted this class in via IncludeExternalClasses
	// or it appears in bridge.ExternalTypes, in which case the
	// library disable doesn't apply.
	lib := classifyLibrary(c.SourceFile)
	if !isLibraryEnabled(lib) && !includedExternal && !isAllowedExternalType(c.QualName) {
		return false
	}
	// Skip classes defined in headers excluded from the bridge's include
	// list (e.g., headers with typedef redefinitions). Without the header,
	// the class is an incomplete type and member access fails.
	if c.SourceFile != "" {
		for skip := range skipBridgeHeadersMap {
			if strings.HasSuffix(c.SourceFile, "/"+skip) || c.SourceFile == skip {
				return false
			}
		}
	}
	if skipBridgeClassesMap[c.QualName] {
		return false
	}
	// Classes without a public destructor are still bridgeable — the
	// owning factory (analogous to googlesql::TypeFactory, which owns
	// every googlesql::ArrayType / StructType / EnumType) retains
	// lifetime, and Go-side code only ever holds a borrowed handle.
	// The Free RPC is skipped for such classes in writeHandleService
	// (see !c.HasPublicDtor handling there).
	if c.HasDeletedOperatorNew {
		// Arena-only types (e.g., protobuf messages) can't be new'd;
		// bridge can't heap-allocate them for return values.
		return false
	}
	return true
}

// buildClassQualNameMap creates a map from short class name to qualified name.
// If multiple classes share the same short name, the first one wins (ambiguous).
func buildClassQualNameMap(spec *apispec.APISpec) map[string]string {
	m := make(map[string]string)
	// Track multiplicity to detect ambiguous short names across classes AND enums.
	// A short name is ambiguous if any of these is true:
	// - Multiple classes share the short name
	// - Multiple enums share the short name
	// - A class and an enum share the short name
	count := make(map[string]int)
	ambiguousShortNames = make(map[string]bool)
	seen := make(map[string]bool)
	add := func(shortName, qualName string) {
		if shortName == "" || qualName == "" || shortName == qualName {
			return
		}
		key := shortName + "|" + qualName
		if seen[key] {
			return
		}
		seen[key] = true
		count[shortName]++
		if count[shortName] > 1 {
			ambiguousShortNames[shortName] = true
		}
		if existing, exists := m[shortName]; !exists {
			m[shortName] = qualName
		} else if specNamespace != "" {
			// Prefer the mapping in the project's top-level namespace
			// (e.g., ns::Column over ns::reflection::Column)
			// because top-level names are more likely to be the intended
			// resolution for unqualified template arguments.
			if strings.HasPrefix(qualName, specNamespace+"::") &&
				!strings.Contains(qualName[len(specNamespace)+2:], "::") &&
				strings.Contains(existing[len(specNamespace)+2:], "::") {
				m[shortName] = qualName
			}
		}
	}
	for _, c := range spec.Classes {
		add(c.Name, c.QualName)
	}
	for _, e := range spec.Enums {
		add(e.Name, e.QualName)
	}
	return m
}

// buildClassSourceFileMap creates a map from qualified class name → source file path.
func buildClassSourceFileMap(spec *apispec.APISpec) map[string]string {
	m := make(map[string]string)
	for _, c := range spec.Classes {
		if c.QualName != "" {
			m[c.QualName] = c.SourceFile
		}
	}
	for _, e := range spec.Enums {
		if e.QualName != "" {
			m[e.QualName] = e.SourceFile
		}
	}
	return m
}

// buildClassAbstractMap creates a map from qualified class name → is_abstract flag.
func buildClassAbstractMap(spec *apispec.APISpec) map[string]bool {
	m := make(map[string]bool)
	for _, c := range spec.Classes {
		if c.QualName != "" && c.IsAbstract {
			m[c.QualName] = true
		}
	}
	return m
}

// buildClassNoDefaultCtorMap creates a map from qualified class name → true
// if the class lacks an accessible default constructor, or has reference-
// type fields that prevent value-initialization (T ref_member; cannot be
// default-initialized).
func buildClassNoDefaultCtorMap(spec *apispec.APISpec) map[string]bool {
	m := make(map[string]bool)
	for _, c := range spec.Classes {
		if c.QualName == "" {
			continue
		}
		if !c.HasPublicDefaultCtor {
			m[c.QualName] = true
			continue
		}
		// Classes with reference-type fields can't be default-initialized
		// even if they declare a default constructor, because the reference
		// member must be bound at construction time.
		for _, f := range c.Fields {
			if f.Type.IsRef {
				m[c.QualName] = true
				break
			}
		}
	}
	return m
}

// buildClassDeletedCopyMap creates a map from qualified class name → true
// if the class cannot be copied by value. This covers two cases:
//
//  1. Classes with an explicitly-deleted copy constructor (recorded by
//     the parser as HasDeletedCopyCtor).
//  2. Classes whose copy constructor is implicitly deleted because they
//     contain a non-copyable member — most commonly a std::unique_ptr or
//     a field whose own type is non-copyable. The parser doesn't record
//     "implicit delete" directly, so we approximate by walking each
//     class's fields for smart pointer types, then propagating the
//     non-copyable flag through containment.
//
// This matters at bridge generation time because any function that takes
// a non-copyable type by value cannot be called directly; the bridge's
// `*reinterpret_cast<T*>(...)` dereference would trigger the deleted
// copy constructor. Functions flagged through this map are filtered out
// by isBridgeableFunction / isUsableType.
func buildClassDeletedCopyMap(spec *apispec.APISpec) map[string]bool {
	m := make(map[string]bool)
	// Index by both qual name and short name so we can look up fields
	// whose type is recorded with only a short name.
	classByName := make(map[string]*apispec.Class, len(spec.Classes))
	for i := range spec.Classes {
		c := &spec.Classes[i]
		if c.QualName != "" {
			classByName[c.QualName] = c
		}
		if c.Name != "" {
			if _, exists := classByName[c.Name]; !exists {
				classByName[c.Name] = c
			}
		}
	}

	// Seed with explicitly-deleted copy ctors.
	for _, c := range spec.Classes {
		if c.QualName != "" && c.HasDeletedCopyCtor {
			m[c.QualName] = true
		}
	}
	// Note: we intentionally do NOT apply a blanket "opaque class (private
	// fields, no public fields) = non-copyable" heuristic here. Types
	// like absl::Status have private-only state but ARE copyable;
	// flagging them would block many APIs.
	// Instead, we rely on the field-type walk below to detect classes
	// that contain known-non-copyable members (unique_ptr etc.). Classes
	// whose copy ctor is implicitly deleted for OTHER reasons (e.g.,
	// containing an absl::Mutex) will produce a compile error that the
	// user can address via the skip mechanism.

	// A class is non-copyable if any of its fields is non-copyable. We
	// iterate to a fixed point because A containing B containing C where
	// C is non-copyable should propagate all the way up.
	fieldIsNonCopyable := func(f apispec.Field) bool {
		t := f.Type
		if isSmartPointerType(t.QualType) || isSmartPointerType(t.Name) {
			return true
		}
		// A pointer or reference to a non-copyable type is still copyable
		// (the pointer itself is a scalar). Only fully-embedded non-
		// copyable subobjects delete a class's implicit copy.
		if t.IsPointer || t.IsRef {
			return false
		}
		// Recurse into the field's class type, if known.
		lookupName := t.Name
		if lookupName == "" {
			lookupName = t.QualType
		}
		if lookupName == "" {
			return false
		}
		if m[lookupName] {
			return true
		}
		if c, ok := classByName[lookupName]; ok && c.QualName != "" && m[c.QualName] {
			return true
		}
		return false
	}

	for changed := true; changed; {
		changed = false
		for i := range spec.Classes {
			c := &spec.Classes[i]
			if c.QualName == "" || m[c.QualName] {
				continue
			}
			for _, f := range c.Fields {
				if fieldIsNonCopyable(f) {
					m[c.QualName] = true
					changed = true
					break
				}
			}
		}
	}

	// Previously: a blanket heuristic marked every opaque class
	// (HasPrivateFields=true, 0 public fields) as non-copyable. That
	// was too aggressive — classes like FunctionArgumentType have
	// private-only state but explicitly declare
	// `C(const C&) = default;`, so they ARE copyable. Flagging them
	// blocked primary constructors like
	// FunctionSignature(FunctionArgumentType, FunctionArgumentTypeList, ...)
	// that pass such classes by value. We now rely only on
	// HasDeletedCopyCtor (clang-explicit) plus the field-type walk
	// above to flag non-copyable classes.
	return m
}

// buildClassPrivateDtorMap creates a map from qualified class name → true
// if the class's destructor is not publicly accessible.
func buildClassPrivateDtorMap(spec *apispec.APISpec) map[string]bool {
	m := make(map[string]bool)
	for _, c := range spec.Classes {
		if c.QualName != "" && !c.HasPublicDtor {
			m[c.QualName] = true
		}
	}
	return m
}

// buildClassNoNewMap creates a map from qualified class name → true
// if the class's operator new is deleted (arena-only allocation).
// Propagates the flag from base classes to derived classes: if any base
// has deleted operator new, the derived class inherits that restriction.
func buildClassNoNewMap(spec *apispec.APISpec) map[string]bool {
	m := make(map[string]bool)
	// Direct flags first
	byQualName := make(map[string]*apispec.Class)
	for i := range spec.Classes {
		c := &spec.Classes[i]
		byQualName[c.QualName] = c
		if c.HasDeletedOperatorNew {
			m[c.QualName] = true
		}
	}
	// Propagate through inheritance: a class inherits a base's deleted
	// operator new unless it overrides it (which we can't detect reliably).
	// Assume inheritance: if any ancestor has deleted operator new, mark.
	var check func(name string, visited map[string]bool) bool
	check = func(name string, visited map[string]bool) bool {
		if visited[name] {
			return false
		}
		visited[name] = true
		if m[name] {
			return true
		}
		c, ok := byQualName[name]
		if !ok {
			return false
		}
		for _, parent := range c.Parents {
			if check(parent, visited) {
				return true
			}
		}
		if c.Parent != "" && check(c.Parent, visited) {
			return true
		}
		return false
	}
	for _, c := range spec.Classes {
		if m[c.QualName] {
			continue
		}
		if check(c.QualName, make(map[string]bool)) {
			m[c.QualName] = true
		}
	}
	return m
}

// typeRefFromTemplateArg builds a minimal TypeRef from a template
// argument spelling (e.g. "const StructType::StructField",
// "const Column *"). Used to route Span<T> param parsing through the
// same code path as Vector<T>.
func typeRefFromTemplateArg(arg string) apispec.TypeRef {
	s := strings.TrimSpace(arg)
	// Drop trailing const ("Foo* const", "Foo const", "Foo*const") —
	// whatever this marks, the bridge stores a non-const local and
	// lets the callee re-apply constness via Span<const T>.
	if strings.HasSuffix(s, " const") {
		s = strings.TrimSpace(strings.TrimSuffix(s, " const"))
	}
	if strings.HasSuffix(s, "*const") {
		s = strings.TrimSpace(strings.TrimSuffix(s, "const"))
	}
	isConst := false
	if strings.HasPrefix(s, "const ") {
		isConst = true
		s = strings.TrimPrefix(s, "const ")
		s = strings.TrimSpace(s)
	}
	isPointer := false
	if strings.HasSuffix(s, "*") {
		isPointer = true
		s = strings.TrimSpace(strings.TrimSuffix(s, "*"))
	}
	ref := apispec.TypeRef{
		Name:      s,
		QualType:  arg,
		IsConst:   isConst,
		IsPointer: isPointer,
	}
	// Classify via existing class / enum tables so downstream readers
	// know whether to push_back a scalar, a handle pointer, or a
	// value-type emplace_back body.
	resolved := resolveTypeName(s)
	if isPointer {
		ref.Kind = apispec.TypeHandle
		return ref
	}
	if _, ok := valueClasses[resolved]; ok {
		ref.Kind = apispec.TypeValue
		return ref
	}
	if enumQualNames[resolved] {
		ref.Kind = apispec.TypeEnum
		return ref
	}
	if s == "int" || s == "int32_t" || s == "int64_t" || s == "bool" || s == "double" || s == "float" ||
		s == "uint32_t" || s == "uint64_t" || s == "short" || s == "char" {
		ref.Kind = apispec.TypePrimitive
		return ref
	}
	if s == "std::string" || s == "string" {
		ref.Kind = apispec.TypeString
		return ref
	}
	// Default fallback: treat as value so readVectorElementExpr
	// produces either the per-class parser or a skip() comment.
	ref.Kind = apispec.TypeValue
	return ref
}

// namespaceOf returns the leading namespace segment of a qualified name
// ("ns::Class" -> "ns", "ns::sub::Class" -> "ns"). Empty when the name
// has no namespace.
func namespaceOf(qual string) string {
	if i := strings.Index(qual, "::"); i > 0 {
		return qual[:i]
	}
	return ""
}

// resolveTypeName resolves a possibly-short type name to its fully qualified form
// using the class lookup map.
func resolveTypeName(name string) string {
	return resolveTypeNameInContext(name, "")
}

// resolveTypeNameInContext resolves a type name with optional class context.
// When classContext is non-empty (the qualified name of the enclosing class),
// the function applies class-scope resolution rules:
//   - If the short name matches the enclosing class's short name, resolve to
//     the enclosing class (e.g., "Column" inside ns::reflection::Column
//     methods resolves to ns::reflection::Column).
//   - If the short name is ambiguous (appears in multiple namespaces) and the
//     default resolution points to a handle class while the original type was
//     declared as a value type, try the nested type form
//     (e.g., "Column" inside ns::TVFRelation → ns::TVFRelation::Column).
//
// The parser's postProcessQualifyShortNames pass rewrites unqualified type
// spellings to their FQDN at parse time using clang's namespace / class
// scope information, so by the time names reach this function they are
// almost always already qualified. The remaining work here is the
// nested-type alias path (clang sometimes records `using X = ::ns::X;`
// as the enclosing class spelling) and the "name equals enclosing class"
// case.
func resolveTypeNameInContext(name, classContext string) string {
	if classQualNames == nil {
		return name
	}
	// If the name contains template brackets, qualify the arguments
	// recursively. E.g., `std::unique_ptr<ParserOutput>` becomes
	// `std::unique_ptr<ns::ParserOutput>`. This must run BEFORE
	// the `::` early return because `absl::Span<const NumericValue>`
	// contains `::` but still has an unqualified template argument.
	if strings.Contains(name, "<") {
		return qualifyTemplateArgsInContext(name, classContext)
	}
	// Already qualified (contains ::)
	if strings.Contains(name, "::") {
		// Clang records nested-type aliases (`using X = ::ns::X;`) as
		// the enclosing class scope, e.g. "StructType::StructField"
		// when the real class is `ns::StructField`. When the spelling
		// isn't a known class itself, try the short suffix as a
		// fallback IFF the resolved class's qualified name preserves
		// the enclosing scope's first component (prevents conflating
		// "absl::Status" with an unrelated "ns::Status" class).
		if _, knownFull := classSourceFiles[name]; knownFull {
			return name
		}
		if idx := strings.LastIndex(name, "::"); idx >= 0 {
			short := name[idx+2:]
			if qual, ok := classQualNames[short]; ok && !ambiguousShortNames[short] {
				prefix := name[:idx] // e.g. "StructType" or "absl"
				// The alias candidate matches when the resolved class
				// qual-name shares a top-level namespace with the
				// prefix's leading component, or when the prefix
				// itself is a known class inside the same namespace.
				// "absl::Status" -> prefix "absl" isn't a class, so
				// this branch must NOT override to an unrelated
				// "ns::Status" class.
				prefixFirst := prefix
				if pi := strings.Index(prefix, "::"); pi >= 0 {
					prefixFirst = prefix[:pi]
				}
				if _, prefixIsClass := classQualNames[prefixFirst]; prefixIsClass {
					return qual
				}
				// Class-scope nested alias case: the prefix is a
				// known class and the short name is one of its
				// nested typedefs (e.g. StructType::StructField ->
				// googlesql::StructField). Accept when the resolved
				// class lives in the same namespace as the prefix
				// class.
				if prefixQual, ok := classQualNames[prefix]; ok {
					if ns := namespaceOf(prefixQual); ns != "" && namespaceOf(qual) == ns {
						return qual
					}
				}
			}
		}
		return name
	}
	// Context-aware resolution: check if the name matches the enclosing
	// class's own short name.
	if classContext != "" {
		classShort := classContext
		if idx := strings.LastIndex(classContext, "::"); idx >= 0 {
			classShort = classContext[idx+2:]
		}
		if name == classShort {
			return classContext
		}
	}
	if qual, ok := classQualNames[name]; ok {
		return qual
	}
	return name
}

// resolveTypeNameInContextForValue is like resolveTypeNameInContext but
// additionally handles nested-type disambiguation for value types. When the
// default classQualNames resolution maps a value-kind type to a known handle
// class, the resolution is likely wrong (C++ nested type took priority in the
// original scope). In that case, try classContext::name.
func resolveTypeNameInContextForValue(name, classContext string) string {
	if classQualNames == nil {
		return name
	}
	if strings.Contains(name, "<") {
		return qualifyTemplateArgsInContext(name, classContext)
	}
	if strings.Contains(name, "::") {
		return name
	}
	// Context-aware: same-class resolution
	if classContext != "" {
		classShort := classContext
		if idx := strings.LastIndex(classContext, "::"); idx >= 0 {
			classShort = classContext[idx+2:]
		}
		if name == classShort {
			return classContext
		}
		// If the name is ambiguous and the default resolution points to a
		// handle class, this value-type name likely refers to a nested type
		// of the enclosing class (C++ nested types shadow namespace-level
		// names in class scope). Handle classes are typically used via
		// pointer/reference; when a param declares the type as a value,
		// the mismatch indicates C++ name lookup resolved to a different
		// (nested) type.
		if ambiguousShortNames != nil && ambiguousShortNames[name] {
			if qual, ok := classQualNames[name]; ok {
				if (classAbstract != nil && classAbstract[qual]) ||
					(classNoDefaultCtor != nil && classNoDefaultCtor[qual]) {
					// Only use nested type if it actually exists in the class hierarchy
					nested := classContext + "::" + name
					if classSourceFiles != nil {
						if _, exists := classSourceFiles[nested]; exists {
							return nested
						}
					}
				}
			}
		}
	}
	if qual, ok := classQualNames[name]; ok {
		return qual
	}
	return name
}

// qualifyTemplateArgsInContext is like qualifyTemplateArgs but passes class
// context through to qualifySingleArgInContext.
func qualifyTemplateArgsInContext(name, classContext string) string {
	if classContext == "" {
		return qualifyTemplateArgs(name)
	}
	idx := strings.Index(name, "<")
	if idx < 0 {
		return name
	}
	depth := 0
	end := -1
scanEnd:
	for i := idx; i < len(name); i++ {
		switch name[i] {
		case '<':
			depth++
		case '>':
			depth--
			if depth == 0 {
				end = i
				break scanEnd
			}
		}
	}
	if end < 0 {
		return name
	}
	prefix := name[:idx+1]
	argsStr := name[idx+1 : end]
	suffix := name[end:]
	args := strings.Split(argsStr, ",")
	var resolved []string
	for _, arg := range args {
		resolved = append(resolved, qualifySingleArgInContext(strings.TrimSpace(arg), classContext))
	}
	return prefix + strings.Join(resolved, ", ") + suffix
}

// qualifySingleArgInContext is like qualifySingleArg but uses context-aware
// resolution for value-type nested names.
func qualifySingleArgInContext(arg, classContext string) string {
	if classContext == "" {
		return qualifySingleArg(arg)
	}
	if strings.Contains(arg, "<") {
		return qualifyTemplateArgsInContext(arg, classContext)
	}
	constPrefix := ""
	if strings.HasPrefix(arg, "const ") {
		constPrefix = "const "
		arg = strings.TrimPrefix(arg, "const ")
	}
	ptrSuffix := ""
	for {
		arg = strings.TrimSpace(arg)
		if strings.HasSuffix(arg, " const") {
			ptrSuffix = " const" + ptrSuffix
			arg = strings.TrimSuffix(arg, " const")
		} else if strings.HasSuffix(arg, "*const") {
			ptrSuffix = "*const" + ptrSuffix
			arg = strings.TrimSuffix(arg, "*const")
		} else if strings.HasSuffix(arg, "* const") {
			ptrSuffix = "* const" + ptrSuffix
			arg = strings.TrimSuffix(arg, "* const")
		} else if strings.HasSuffix(arg, "*") {
			ptrSuffix = "*" + ptrSuffix
			arg = strings.TrimSuffix(arg, "*")
		} else if strings.HasSuffix(arg, "&") {
			ptrSuffix = "&" + ptrSuffix
			arg = strings.TrimSuffix(arg, "&")
		} else {
			break
		}
	}
	arg = strings.TrimSpace(arg)
	// Use value-aware context resolution (handles nested type disambiguation)
	resolved := resolveTypeNameInContextForValue(arg, classContext)
	return constPrefix + resolved + ptrSuffix
}

// qualifyTemplateArgs qualifies type names inside template brackets.
// Given `std::unique_ptr<const ParserOutput>`, resolves each template
// argument through classQualNames to produce
// `std::unique_ptr<const ns::ParserOutput>`.
func qualifyTemplateArgs(name string) string {
	idx := strings.Index(name, "<")
	if idx < 0 {
		return name
	}
	// Find matching >
	depth := 0
	end := -1
scanEnd:
	for i := idx; i < len(name); i++ {
		switch name[i] {
		case '<':
			depth++
		case '>':
			depth--
			if depth == 0 {
				end = i
				break scanEnd
			}
		}
	}
	if end < 0 {
		return name // malformed
	}

	prefix := name[:idx+1]         // "std::unique_ptr<"
	argsStr := name[idx+1 : end]   // "const ParserOutput"
	suffix := name[end:]            // ">"

	// Split by comma for multi-arg templates
	args := strings.Split(argsStr, ",")
	var resolved []string
	for _, arg := range args {
		resolved = append(resolved, qualifySingleArg(strings.TrimSpace(arg)))
	}
	return prefix + strings.Join(resolved, ", ") + suffix
}

// qualifySingleArg qualifies a single template argument, preserving
// const/volatile/pointer/reference qualifiers.
func qualifySingleArg(arg string) string {
	// Recurse into nested templates
	if strings.Contains(arg, "<") {
		return qualifyTemplateArgs(arg)
	}
	// Strip qualifiers, resolve, put back.
	// Handle patterns like "const ASTFoo *const" (const pointer to const T).
	constPrefix := ""
	if strings.HasPrefix(arg, "const ") {
		constPrefix = "const "
		arg = strings.TrimPrefix(arg, "const ")
	}
	ptrSuffix := ""
	// Iteratively strip trailing const, *, & qualifiers.
	// Handles patterns like `T *const` (const pointer), `T *const *`,
	// `T &`, `T const` etc.
	for {
		arg = strings.TrimSpace(arg)
		if strings.HasSuffix(arg, " const") {
			ptrSuffix = " const" + ptrSuffix
			arg = strings.TrimSuffix(arg, " const")
		} else if strings.HasSuffix(arg, "*const") {
			ptrSuffix = "*const" + ptrSuffix
			arg = strings.TrimSuffix(arg, "*const")
		} else if strings.HasSuffix(arg, "* const") {
			ptrSuffix = "* const" + ptrSuffix
			arg = strings.TrimSuffix(arg, "* const")
		} else if strings.HasSuffix(arg, "*") {
			ptrSuffix = "*" + ptrSuffix
			arg = strings.TrimSuffix(arg, "*")
		} else if strings.HasSuffix(arg, "&") {
			ptrSuffix = "&" + ptrSuffix
			arg = strings.TrimSuffix(arg, "&")
		} else {
			break
		}
	}
	arg = strings.TrimSpace(arg)

	// If the name contains `::`, the leftmost segment might still be
	// unqualified (e.g. `Value::Property` → `ns::Value::Property`).
	// Try resolving the prefix before the first `::`.
	if strings.Contains(arg, "::") {
		parts := strings.SplitN(arg, "::", 2)
		prefix := strings.TrimSpace(parts[0])
		if classQualNames != nil {
			if qual, ok := classQualNames[prefix]; ok {
				return constPrefix + qual + "::" + parts[1] + ptrSuffix
			}
		}
		return constPrefix + arg + ptrSuffix
	}
	if classQualNames != nil {
		if qual, ok := classQualNames[arg]; ok {
			return constPrefix + qual + ptrSuffix
		}
	}
	// If not found in classQualNames, try the spec's default namespace
	// as a fallback — but only for names that look like class/type names
	// (start with uppercase). Primitives (int64_t etc.) and external
	// library types (RE2 etc.) should not be prefixed.
	// Namespace fallback for template arguments only. If the unresolved
	// name starts with an uppercase letter (likely a class name) and is
	// not a primitive or known external type, try prepending the project's
	// default namespace. This handles types like FunctionSignature that
	// are not in the api-spec but are used in template parameters.
	if specNamespace != "" && !strings.Contains(arg, "::") {
		// Only uppercase-start names that aren't primitives
		low := strings.ToLower(arg)
		isPrim := low == "bool" || low == "int" || low == "float" || low == "double" ||
			low == "char" || low == "void" || low == "short" || low == "long" ||
			strings.HasSuffix(low, "_t") // int32_t, size_t, etc.
		// Only apply to names with 3+ chars (short names like RE2 are
		// likely external library types, not project classes)
		if !isPrim && arg[0] >= 'A' && arg[0] <= 'Z' && !isAllowedExternalType(arg) {
			return constPrefix + specNamespace + "::" + arg + ptrSuffix
		}
	}
	return constPrefix + arg + ptrSuffix
}

// isHandleByPointer returns true if a handle TypeRef is used via pointer/reference.
// Handle types returned by value need heap allocation in the bridge.
func isHandleByPointer(ref apispec.TypeRef) bool {
	return ref.IsPointer || ref.IsRef
}

// cppTypeName returns the best C++ type name from a TypeRef.
// Uses QualType if available, otherwise resolves short names via the spec's class map.
func cppTypeName(ref apispec.TypeRef) string {
	// For primitives, use our canonical mapping
	if ref.Kind == apispec.TypePrimitive {
		return cppPrimitiveType(ref.Name)
	}
	// For string types, always use std::string for locals
	if ref.Kind == apispec.TypeString {
		return "std::string"
	}
	// For enum types, prefer ref.Name because postProcessEnumTypes rewrites
	// ambiguous short names to their fully qualified form. QualType is the
	// raw clang-recorded spelling (often just the short name) and would
	// collide with classQualNames-based resolution when a class shares the
	// same short name as the enum.
	if ref.Kind == apispec.TypeEnum {
		if ref.Name != "" {
			return ref.Name
		}
	}
	// Use QualType if available (has full qualification like "const ns_base::ExactFloat &")
	name := ref.QualType
	if name == "" {
		name = ref.Name
	}
	if name == "" {
		return "/* unknown type */"
	}
	name = stripCppTypeQualifiers(name)
	// Resolve short names to fully qualified names
	return resolveTypeName(name)
}

// stripCppTypeQualifiers reduces a clang-spelled C++ type expression
// to its bare class/typedef identifier by repeatedly peeling off:
//
//   - trailing pointer / reference markers (`*`, `&`)
//   - leading cv-qualifiers (`const`, `volatile`)
//   - leading elaborated-type-specifiers (`class`, `struct`, `union`,
//     `enum`)
//
// All four are part of standard C++ syntax and apply uniformly across
// any header source, so the helper is library-agnostic. The
// elaborated-type-specifier strip in particular matters because clang
// occasionally re-emits the keyword in `qual_type` (e.g.
// `const class Type *` rather than `const Type *`), and downstream
// resolution against `classQualNames` keys on the bare name only.
func stripCppTypeQualifiers(name string) string {
	name = strings.TrimSpace(name)
	for {
		prev := name
		name = strings.TrimSpace(name)
		name = strings.TrimSuffix(name, "*")
		name = strings.TrimSuffix(name, "&")
		name = strings.TrimSpace(name)
		name = strings.TrimPrefix(name, "const ")
		name = strings.TrimPrefix(name, "volatile ")
		name = strings.TrimPrefix(name, "class ")
		name = strings.TrimPrefix(name, "struct ")
		name = strings.TrimPrefix(name, "union ")
		name = strings.TrimPrefix(name, "enum ")
		// Leading `::` is the C++ global-namespace anchor: `::ns::Foo`
		// names the exact same type as `ns::Foo`, but
		// `classSourceFiles` / `classQualNames` are keyed without the
		// anchor. Strip it here so downstream resolution lines up;
		// otherwise clang's occasional double-colon spellings (e.g.
		// `const ::googlesql::ResolvedCreateTableFunctionStmt *`)
		// silently filter the method out at isUsableType time.
		name = strings.TrimPrefix(name, "::")
		name = strings.TrimSpace(name)
		if name == prev {
			break
		}
	}
	return name
}

// cppTypeNameInContext returns the C++ type name with class-context-aware resolution.
// Used when generating code for methods/constructors of a specific class.
func cppTypeNameInContext(ref apispec.TypeRef, classContext string) string {
	if classContext == "" {
		return cppTypeName(ref)
	}
	if ref.Kind == apispec.TypePrimitive {
		return cppPrimitiveType(ref.Name)
	}
	if ref.Kind == apispec.TypeString {
		return "std::string"
	}
	if ref.Kind == apispec.TypeEnum {
		if ref.Name != "" {
			return ref.Name
		}
	}
	name := ref.QualType
	if name == "" {
		name = ref.Name
	}
	if name == "" {
		return "/* unknown type */"
	}
	name = stripCppTypeQualifiers(name)
	return resolveTypeNameInContext(name, classContext)
}

// cppLocalTypeInContext returns the C++ type for a local variable declaration
// with class-context-aware resolution for value types.
func cppLocalTypeInContext(ref apispec.TypeRef, classContext string) string {
	if classContext == "" {
		return cppLocalType(ref)
	}
	switch ref.Kind {
	case apispec.TypePrimitive:
		return cppPrimitiveType(ref.Name)
	case apispec.TypeString:
		if strings.Contains(ref.Name, "string_view") || strings.Contains(ref.QualType, "string_view") {
			return "std::string"
		}
		return "std::string"
	case apispec.TypeHandle:
		// Set-like containers: declare the container as the local
		// (mirrors cppLocalType). Class context only affects value
		// types, so we delegate the recognition to setLikeContainerInfo
		// which keys on the ref's spelling.
		if container, _, ok := setLikeContainerInfo(ref); ok {
			return container
		}
		return "uint64_t"
	case apispec.TypeEnum:
		return cppTypeNameInContext(ref, classContext)
	case apispec.TypeVector:
		if ref.Inner != nil {
			if ref.Inner.Kind == apispec.TypeHandle {
				inner := resolveTypeNameInContext(cppTypeNameInContext(*ref.Inner, classContext), classContext)
				if ref.Inner.IsPointer {
					constQual := ""
					if ref.Inner.IsConst {
						constQual = "const "
					}
					return "std::vector<" + constQual + inner + "*>"
				}
				return "std::vector<" + inner + ">"
			}
			if ref.Inner.Kind == apispec.TypeValue {
				resolved := resolveTypeNameInContext(cppTypeNameInContext(*ref.Inner, classContext), classContext)
				return "std::vector<" + resolved + ">"
			}
			inner := cppLocalTypeInContext(*ref.Inner, classContext)
			return "std::vector<" + inner + ">"
		}
		return "std::vector<uint8_t>"
	case apispec.TypeValue:
		// Non-owning view types (configured via BridgeConfig.ValueViewTypes,
		// e.g. absl::Span) are materialised as std::vector<Elem>; the
		// view's implicit ctor binds at the callsite.
		if matchesValueViewType(ref.QualType) {
			if elem := extractTemplateArgFromQualType(ref.QualType); elem != "" {
				return "std::vector<" + spanVectorElement(elem, classContext) + ">"
			}
		}
		if ref.QualType != "" && isAllowedExternalType(ref.Name) {
			return resolveTypeNameInContextForValue(ref.QualType, classContext)
		}
		return cppTypeNameInContextForValue(ref, classContext)
	case apispec.TypeVoid:
		return "void"
	default:
		return "/* unknown */"
	}
}

// spanVectorElement normalises a Span<T> template argument spelling to
// a form that std::vector<T> accepts. std::vector forbids cv-qualified
// element types so both leading `const` and top-level ("pointer-const")
// qualifiers are stripped; pointee-const is preserved for pointer
// elements. Nested-alias names (e.g. StructType::StructField) are
// resolved to their real class name.
func spanVectorElement(arg, classContext string) string {
	s := strings.TrimSpace(arg)
	// Drop top-level const. Clang spells this variously depending on
	// placement: "Foo const" or "Foo* const" for pointer-level const,
	// and "*const" (no space) when clang omits whitespace after the
	// asterisk. All of these mean "this element is const"; vector
	// rejects each form.
	if strings.HasSuffix(s, " const") {
		s = strings.TrimSpace(strings.TrimSuffix(s, " const"))
	}
	if strings.HasSuffix(s, "*const") {
		s = strings.TrimSpace(strings.TrimSuffix(s, "const"))
	}
	leadingConst := false
	if strings.HasPrefix(s, "const ") {
		leadingConst = true
		s = strings.TrimSpace(strings.TrimPrefix(s, "const "))
	}
	// Pull the trailing pointer asterisks off so we can resolve the
	// pointee name independently.
	ptrs := ""
	for strings.HasSuffix(s, "*") {
		ptrs += "*"
		s = strings.TrimSpace(strings.TrimSuffix(s, "*"))
	}
	// Resolve nested-alias names to their real class form.
	s = resolveTypeNameInContext(s, classContext)
	if ptrs != "" {
		pointee := s
		if leadingConst {
			pointee = "const " + pointee
		}
		return pointee + ptrs
	}
	// By-value element: std::vector disallows cv-qualifiers on T.
	return s
}

// cppTypeNameInContextForValue resolves a value-type TypeRef with nested-type
// disambiguation (uses resolveTypeNameInContextForValue).
func cppTypeNameInContextForValue(ref apispec.TypeRef, classContext string) string {
	if ref.Kind == apispec.TypePrimitive {
		return cppPrimitiveType(ref.Name)
	}
	if ref.Kind == apispec.TypeString {
		return "std::string"
	}
	if ref.Kind == apispec.TypeEnum {
		if ref.Name != "" {
			return ref.Name
		}
	}
	name := ref.QualType
	if name == "" {
		name = ref.Name
	}
	if name == "" {
		return "/* unknown type */"
	}
	name = strings.TrimSpace(name)
	for {
		prev := name
		name = strings.TrimSpace(name)
		name = strings.TrimSuffix(name, "*")
		name = strings.TrimSuffix(name, "&")
		name = strings.TrimSpace(name)
		name = strings.TrimPrefix(name, "const ")
		name = strings.TrimPrefix(name, "volatile ")
		name = strings.TrimSpace(name)
		if name == prev {
			break
		}
	}
	return resolveTypeNameInContextForValue(name, classContext)
}

// cppLocalType returns the C++ type for a local variable declaration.
// setLikeContainerInfo recognises a parameter that the proto-schema
// side classifies as a set-like container (via parseMapType / the
// bridge config's SetLikeTypePrefixes) and returns the C++ container
// type plus the resolved element type to use on the bridge body
// side. Returns ok=false when ref is not a set-like container.
//
// clang reports `absl::flat_hash_set<const Foo *>` as `kind: handle`
// because the set is itself a class template instance — but the
// proto schema emits `repeated <Handle>` for the same parameter.
// The bridge body therefore needs to materialise the container from
// the wire instead of taking a single opaque handle. This helper
// keeps the recognition logic in one place so the local declaration,
// per-element read, and call-site argument all agree on the shape.
//
// The recognition is library-agnostic: any prefix listed in
// `BridgeConfig.SetLikeTypePrefixes` (or the std-library set-like
// templates that parseMapType always recognises) participates.
func setLikeContainerInfo(ref apispec.TypeRef) (containerType, elementType string, ok bool) {
	if ref.Kind != apispec.TypeHandle && ref.Kind != apispec.TypeValue {
		return "", "", false
	}
	elem, _, isMapOrSet := parseMapType(ref.Name)
	if !isMapOrSet {
		return "", "", false
	}
	// parseMapType returns elem != "" and val == "" for the set case;
	// elem != "" and val != "" for the map case. We only handle sets here.
	if elem == "" {
		return "", "", false
	}
	// Maps come back from parseMapType with both key and value populated.
	// Detect that by re-running with explicit set-prefix matching: if
	// the input doesn't match a set prefix, fall through.
	if !looksLikeSetTypeName(ref.Name) {
		return "", "", false
	}
	// Element type: extract pointer / const qualifiers and resolve the
	// base name to its fully-qualified form. Set elements are typically
	// `const T*` (the canonical "non-owning reference set" idiom).
	elemTrimmed := strings.TrimSpace(elem)
	hasConst := strings.HasPrefix(elemTrimmed, "const ")
	if hasConst {
		elemTrimmed = strings.TrimSpace(strings.TrimPrefix(elemTrimmed, "const "))
	}
	isPtr := strings.HasSuffix(elemTrimmed, "*")
	if isPtr {
		elemTrimmed = strings.TrimSpace(strings.TrimSuffix(elemTrimmed, "*"))
	}
	resolvedBase := resolveTypeName(elemTrimmed)
	if resolvedBase == "" {
		resolvedBase = elemTrimmed
	}
	rebuilt := resolvedBase
	if isPtr {
		rebuilt += "*"
	}
	if hasConst {
		rebuilt = "const " + rebuilt
	}
	containerPrefix := setContainerPrefix(ref.Name)
	if containerPrefix == "" {
		return "", "", false
	}
	return containerPrefix + "<" + rebuilt + ">", rebuilt, true
}

// setLikeCallExpr returns the C++ expression to pass `varName`
// (a local of a set-like container type) to the callee, adapting to
// how the callee declares the parameter. The local is always an
// lvalue of the materialised container, so:
//
//   - `T*` (pointer-to-container, the writable out-param idiom):
//     emit `&varName`. The callee mutates the set in place.
//   - `T&&` (rvalue-ref by-value sink): emit `std::move(varName)`
//     so move-only containers bind without falling back to a copy.
//   - `T` (by-value): same as rvalue-ref — the move-into-parameter
//     is always at least as cheap as a copy.
//   - `const T&` / `T&` (lvalue ref): bind by name. Const-ref is the
//     usual shape for read-only set-like inputs (e.g. labels in
//     SimpleGraphElementLabel).
//
// The helper is library-agnostic: any set-like spelling that
// `setLikeContainerInfo` accepts is handled the same way.
func setLikeCallExpr(p apispec.Param, varName string) string {
	if p.Type.IsPointer && !p.Type.IsRef {
		return "&" + varName
	}
	qt := strings.TrimSpace(p.Type.QualType)
	if strings.HasSuffix(qt, "&&") {
		return "std::move(" + varName + ")"
	}
	if !p.Type.IsRef {
		return "std::move(" + varName + ")"
	}
	return varName
}

// looksLikeSetTypeName reports whether name is a set-like container
// per the std-library defaults plus `BridgeConfig.SetLikeTypePrefixes`.
// Mirrors the prefix list parseMapType uses for sets (excluding
// map prefixes).
func looksLikeSetTypeName(name string) bool {
	cleaned := strings.TrimPrefix(name, "const ")
	cleaned = strings.TrimRight(cleaned, "* &")
	cleaned = strings.TrimSpace(cleaned)
	prefixes := []string{
		"std::unordered_set<",
		"unordered_set<",
		"std::set<",
		"set<",
	}
	for _, p := range bridgeConfig.SetLikeTypePrefixes {
		prefixes = append(prefixes, p+"<")
	}
	for _, p := range prefixes {
		if strings.HasPrefix(cleaned, p) {
			return true
		}
	}
	return false
}

// setContainerPrefix returns the C++ template prefix (without the
// trailing `<`) for a set-like type spelling. Used to reconstruct the
// container's local declaration after the element type has been
// resolved.
func setContainerPrefix(name string) string {
	cleaned := strings.TrimPrefix(name, "const ")
	cleaned = strings.TrimRight(cleaned, "* &")
	cleaned = strings.TrimSpace(cleaned)
	prefixes := []string{
		"std::unordered_set",
		"unordered_set",
		"std::set",
		"set",
	}
	prefixes = append(prefixes, bridgeConfig.SetLikeTypePrefixes...)
	// Longest match first so `std::unordered_set` wins over `set`.
	sort.SliceStable(prefixes, func(i, j int) bool {
		return len(prefixes[i]) > len(prefixes[j])
	})
	for _, p := range prefixes {
		if strings.HasPrefix(cleaned, p+"<") {
			return p
		}
	}
	return ""
}

func cppLocalType(ref apispec.TypeRef) string {
	switch ref.Kind {
	case apispec.TypePrimitive:
		return cppPrimitiveType(ref.Name)
	case apispec.TypeString:
		// Use the actual type if it's string_view
		if strings.Contains(ref.Name, "string_view") || strings.Contains(ref.QualType, "string_view") {
			return "std::string" // Still store as string, pass as needed
		}
		return "std::string"
	case apispec.TypeHandle:
		// Set-like containers (e.g. `absl::flat_hash_set<const T*>`)
		// declare the container as the local; the per-element wire
		// reader inserts into it. See setLikeContainerInfo.
		if container, _, ok := setLikeContainerInfo(ref); ok {
			return container
		}
		return "uint64_t" // stored as pointer value in bridge
	case apispec.TypeEnum:
		return cppTypeName(ref)
	case apispec.TypeVector:
		if ref.Inner != nil {
			// For vector<Handle*>/<const Handle*>, the element IS a
			// pointer so the local storage matches. For vector<Handle>
			// (post-promotion by-value elements), the local must match
			// the callee's declared type — use plain vector<Handle>.
			if ref.Inner.Kind == apispec.TypeHandle {
				inner := resolveTypeName(cppTypeName(*ref.Inner))
				if ref.Inner.IsPointer {
					constQual := ""
					if ref.Inner.IsConst {
						constQual = "const "
					}
					return "std::vector<" + constQual + inner + "*>"
				}
				return "std::vector<" + inner + ">"
			}
			if ref.Inner.Kind == apispec.TypeValue {
				// Nested value-type element names often arrive as
				// class-scoped aliases ("StructType::StructField"),
				// which don't exist as standalone identifiers. Resolve
				// to the real class qualification so std::vector picks
				// up a compilable type.
				resolved := resolveTypeName(cppTypeName(*ref.Inner))
				return "std::vector<" + resolved + ">"
			}
			inner := cppLocalType(*ref.Inner)
			return "std::vector<" + inner + ">"
		}
		return "std::vector<uint8_t>"
	case apispec.TypeValue:
		// Non-owning view types (configured via ValueViewTypes) are
		// backed by std::vector. See spanVectorElement for cv/pointer
		// normalisation (vectors don't accept cv-qualified values).
		if matchesValueViewType(ref.QualType) {
			if elem := extractTemplateArgFromQualType(ref.QualType); elem != "" {
				return "std::vector<" + spanVectorElement(elem, "") + ">"
			}
		}
		// For external value types (e.g., absl::Span<const Column*>),
		// use QualType with resolved template args. This avoids the
		// cppTypeName path which strips qualifiers that are part of the
		// external type's identity (e.g., stripping "const" from
		// "absl::Span<const Column *const>").
		if ref.QualType != "" && isAllowedExternalType(ref.Name) {
			return resolveTypeName(ref.QualType)
		}
		return cppTypeName(ref)
	case apispec.TypeVoid:
		return "void"
	default:
		return "/* unknown */"
	}
}

// cppReturnType returns the C++ type to use for capturing a function's return value.
// Uses `auto` for most types to avoid type mismatch issues (e.g., string_view vs string,
// handle by-value vs by-pointer, etc.)
func cppReturnType(ref apispec.TypeRef) string {
	switch ref.Kind {
	case apispec.TypePrimitive:
		return cppPrimitiveType(ref.Name)
	case apispec.TypeEnum:
		return cppTypeName(ref)
	default:
		// Reference returns: bind to auto& (lvalue ref) or auto&& (rvalue ref).
		if ref.IsRef {
			qt := strings.TrimSpace(ref.QualType)
			if strings.HasSuffix(qt, "&&") {
				// rvalue ref return (e.g., `T&&`): must use universal
				// reference `auto&&` to bind.
				return "auto&&"
			}
			if ref.IsConst {
				return "const auto&"
			}
			return "auto&"
		}
		return "auto"
	}
}

func cppPrimitiveType(name string) string {
	switch name {
	case "bool":
		return "bool"
	case "int", "int32_t":
		return "int32_t"
	case "unsigned int", "uint32_t":
		return "uint32_t"
	case "long", "long long", "int64_t", "ssize_t", "ptrdiff_t", "intptr_t":
		return "int64_t"
	case "unsigned long", "unsigned long long", "uint64_t", "size_t", "uintptr_t":
		return "uint64_t"
	case "short", "int16_t", "char", "int8_t", "signed char":
		return "int32_t"
	case "unsigned short", "uint16_t", "unsigned char", "uint8_t":
		return "uint32_t"
	case "float":
		return "float"
	case "double", "long double":
		return "double"
	default:
		return "int64_t"
	}
}

// cppParamType returns the C++ type for passing a parameter to a function.
// Unlike cppLocalType which stores as uint64_t for handles, this returns the actual C++ type.
func cppParamType(ref apispec.TypeRef) string {
	switch ref.Kind {
	case apispec.TypeHandle:
		typeName := cppTypeName(ref)
		constQual := ""
		if ref.IsConst {
			constQual = "const "
		}
		if ref.IsRef {
			return constQual + typeName + "&"
		}
		return constQual + typeName + "*"
	default:
		return cppLocalType(ref)
	}
}

// protoWireType returns the wire type for a given TypeRef.
// 0=varint, 1=fixed64, 2=length-delimited, 5=fixed32
func protoWireType(ref apispec.TypeRef) int {
	switch ref.Kind {
	case apispec.TypePrimitive:
		switch cppPrimitiveType(ref.Name) {
		case "float":
			return 5
		case "double":
			return 1
		default:
			return 0 // varint
		}
	case apispec.TypeEnum:
		return 0
	case apispec.TypeString, apispec.TypeHandle, apispec.TypeValue, apispec.TypeVector:
		return 2
	default:
		return 2
	}
}

// readExpr returns the C++ expression to read a field from a ProtoReader.
func readExpr(ref apispec.TypeRef, varName string) string {
	switch ref.Kind {
	case apispec.TypePrimitive:
		cpp := cppPrimitiveType(ref.Name)
		switch cpp {
		case "bool":
			return fmt.Sprintf("%s = reader.read_bool();", varName)
		case "float":
			return fmt.Sprintf("%s = reader.read_float();", varName)
		case "double":
			return fmt.Sprintf("%s = reader.read_double();", varName)
		case "int32_t":
			return fmt.Sprintf("%s = reader.read_int32();", varName)
		case "uint32_t":
			return fmt.Sprintf("%s = reader.read_uint32();", varName)
		case "int64_t":
			return fmt.Sprintf("%s = reader.read_int64();", varName)
		case "uint64_t":
			return fmt.Sprintf("%s = reader.read_uint64();", varName)
		}
		return fmt.Sprintf("%s = static_cast<%s>(reader.read_varint());", varName, cpp)
	case apispec.TypeString:
		return fmt.Sprintf("%s = reader.read_string();", varName)
	case apispec.TypeEnum:
		// Proto enum values are shifted +1 from the C++ enum values so that
		// proto3's required ENUM_UNSPECIFIED = 0 is reserved; undo that
		// shift here before casting to the C++ enum.
		return fmt.Sprintf("%s = static_cast<%s>(reader.read_int32() - 1);", varName, cppTypeName(ref))
	case apispec.TypeHandle:
		// Set-like containers: each repeated wire entry contributes
		// one element, so emit an `insert(...)` per entry rather
		// than the single-handle assignment used for plain handles.
		// See setLikeContainerInfo for the recognition rule.
		if _, _, ok := setLikeContainerInfo(ref); ok {
			elem, _, _ := parseMapType(ref.Name)
			innerRef := typeRefFromTemplateArg(strings.TrimSpace(elem))
			return readSetElementExpr(innerRef, varName)
		}
		return fmt.Sprintf("%s = read_handle_ptr(reader);", varName)
	case apispec.TypeValue:
		// View-type params (ValueViewTypes, e.g. absl::Span<const T>)
		// were rewritten by cppLocalType to std::vector<const T*> (or
		// similar). Each repeated submessage reads into an element,
		// mirroring the real TypeVector path.
		if matchesValueViewType(ref.QualType) {
			if elem := extractTemplateArgFromQualType(ref.QualType); elem != "" {
				// Build a synthetic TypeRef for the inner element
				// so readVectorElementExpr can produce the right
				// push_back / emplace_back code.
				innerRef := typeRefFromTemplateArg(strings.TrimSpace(elem))
				return readVectorElementExpr(innerRef, varName)
			}
		}
		// Value-type parameter: the Go client marshals the proto message
		// into a submessage; we route it into the per-class parser helper
		// (emitted once per value-type class — see valueTypeParsers).
		resolved := resolveTypeName(cppTypeName(ref))
		if c, ok := valueClasses[resolved]; ok && !c.IsHandle {
			recordValueTypeParserNeeded(c)
			parser := valueTypeParserName(resolved)
			return fmt.Sprintf("{ ProtoReader _sub = reader.read_submessage(); %s(_sub, %s); }", parser, varName)
		}
		return fmt.Sprintf("/* value type %s: submessage parser missing */ reader.skip();", ref.Name)
	case apispec.TypeVector:
		// A repeated proto field emits one `case` invocation per
		// element, so emit code that reads ONE submessage and
		// appends it to `varName`. We handle three element shapes:
		//   - primitives / enums / handles → read scalar and push_back
		//   - value-type submessages → delegate to per-class parser
		//     when the inner has a public default constructor; use
		//     emplace_back with all fields otherwise (supports types
		//     like StructType::StructField that only expose a
		//     field-initialising constructor)
		if ref.Inner == nil {
			return "reader.skip();"
		}
		return readVectorElementExpr(*ref.Inner, varName)
	default:
		return "reader.skip();"
	}
}

// readVectorElementExpr emits C++ that reads ONE element of the proto
// repeated field into the named std::vector / absl::Span-backing
// vector. Each proto `case N:` block invokes this; a repeated field's
// multiple entries naturally loop through the case handler.
func readVectorElementExpr(inner apispec.TypeRef, varName string) string {
	// Scalar inner types: convert to vector element via push_back.
	switch inner.Kind {
	case apispec.TypePrimitive:
		cpp := cppPrimitiveType(inner.Name)
		switch cpp {
		case "bool":
			return fmt.Sprintf("%s.push_back(reader.read_bool());", varName)
		case "float":
			return fmt.Sprintf("%s.push_back(reader.read_float());", varName)
		case "double":
			return fmt.Sprintf("%s.push_back(reader.read_double());", varName)
		case "int32_t":
			return fmt.Sprintf("%s.push_back(reader.read_int32());", varName)
		case "uint32_t":
			return fmt.Sprintf("%s.push_back(reader.read_uint32());", varName)
		case "int64_t":
			return fmt.Sprintf("%s.push_back(reader.read_int64());", varName)
		case "uint64_t":
			return fmt.Sprintf("%s.push_back(reader.read_uint64());", varName)
		}
		return fmt.Sprintf("%s.push_back(static_cast<%s>(reader.read_varint()));", varName, cpp)
	case apispec.TypeEnum:
		return fmt.Sprintf("%s.push_back(static_cast<%s>(reader.read_int32() - 1));", varName, cppTypeName(inner))
	case apispec.TypeString:
		return fmt.Sprintf("%s.push_back(reader.read_string());", varName)
	case apispec.TypeHandle:
		// Three cases for vector<Handle>:
		//   - unique_ptr<T> elements (std::vector<unique_ptr<T>>):
		//     each wire entry transfers sole ownership of one heap
		//     object. Construct a fresh unique_ptr<T> from the raw
		//     handle and emplace_back via std::move so the vector
		//     becomes the sole owner. Plain push_back of a
		//     dereferenced unique_ptr* would not compile —
		//     unique_ptr is move-only.
		//   - Pointer elements (std::vector<Foo*> or const Foo*):
		//     reinterpret the raw uint64 as the declared pointer
		//     type and push_back directly.
		//   - Value elements (std::vector<Foo> where Foo is bridged
		//     as a handle but the vector holds it by value): we
		//     receive a uint64 pointer from the Go side, dereference
		//     it to copy-construct the element, then push_back.
		if isUniquePointerType(inner.Name) || isUniquePointerType(inner.QualType) {
			pointee := smartPointerInner(inner.QualType)
			if pointee == "" {
				pointee = smartPointerInner(inner.Name)
			}
			if pointee == "" {
				return "/* vector<unique_ptr<T>>: pointee resolution failed */ reader.skip();"
			}
			// pointee carries any leading `const ` it picked up;
			// keep it so the constructed unique_ptr matches the
			// vector's declared element type.
			return fmt.Sprintf(
				"{ uint64_t _h = read_handle_ptr(reader); %s.emplace_back(reinterpret_cast<%s*>(_h)); }",
				varName, pointee,
			)
		}
		if inner.IsPointer {
			return fmt.Sprintf("%s.push_back(reinterpret_cast<decltype(%s)::value_type>(read_handle_ptr(reader)));", varName, varName)
		}
		elemType := cppTypeName(inner)
		return fmt.Sprintf("%s.push_back(*reinterpret_cast<const %s*>(read_handle_ptr(reader)));", varName, elemType)
	case apispec.TypeValue:
		resolved := resolveTypeName(cppTypeName(inner))
		if c, ok := valueClasses[resolved]; ok && !c.IsHandle {
			// Build a per-class emplace-back body using the known
			// public fields. When a default constructor exists we
			// still prefer the generated parser helper (keeps the
			// two paths consistent). When no default ctor exists,
			// parse the fields into locals first and emplace_back
			// with them positionally, matching the class's
			// field-initialising constructor.
			if classNoDefaultCtor[resolved] {
				return vectorElementEmplaceBackExpr(c, varName)
			}
			recordValueTypeParserNeeded(c)
			parser := valueTypeParserName(resolved)
			return fmt.Sprintf("{ %s _tmp{}; ProtoReader _sub = reader.read_submessage(); %s(_sub, _tmp); %s.push_back(std::move(_tmp)); }",
				resolved, parser, varName)
		}
		return fmt.Sprintf("/* vector element value %s: submessage parser missing */ reader.skip();", inner.Name)
	}
	return "reader.skip();"
}

// readSetElementExpr is the set-like sibling of
// readVectorElementExpr. The proto schema emits `repeated <Elem>`
// for any set-like container (configured via
// BridgeConfig.SetLikeTypePrefixes — see parseMapType / handleMapType),
// so each `case N:` invocation reads ONE element and inserts it into
// the local container instead of push_back-ing.
//
// Behaviour mirrors the vector path element-by-element: primitives
// and enums are read scalar-shaped, strings are read as std::string,
// handle pointers are reinterpreted from the wire's uint64. The only
// observable difference between vector and set emission is the
// container API call (`push_back` vs `insert`).
func readSetElementExpr(inner apispec.TypeRef, varName string) string {
	switch inner.Kind {
	case apispec.TypePrimitive:
		cpp := cppPrimitiveType(inner.Name)
		switch cpp {
		case "bool":
			return fmt.Sprintf("%s.insert(reader.read_bool());", varName)
		case "float":
			return fmt.Sprintf("%s.insert(reader.read_float());", varName)
		case "double":
			return fmt.Sprintf("%s.insert(reader.read_double());", varName)
		case "int32_t":
			return fmt.Sprintf("%s.insert(static_cast<%s>(reader.read_int32()));", varName, inner.Name)
		case "uint32_t":
			return fmt.Sprintf("%s.insert(static_cast<%s>(reader.read_uint32()));", varName, inner.Name)
		case "int64_t":
			return fmt.Sprintf("%s.insert(reader.read_int64());", varName)
		case "uint64_t":
			return fmt.Sprintf("%s.insert(reader.read_uint64());", varName)
		}
		return fmt.Sprintf("%s.insert(static_cast<%s>(reader.read_varint()));", varName, cpp)
	case apispec.TypeEnum:
		// cppTypeName for enums returns the bare api-spec name; the
		// local declaration goes through setLikeContainerInfo which
		// resolves it to the fully-qualified spelling. Resolving here
		// keeps the cast in step with the local's actual type so the
		// bridge's flat namespace context can find the enum.
		enumName := cppTypeName(inner)
		if resolved := resolveTypeName(enumName); resolved != "" {
			enumName = resolved
		}
		return fmt.Sprintf("%s.insert(static_cast<%s>(reader.read_int32() - 1));", varName, enumName)
	case apispec.TypeString:
		return fmt.Sprintf("%s.insert(reader.read_string());", varName)
	case apispec.TypeHandle:
		// Pointer element: cast the wire handle back to the declared
		// pointer type and insert it. value-by-handle elements
		// (sets-of-T where T is a bridged handle) are vanishingly
		// rare in real APIs (sets of value types require Hash + Eq);
		// fall through to a skip + comment so the bridge still
		// compiles even if such a shape ever shows up.
		if inner.IsPointer {
			return fmt.Sprintf("{ uint64_t _h = read_handle_ptr(reader); %s.insert(reinterpret_cast<decltype(%s)::value_type>(_h)); }", varName, varName)
		}
		return fmt.Sprintf("/* set element handle-by-value %s: not yet supported */ reader.skip();", inner.Name)
	}
	return "reader.skip();"
}

// vectorElementEmplaceBackExpr emits a C++ block that reads one
// submessage into per-field locals and emplace_back's into `varName`.
// Two emplace strategies are supported, picked from the class shape:
//
//   1. **All-fields ctor** — when the class has a non-default ctor
//      whose param count equals its field count, the locals are
//      forwarded positionally. Useful for value classes whose
//      ctor mirrors the field list.
//   2. **Best-match ctor + field-assign** — for `struct` types
//      whose ctor takes a strict subset of public fields (e.g.
//      `TVFSchemaColumn(name, type, is_pseudo_column)` over the
//      6-field public layout), pick the ctor with the maximum
//      number of name-matched fields, emplace with those, and
//      assign every remaining public field via `.back().X = ...`.
//
// Without strategy 2 the generator emitted an emplace_back with
// every field as a positional arg, which fails to compile when
// the class has no matching ctor.
//
// Handle fields are stored as uint64_t locals so the reader
// assignment stays straightforward; the casts are reapplied at
// the call site.
func vectorElementEmplaceBackExpr(c *apispec.Class, varName string) string {
	// Pick the constructor we'll forward to. Prefer a ctor that
	// matches every field; otherwise pick the ctor with the most
	// name-matched fields.
	ctorArgIdx, remainingFieldIdx := pickEmplaceCtor(c)

	var b strings.Builder
	b.WriteString("{\n")
	b.WriteString("    ProtoReader _sub = reader.read_submessage();\n")
	for i, f := range c.Fields {
		b.WriteString("    ")
		if f.Type.Kind == apispec.TypeHandle {
			fmt.Fprintf(&b, "uint64_t _f%d = 0;\n", i)
		} else {
			fmt.Fprintf(&b, "%s _f%d{};\n", cppLocalType(f.Type), i)
		}
	}
	b.WriteString("    while (_sub.has_data() && _sub.next()) {\n")
	b.WriteString("        switch (_sub.field()) {\n")
	for i, f := range c.Fields {
		local := fmt.Sprintf("_f%d", i)
		fieldNum := i + 1
		fmt.Fprintf(&b, "        case %d: %s break;\n", fieldNum, readExprForSub(f.Type, local))
	}
	b.WriteString("        default: _sub.skip(); break;\n")
	b.WriteString("        }\n")
	b.WriteString("    }\n")
	b.WriteString("    ")
	fmt.Fprintf(&b, "%s.emplace_back(", varName)
	for n, idx := range ctorArgIdx {
		if n > 0 {
			b.WriteString(", ")
		}
		f := c.Fields[idx]
		if f.Type.Kind == apispec.TypeHandle {
			cpp := cppTypeName(f.Type) + "*"
			if f.Type.IsConst {
				cpp = "const " + cpp
			}
			fmt.Fprintf(&b, "reinterpret_cast<%s>(_f%d)", cpp, idx)
		} else {
			fmt.Fprintf(&b, "std::move(_f%d)", idx)
		}
	}
	b.WriteString(");\n")
	for _, idx := range remainingFieldIdx {
		f := c.Fields[idx]
		b.WriteString("    ")
		if f.Type.Kind == apispec.TypeHandle {
			cpp := cppTypeName(f.Type) + "*"
			if f.Type.IsConst {
				cpp = "const " + cpp
			}
			fmt.Fprintf(&b, "%s.back().%s = reinterpret_cast<%s>(_f%d);\n", varName, f.Name, cpp, idx)
		} else {
			fmt.Fprintf(&b, "%s.back().%s = std::move(_f%d);\n", varName, f.Name, idx)
		}
	}
	b.WriteString("}")
	return b.String()
}

// pickEmplaceCtor returns the field indices that get passed to
// emplace_back (in ctor-param order) plus the field indices that
// must be assigned to `back().X` after construction. Strategy:
//
//   * If any non-default ctor's param count equals the field count,
//     return all field indices in declaration order (the historical
//     fast path — preserves behaviour for classes already aligned).
//   * Else pick the ctor that name-matches the most fields. Order
//     the matched indices to mirror the ctor's param sequence so
//     `emplace_back(name, type, is_pseudo_column)` lines up. Any
//     unmatched fields land in remainingFieldIdx for post-assign.
//   * If no ctor exists or no fields match, fall back to "all
//     fields, in declaration order" — same as the historical path,
//     preserving behaviour for classes whose ctors weren't parsed.
func pickEmplaceCtor(c *apispec.Class) (ctorArgIdx []int, remainingFieldIdx []int) {
	if c == nil {
		return nil, nil
	}
	allIdx := make([]int, len(c.Fields))
	for i := range c.Fields {
		allIdx[i] = i
	}
	defaultPath := func() ([]int, []int) {
		return allIdx, nil
	}

	type ctorMatch struct {
		matched []int // field indices in ctor-param order
		missing []int // field indices not used by this ctor
		score   int   // matched name count
	}
	var bestExact *ctorMatch
	var bestPartial *ctorMatch
	for _, m := range c.Methods {
		if !m.IsConstructor {
			continue
		}
		if m.Access != "" && m.Access != "public" {
			continue
		}
		if len(m.Params) == 0 {
			continue
		}
		used := make(map[int]bool)
		match := ctorMatch{}
		nameMatched := true
		for _, p := range m.Params {
			pn := normaliseCtorParamName(p.Name)
			idx := -1
			for fi, f := range c.Fields {
				if used[fi] {
					continue
				}
				if normaliseFieldName(f.Name) == pn {
					idx = fi
					break
				}
			}
			if idx < 0 {
				nameMatched = false
				break
			}
			used[idx] = true
			match.matched = append(match.matched, idx)
			match.score++
		}
		if !nameMatched {
			continue
		}
		for fi := range c.Fields {
			if !used[fi] {
				match.missing = append(match.missing, fi)
			}
		}
		if len(match.matched) == len(c.Fields) && len(match.missing) == 0 {
			bestExact = &match
			break
		}
		if bestPartial == nil || match.score > bestPartial.score {
			localCopy := match
			bestPartial = &localCopy
		}
	}
	if bestExact != nil {
		return bestExact.matched, bestExact.missing
	}
	if bestPartial != nil && bestPartial.score > 0 {
		return bestPartial.matched, bestPartial.missing
	}
	return defaultPath()
}

// normaliseCtorParamName strips trailing "_in" / "_arg" suffixes
// commonly used in C++ ctor parameters
// (`name_in`, `type_in`, …) so they line up with field names
// (`name`, `type`, …).
func normaliseCtorParamName(s string) string {
	s = strings.TrimSpace(s)
	for _, suf := range []string{"_in", "_arg", "Arg", "In"} {
		if strings.HasSuffix(s, suf) && len(s) > len(suf) {
			s = strings.TrimSuffix(s, suf)
			break
		}
	}
	return s
}

// normaliseFieldName trims a single trailing "_" some C++ code
// uses to mark private members (`name_`) so it matches a ctor
// parameter such as `name_in` after normalisation.
func normaliseFieldName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "_")
	return s
}

// readExprForSub is a tiny helper that emits read code operating on
// the nested reader `_sub` instead of the outer `reader`.
func readExprForSub(ref apispec.TypeRef, varName string) string {
	expr := readExpr(ref, varName)
	// Rewrite the outer reader's name to the inner one. readExpr
	// emits "reader.read_*()" / "read_handle_ptr(reader)"; swap those
	// for "_sub.*".
	expr = strings.ReplaceAll(expr, "reader.read_", "_sub.read_")
	expr = strings.ReplaceAll(expr, "reader.skip()", "_sub.skip()")
	expr = strings.ReplaceAll(expr, "read_handle_ptr(reader)", "read_handle_ptr(_sub)")
	expr = strings.ReplaceAll(expr, "reader.read_submessage()", "_sub.read_submessage()")
	return expr
}

// writeReturnExpr returns the C++ statements to serialize a return value.
// For handle types, handles by-pointer vs by-value returns differently.
func writeReturnExpr(ref apispec.TypeRef, fieldNum int, varName string) string {
	switch ref.Kind {
	case apispec.TypePrimitive:
		cpp := cppPrimitiveType(ref.Name)
		switch cpp {
		case "bool":
			return fmt.Sprintf("_pw.write_bool(%d, %s);", fieldNum, varName)
		case "float":
			return fmt.Sprintf("_pw.write_float(%d, %s);", fieldNum, varName)
		case "double":
			return fmt.Sprintf("_pw.write_double(%d, %s);", fieldNum, varName)
		case "int32_t":
			return fmt.Sprintf("_pw.write_int32(%d, %s);", fieldNum, varName)
		case "uint32_t":
			return fmt.Sprintf("_pw.write_uint32(%d, %s);", fieldNum, varName)
		case "int64_t":
			return fmt.Sprintf("_pw.write_int64(%d, %s);", fieldNum, varName)
		case "uint64_t":
			return fmt.Sprintf("_pw.write_uint64(%d, %s);", fieldNum, varName)
		}
		return fmt.Sprintf("_pw.write_int64(%d, static_cast<int64_t>(%s));", fieldNum, varName)
	case apispec.TypeString:
		// Handle string, string_view, string*, etc. via if-constexpr-like pattern
		// Use a templated helper via lambda
		if ref.IsPointer {
			return fmt.Sprintf("if (%s) _pw.write_string(%d, std::string(*%s));", varName, fieldNum, varName)
		}
		return fmt.Sprintf("_pw.write_string(%d, std::string(%s));", fieldNum, varName)
	case apispec.TypeEnum:
		// Shift C++ enum value by +1 to match proto3 ENUM_UNSPECIFIED = 0.
		return fmt.Sprintf("_pw.write_int32(%d, static_cast<int32_t>(%s) + 1);", fieldNum, varName)
	case apispec.TypeHandle:
		if ref.IsPointer {
			// Pointer return: direct cast
			return fmt.Sprintf("_pw.write_handle(%d, reinterpret_cast<uint64_t>(%s));", fieldNum, varName)
		}
		// Reference return (const or non-const): take address of the bound
		// reference. No copy needed.
		if ref.IsRef {
			return fmt.Sprintf("_pw.write_handle(%d, reinterpret_cast<uint64_t>(&%s));", fieldNum, varName)
		}
		// True by-value return. Pattern depends on type:
		typeName := cppTypeName(ref)
		// shared_ptr: heap-allocate a copy of the shared_ptr to retain
		// joint ownership. .release() does not exist on shared_ptr.
		if isSharedPointerType(typeName) || isSharedPointerType(ref.QualType) {
			return fmt.Sprintf("_pw.write_handle(%d, reinterpret_cast<uint64_t>(new auto(std::move(%s))));", fieldNum, varName)
		}
		// unique_ptr: .release() to get raw pointer
		if isSmartPointerType(typeName) || isSmartPointerType(ref.QualType) {
			return fmt.Sprintf("_pw.write_handle(%d, reinterpret_cast<uint64_t>(%s.release()));", fieldNum, varName)
		}
		// By-value handle returns are heap-constructed directly in writeCallBody
		// (`auto* _result = new T(func(...))`), so _result is already a pointer.
		return fmt.Sprintf("_pw.write_handle(%d, reinterpret_cast<uint64_t>(%s));", fieldNum, varName)
	case apispec.TypeVector:
		return writeVectorReturnExpr(ref, fieldNum, varName)
	case apispec.TypeValue:
		// Check if this type has a configured error-checking pattern.
		// This allows project-specific error types (e.g., status types
		// with .ok()/.message() methods) to be handled without
		// hardcoding library names in the generator.
		name := ref.Name
		if name == "" {
			name = ref.QualType
		}
		baseName := strings.TrimSpace(name)
		baseName = strings.TrimPrefix(baseName, "const ")
		baseName = strings.TrimSuffix(baseName, "&")
		baseName = strings.TrimSuffix(baseName, "*")
		baseName = strings.TrimSpace(baseName)
		if pattern := matchErrorType(baseName); pattern != "" {
			code := strings.ReplaceAll(pattern, "{result}", varName)
			// Templated error wrappers (e.g. absl::StatusOr<T>) carry a
			// payload on success — emit an else-branch that serialises
			// *_result to the response. Plain error types (absl::Status)
			// carry no payload, so the error pattern alone is correct.
			if inner, ok := statusOrInnerType(ref); ok {
				if successWrite := writeStatusOrSuccessExpr(inner, fieldNum, varName); successWrite != "" {
					return code + " else { " + successWrite + " }"
				}
			}
			return code
		}
		return writeValueReturnExpr(ref, fieldNum, varName)
	case apispec.TypeUnknown:
		return fmt.Sprintf("// TODO: serialize %s %s", ref.Kind, ref.Name)
	default:
		return fmt.Sprintf("// unsupported type %s", ref.Kind)
	}
}

// writeVectorReturnExpr emits C++ code to serialize a std::vector<T> result
// into the response writer. Repeated primitive/enum types use packed
// encoding via ProtoWriter's write_repeated_* helpers; repeated strings and
// handles use one tag per element.
func writeVectorReturnExpr(ref apispec.TypeRef, fieldNum int, varName string) string {
	if ref.Inner == nil {
		return fmt.Sprintf("// TODO: serialize vector (unknown element type) %s", ref.Name)
	}
	// If the callee returned a pointer to vector (e.g. `const std::vector<T>*`),
	// dereference it first. A null pointer should be treated as empty.
	// Parenthesise so member-access expressions bind to the dereferenced
	// value, e.g. `(*_result).size()` rather than `*_result.size()`.
	if ref.IsPointer {
		deref := "(*" + varName + ")"
		refNoPtr := ref
		refNoPtr.IsPointer = false
		return fmt.Sprintf("if (%s) { %s }", varName, writeVectorReturnExpr(refNoPtr, fieldNum, deref))
	}
	inner := *ref.Inner
	switch inner.Kind {
	case apispec.TypePrimitive:
		cpp := cppPrimitiveType(inner.Name)
		switch cpp {
		case "bool":
			return fmt.Sprintf("_pw.write_repeated_bool(%d, %s);", fieldNum, varName)
		case "float":
			return fmt.Sprintf("_pw.write_repeated_float(%d, %s);", fieldNum, varName)
		case "double":
			return fmt.Sprintf("_pw.write_repeated_double(%d, %s);", fieldNum, varName)
		case "int32_t":
			return fmt.Sprintf("_pw.write_repeated_int32(%d, %s);", fieldNum, varName)
		case "uint32_t":
			return fmt.Sprintf("_pw.write_repeated_uint32(%d, %s);", fieldNum, varName)
		case "int64_t":
			return fmt.Sprintf("_pw.write_repeated_int64(%d, %s);", fieldNum, varName)
		case "uint64_t":
			return fmt.Sprintf("_pw.write_repeated_uint64(%d, %s);", fieldNum, varName)
		}
		// Narrow int (int16_t/int8_t) cast-copy to int32 vector and write
		// packed to avoid touching ABI-sensitive enum-style sizes.
		return fmt.Sprintf("{ std::vector<int32_t> _tmp(%s.begin(), %s.end()); _pw.write_repeated_int32(%d, _tmp); }", varName, varName, fieldNum)
	case apispec.TypeEnum:
		return fmt.Sprintf("{ std::vector<int32_t> _tmp; _tmp.reserve(%s.size()); for (const auto& _e : %s) _tmp.push_back(static_cast<int32_t>(_e) + 1); _pw.write_repeated_int32(%d, _tmp); }", varName, varName, fieldNum)
	case apispec.TypeString:
		return fmt.Sprintf("{ std::vector<std::string> _tmp; _tmp.reserve(%s.size()); for (const auto& _s : %s) _tmp.emplace_back(_s); _pw.write_repeated_string(%d, _tmp); }", varName, varName, fieldNum)
	case apispec.TypeHandle:
		// Two sub-cases:
		//   vector<T*> / vector<const T*>: iterate as-is and write each
		//       non-null pointer directly.
		//   vector<T>  (T is a handle-promoted encapsulated class): each
		//       element lives on the vector's stack and would die when
		//       _result goes out of scope. Heap-copy each element so the
		//       caller receives stable handle pointers. Requires T to
		//       have an accessible copy constructor and operator new —
		//       bridgeable-type filters should already enforce this.
		if inner.IsPointer {
			return fmt.Sprintf("for (auto* _h : %s) { if (_h) _pw.write_handle(%d, reinterpret_cast<uint64_t>(_h)); }", varName, fieldNum)
		}
		// Smart pointer elements: handle unique_ptr and shared_ptr separately.
		innerName := inner.Name
		if innerName == "" {
			innerName = inner.QualType
		}
		if isSharedPointerType(cppTypeName(inner)) || isSharedPointerType(innerName) {
			// shared_ptr<T>: heap-allocate a copy of each shared_ptr to
			// retain ownership across the bridge. Works for const iteration
			// because we copy the shared_ptr rather than move.
			return fmt.Sprintf("for (const auto& _elem : %s) { _pw.write_handle(%d, reinterpret_cast<uint64_t>(new auto(_elem))); }", varName, fieldNum)
		}
		if isSmartPointerType(cppTypeName(inner)) || isSmartPointerType(innerName) {
			// unique_ptr<T>: non-owning access via .get(). We use const
			// iteration because the source may be a `const vector&` (e.g.,
			// returned from a getter); calling .release() would require
			// mutation and fail for const containers. The caller receives
			// a non-owning raw pointer valid for the lifetime of the owner.
			return fmt.Sprintf("for (const auto& _elem : %s) { _pw.write_handle(%d, reinterpret_cast<uint64_t>(_elem.get())); }", varName, fieldNum)
		}
		// Non-pointer, non-smart-pointer handle: take address of element
		return fmt.Sprintf("for (const auto& _elem : %s) { _pw.write_handle(%d, reinterpret_cast<uint64_t>(&_elem)); }", varName, fieldNum)
	case apispec.TypeValue:
		// vector<Value>: serialize each element as a submessage by recursing
		// through writeValueReturnExpr on a per-element temporary. A sub
		// ProtoWriter is emitted for each element and flushed with the
		// parent's write_submessage.
		perElem := writeValueReturnExprForSub("_elem", inner)
		// The _subw destructor releases _subw.data_ at end of the
		// block; calling free() before it is a double-free that
		// corrupts dlmalloc's freelist.
		return fmt.Sprintf("for (const auto& _elem : %s) {\n    ProtoWriter _subw;\n    %s\n    _pw.write_submessage(%d, _subw);\n}", varName, perElem, fieldNum)
	}
	return fmt.Sprintf("// TODO: serialize vector<%s>", inner.Name)
}

// writeValueReturnExpr emits C++ to serialize a POD/value struct return as
// a nested submessage at fieldNum. It looks the class up in valueClasses to
// find its fields, then emits field-by-field writes into a sub ProtoWriter.
func writeValueReturnExpr(ref apispec.TypeRef, fieldNum int, varName string) string {
	// ValueViewTypes (e.g. `absl::Span<T>`) are non-owning views. The
	// proto schema renders them as `repeated <Elem>` (see typeRefToProto's
	// matchesValueViewType branch); the bridge response writer therefore
	// has to iterate the view and emit one element per wire entry.
	// Without this, getters that return a Span fall through to the
	// "no fields found" TODO and silently return an empty result.
	if matchesValueViewType(ref.QualType) {
		if elem := extractTemplateArgFromQualType(ref.QualType); elem != "" {
			return writeValueViewReturnExpr(elem, fieldNum, varName)
		}
	}
	c := lookupValueClass(ref.Name)
	if c == nil || len(c.Fields) == 0 {
		return fmt.Sprintf("// TODO: serialize value %s (no fields found)", ref.Name)
	}
	var sb strings.Builder
	sb.WriteString("{\n    ProtoWriter _subw;\n")
	accessOp := "."
	if ref.IsPointer {
		accessOp = "->"
	}
	for i, f := range c.Fields {
		fieldProtoNum := i + 1
		memberRef := fmt.Sprintf("%s%s%s", varName, accessOp, f.Name)
		sb.WriteString("    ")
		sb.WriteString(writeValueFieldExpr(f.Type, fieldProtoNum, memberRef, "_subw"))
		sb.WriteString("\n")
	}
	// _subw's destructor releases its buffer at end of block; an
	// explicit free() before it double-frees and corrupts dlmalloc.
	fmt.Fprintf(&sb, "    _pw.write_submessage(%d, _subw);\n}", fieldNum)
	return sb.String()
}

// writeValueViewReturnExpr emits a C++ for-each loop that writes
// every element of a non-owning view (e.g. `absl::Span<const T>`) as
// a separate wire entry at fieldNum. The dispatch by element kind
// mirrors writeReturnExpr / writeVectorReturnExpr: primitive and
// string elements use scalar write helpers; class-handle elements
// emit `write_handle` per element.
//
// The helper is library-agnostic — any type registered in
// `BridgeConfig.ValueViewTypes` is matched the same way; only the
// inner element type drives the per-element emission.
func writeValueViewReturnExpr(elem string, fieldNum int, varName string) string {
	rawWithPtr := strings.TrimSpace(elem)
	innerIsPointer := strings.HasSuffix(rawWithPtr, "*")
	raw := rawWithPtr
	if innerIsPointer {
		raw = strings.TrimSpace(strings.TrimSuffix(raw, "*"))
	}
	raw = strings.TrimPrefix(raw, "const ")
	raw = strings.TrimSpace(raw)

	// String element (the common case for path-like APIs).
	if raw == "std::string" || raw == "string" || raw == "std::string_view" {
		var sb strings.Builder
		sb.WriteString("for (const auto& _e : ")
		sb.WriteString(varName)
		sb.WriteString(") {\n        ")
		fmt.Fprintf(&sb, "_pw.write_string(%d, std::string(_e));\n    }", fieldNum)
		return sb.String()
	}

	// Primitive element: route through cppPrimitiveType to find the
	// matching ProtoWriter scalar write.
	if pt := tryPrimitiveToPbType(raw); pt != "" {
		write := primitiveProtoWriterCall(raw, fieldNum, "_e")
		if write != "" {
			var sb strings.Builder
			sb.WriteString("for (const auto& _e : ")
			sb.WriteString(varName)
			sb.WriteString(") {\n        ")
			sb.WriteString(write)
			sb.WriteString("\n    }")
			return sb.String()
		}
	}

	// Class element: handed back to Go as a borrowed uint64 handle
	// pointing at the live C++ storage. The view is non-owning, so
	// the underlying objects are kept alive by whatever owns the
	// view's source — the Go caller MUST release the handles before
	// that source goes out of scope.
	//
	//   - View<const T*> → `_e` is itself a pointer; cast directly.
	//   - View<const T>  → `_e` is a const reference; take its
	//     address to recover a pointer-to-T.
	resolved := resolveTypeName(raw)
	if resolved == "" {
		resolved = raw
	}
	if classSourceFiles != nil {
		if _, known := classSourceFiles[resolved]; known {
			handleExpr := "_e"
			if !innerIsPointer {
				handleExpr = "&_e"
			}
			var sb strings.Builder
			sb.WriteString("for (const auto& _e : ")
			sb.WriteString(varName)
			sb.WriteString(") {\n        ")
			fmt.Fprintf(&sb, "_pw.write_handle(%d, reinterpret_cast<uint64_t>(%s));\n    }", fieldNum, handleExpr)
			return sb.String()
		}
	}

	return fmt.Sprintf("// TODO: serialize view-of-%s (unrecognised element kind)", elem)
}

// primitiveProtoWriterCall returns the per-element ProtoWriter scalar
// write for a known primitive name, or "" when the name is not a
// recognised primitive. Mirrors readVectorElementExpr's primitive
// dispatch in reverse — kept private because the bridge writer is
// the only consumer.
func primitiveProtoWriterCall(primitiveName string, fieldNum int, expr string) string {
	cpp := cppPrimitiveType(primitiveName)
	switch cpp {
	case "bool":
		return fmt.Sprintf("_pw.write_bool(%d, %s);", fieldNum, expr)
	case "float":
		return fmt.Sprintf("_pw.write_float(%d, %s);", fieldNum, expr)
	case "double":
		return fmt.Sprintf("_pw.write_double(%d, %s);", fieldNum, expr)
	case "int32_t":
		return fmt.Sprintf("_pw.write_int32(%d, %s);", fieldNum, expr)
	case "uint32_t":
		return fmt.Sprintf("_pw.write_uint32(%d, %s);", fieldNum, expr)
	case "int64_t":
		return fmt.Sprintf("_pw.write_int64(%d, %s);", fieldNum, expr)
	case "uint64_t":
		return fmt.Sprintf("_pw.write_uint64(%d, %s);", fieldNum, expr)
	}
	return ""
}

// writeValueReturnExprForSub is a variant that writes fields directly into a
// caller-provided sub writer (used for vector<Value> element serialization).
func writeValueReturnExprForSub(varName string, ref apispec.TypeRef) string {
	c := lookupValueClass(ref.Name)
	if c == nil || len(c.Fields) == 0 {
		return fmt.Sprintf("/* TODO: value %s fields */", ref.Name)
	}
	var sb strings.Builder
	for i, f := range c.Fields {
		fieldProtoNum := i + 1
		memberRef := fmt.Sprintf("%s.%s", varName, f.Name)
		sb.WriteString(writeValueFieldExpr(f.Type, fieldProtoNum, memberRef, "_subw"))
		sb.WriteString(" ")
	}
	return sb.String()
}

// writeValueFieldExpr emits a single-field write into the named writer
// variable. Only primitive, string, bool, and enum fields are supported;
// nested vector/value/handle fields inside a value struct currently fall
// back to a TODO comment (these are rare in POD return types).
func writeValueFieldExpr(ref apispec.TypeRef, fieldNum int, memberExpr string, writerVar string) string {
	switch ref.Kind {
	case apispec.TypePrimitive:
		cpp := cppPrimitiveType(ref.Name)
		switch cpp {
		case "bool":
			return fmt.Sprintf("%s.write_bool(%d, %s);", writerVar, fieldNum, memberExpr)
		case "float":
			return fmt.Sprintf("%s.write_float(%d, %s);", writerVar, fieldNum, memberExpr)
		case "double":
			return fmt.Sprintf("%s.write_double(%d, %s);", writerVar, fieldNum, memberExpr)
		case "int32_t":
			return fmt.Sprintf("%s.write_int32(%d, %s);", writerVar, fieldNum, memberExpr)
		case "uint32_t":
			return fmt.Sprintf("%s.write_uint32(%d, %s);", writerVar, fieldNum, memberExpr)
		case "int64_t":
			return fmt.Sprintf("%s.write_int64(%d, %s);", writerVar, fieldNum, memberExpr)
		case "uint64_t":
			return fmt.Sprintf("%s.write_uint64(%d, %s);", writerVar, fieldNum, memberExpr)
		}
		return fmt.Sprintf("%s.write_int64(%d, static_cast<int64_t>(%s));", writerVar, fieldNum, memberExpr)
	case apispec.TypeString:
		return fmt.Sprintf("%s.write_string(%d, std::string(%s));", writerVar, fieldNum, memberExpr)
	case apispec.TypeEnum:
		return fmt.Sprintf("%s.write_int32(%d, static_cast<int32_t>(%s) + 1);", writerVar, fieldNum, memberExpr)
	case apispec.TypeHandle:
		// Smart pointers (unique_ptr, shared_ptr) are classified as
		// TypeHandle but cannot be reinterpret_cast'd to uint64 directly
		// — we need the raw pointer via .get().
		if isSmartPointerType(ref.QualType) {
			return fmt.Sprintf("%s.write_handle(%d, reinterpret_cast<uint64_t>(%s.get()));", writerVar, fieldNum, memberExpr)
		}
		if ref.IsPointer || ref.IsRef {
			return fmt.Sprintf("%s.write_handle(%d, reinterpret_cast<uint64_t>(%s));", writerVar, fieldNum, memberExpr)
		}
		// By-value handle field. The element being iterated is
		// typically on the stack and will die at end of iteration, so
		// we cannot hand out a pointer to it. Skip with a TODO — the
		// caller can expose this via an explicit method RPC if needed.
		return fmt.Sprintf("/* TODO: by-value handle field %s (would leak) */", memberExpr)
	}
	return fmt.Sprintf("/* TODO: value field %s of kind %s */", memberExpr, ref.Kind)
}

// isAllowedExternalType returns true for external types that are
// commonly used in C++ project function signatures. These types aren't
// in the parsed spec (they're from external libraries) but the
// bridge can handle them through auto capture and fallback serialization.
// matchErrorType checks if baseName matches any configured error type
// pattern from BridgeConfig.ErrorTypes. Returns the C++ snippet to emit,
// or empty string if no match.
func matchErrorType(baseName string) string {
	for typeName, pattern := range bridgeConfig.ErrorTypes {
		if baseName == typeName {
			return pattern
		}
		// Template prefix match
		if !strings.Contains(typeName, "<") && strings.HasPrefix(baseName, typeName+"<") {
			return pattern
		}
	}
	return ""
}

// isAllowedExternalType checks if the type is in the project-specific
// external types list from BridgeConfig. Returns true if the type
// (after stripping qualifiers) matches any configured external type,
// either exactly or as a template prefix (e.g., "Foo<" matches "Foo<Bar>").
// isIncludedExternalClass reports whether qualName is opted into
// bridge generation despite living outside the project source via
// `bridge.IncludeExternalClasses` in wasmify.json.
func isIncludedExternalClass(qualName string) bool {
	q := strings.TrimSpace(qualName)
	if q == "" {
		return false
	}
	for _, c := range bridgeConfig.IncludeExternalClasses {
		if c == q {
			return true
		}
	}
	return false
}

func isAllowedExternalType(name string) bool {
	n := strings.TrimSpace(name)
	n = strings.TrimPrefix(n, "const ")
	n = strings.TrimSuffix(n, "&")
	n = strings.TrimSuffix(n, "*")
	n = strings.TrimSpace(n)

	for _, ext := range bridgeConfig.ExternalTypes {
		if n == ext {
			return true
		}
		// Template prefix match: "absl::StatusOr" matches "absl::StatusOr<T>"
		if strings.HasSuffix(ext, "<") && strings.HasPrefix(n, ext) {
			return true
		}
		if !strings.Contains(ext, "<") && strings.HasPrefix(n, ext+"<") {
			return true
		}
	}
	// Smart pointers wrapping any type are always usable
	if isSmartPointerType(n) {
		return true
	}
	return false
}

// matchesValueViewType reports whether qualType is one of the
// configured non-owning view types (ValueViewTypes). When true, the
// caller typically materialises a std::vector<Elem> and relies on the
// view's implicit ctor. The match is by qual-type prefix (e.g.
// "absl::Span" matches "absl::Span<const Foo>") and strips leading
// cv-qualifiers so `const View<...>&` forms match too.
func matchesValueViewType(qualType string) bool {
	qt := strings.TrimSpace(qualType)
	qt = strings.TrimPrefix(qt, "const ")
	qt = strings.TrimSpace(qt)
	for _, prefix := range bridgeConfig.ValueViewTypes {
		if strings.HasPrefix(qt, prefix) {
			return true
		}
	}
	return false
}

// isErrorOnlyReturnType reports whether t is one of the configured
// error-only return types. These serialize as "void with error" on
// the wire and are reconstructed from the error field alone on the
// callback side (see ErrorReconstruct).
func isErrorOnlyReturnType(t apispec.TypeRef) bool {
	if t.IsPointer || t.IsRef {
		return false
	}
	qt := strings.TrimSpace(t.QualType)
	for _, name := range bridgeConfig.ErrorOnlyReturnTypes {
		if qt == name || t.Name == name {
			return true
		}
	}
	return false
}

// matchErrorOnlyReturnType returns the canonical name that matched,
// or "" when t is not an error-only return type.
func matchErrorOnlyReturnType(t apispec.TypeRef) string {
	if t.IsPointer || t.IsRef {
		return ""
	}
	qt := strings.TrimSpace(t.QualType)
	for _, name := range bridgeConfig.ErrorOnlyReturnTypes {
		if qt == name || t.Name == name {
			return name
		}
	}
	return ""
}

// isUnsupportedStringType reports whether qualType contains one of
// the configured substrings that mark a string-like type as not
// bridgeable (views, compressed strings). Used by trampoline paths
// where we can't guarantee backing-store lifetime.
func isUnsupportedStringType(qualType string) bool {
	qt := strings.TrimSpace(qualType)
	if qt == "" {
		return false
	}
	for _, marker := range bridgeConfig.UnsupportedStringTypes {
		if strings.Contains(qt, marker) {
			return true
		}
	}
	return false
}

// isSmartPointerType reports whether a C++ type spelling names a
// std::unique_ptr or std::shared_ptr. The check is intentionally loose
// (string-based) because clang AST spellings may include template arg
// whitespace variations.
func isSmartPointerType(qualType string) bool {
	if qualType == "" {
		return false
	}
	qt := strings.TrimSpace(qualType)
	qt = strings.TrimPrefix(qt, "const ")
	return strings.HasPrefix(qt, "std::unique_ptr<") ||
		strings.HasPrefix(qt, "unique_ptr<") ||
		strings.HasPrefix(qt, "std::shared_ptr<") ||
		strings.HasPrefix(qt, "shared_ptr<")
}

// isUniquePointerType is the ownership-transfer-only variant of
// isSmartPointerType. It matches only the `std::unique_ptr<...>`
// spellings (with or without `const` / `std::` prefix), and is the
// correct check for any code path that needs to recognise an
// "owned-by-caller, exclusive ownership" handoff — most prominently
// factory methods that produce a freshly-allocated object via an
// out-parameter (`Status f(args..., std::unique_ptr<T>*)`).
//
// `std::shared_ptr` is intentionally excluded: it represents shared
// ownership rather than transfer of sole ownership, and the bridge's
// release-and-emit-handle path (`out.release()` then write the raw
// pointer to the response) is only correct under exclusive ownership.
//
// The split between this and `isSmartPointerType` is the
// representation-level expression of what is actually a semantic
// distinction in the C++ standard: `unique_ptr` and `shared_ptr` are
// different smart pointers with different ownership rules, and the
// bridge has to handle them differently.
func isUniquePointerType(qualType string) bool {
	if qualType == "" {
		return false
	}
	qt := strings.TrimSpace(qualType)
	qt = strings.TrimPrefix(qt, "const ")
	return strings.HasPrefix(qt, "std::unique_ptr<") ||
		strings.HasPrefix(qt, "unique_ptr<")
}

// smartPointerInner returns the inner T of a std::unique_ptr<T> or
// std::shared_ptr<T> spelling, with the bare class name resolved to its
// fully-qualified form via resolveTypeName. Returns "" if qualType is not
// a smart pointer. Example: "std::unique_ptr<const Column>" → "const
// googlesql::Column" (when Column resolves to googlesql::Column).
func smartPointerInner(qualType string) string {
	qt := strings.TrimSpace(qualType)
	// Strip trailing reference markers so `unique_ptr<T>&&` is handled.
	qt = strings.TrimSuffix(qt, "&&")
	qt = strings.TrimSuffix(qt, "&")
	qt = strings.TrimSpace(qt)
	qt = strings.TrimPrefix(qt, "const ")
	qt = strings.TrimSpace(qt)
	for _, prefix := range []string{"std::unique_ptr<", "unique_ptr<", "std::shared_ptr<", "shared_ptr<"} {
		if !strings.HasPrefix(qt, prefix) {
			continue
		}
		inner := qt[len(prefix):]
		inner = strings.TrimSuffix(inner, ">")
		inner = strings.TrimSpace(inner)
		// Preserve const-qualifier and resolve the bare type name so the
		// emitted C++ uses the fully-qualified class ("Column" alone may
		// be ambiguous where a using-declaration is absent; the wasm
		// bridge compiles the file in a neutral namespace context).
		hadConst := false
		if strings.HasPrefix(inner, "const ") {
			hadConst = true
			inner = strings.TrimSpace(strings.TrimPrefix(inner, "const "))
		}
		// Only resolve bare identifiers — if the inner is already qualified
		// (`googlesql::Column`) or a pointer/reference, keep it as-is.
		if !strings.Contains(inner, "::") && !strings.ContainsAny(inner, "*&<>") {
			inner = resolveTypeName(inner)
		}
		if hadConst {
			return "const " + inner
		}
		return inner
	}
	return ""
}

// handleArgExpr builds the C++ expression used as a function-call argument
// for a handle-typed parameter. It centralizes the logic for plain handles,
// unique_ptr, shared_ptr, references, pointers, and rvalue references so the
// three dispatch-body generators share one correct implementation.
//
// varName is the name of the local variable holding the raw handle integer
// (populated from the request via reinterpret_cast). castType is the non-
// qualified resolved C++ type (inner T for a handle). constQual is "const "
// or "". The returned string is pasted directly into a function-call arg
// list.
func handleArgExpr(p apispec.Param, varName, castType, constQual string) string {
	qt := strings.TrimSpace(p.Type.QualType)
	// Pointer-to-pointer: `T *&` — rare but the original writeCallBody
	// handles this explicitly in its own switch; fall through to the
	// caller when detected, since the emission requires a lambda and is
	// not shared with other sites.
	// (This helper is only used for the by-value / by-ref / by-pointer /
	// by-rvalue-ref cases.)
	isRvalRef := p.Type.IsRef && strings.HasSuffix(qt, "&&")

	// Smart pointer paths: when the callee signature is unique_ptr<T> or
	// shared_ptr<T> (by value or rvalue-ref), the raw varName encodes the
	// handle the Go side holds — which for unique_ptr is a bare T* obtained
	// from .release() on the factory side, and for shared_ptr is a heap-
	// allocated shared_ptr<T>* (see writeReturnExpr at bridge.go ~2029).
	if inner := smartPointerInner(qt); inner != "" {
		if isSharedPointerType(qt) {
			// shared_ptr<T>: varName -> shared_ptr<T>* on the heap.
			if isRvalRef || !p.Type.IsRef {
				// Pass by value or T&&: move the heap shared_ptr into the
				// callee. (Move is safe; the heap storage is still valid
				// afterwards but the shared_ptr inside it is empty.)
				return fmt.Sprintf("std::move(*reinterpret_cast<std::shared_ptr<%s>*>(%s))", inner, varName)
			}
			// T& / const T&: lvalue bind to the heap shared_ptr.
			return fmt.Sprintf("*reinterpret_cast<std::shared_ptr<%s>*>(%s)", inner, varName)
		}
		// unique_ptr<T>: varName -> raw T* (Go side called .release() on
		// its factory). Construct a fresh unique_ptr that owns this raw
		// pointer and pass it into the callee. The constructed prvalue
		// binds to either `unique_ptr<T>` by value or `unique_ptr<T>&&`.
		// For lvalue references (`unique_ptr<T>&`) this would need a named
		// local; that case is rare in public APIs and left as a TODO.
		return fmt.Sprintf("std::unique_ptr<%s>(reinterpret_cast<%s*>(%s))", inner, inner, varName)
	}

	switch {
	case isRvalRef:
		return fmt.Sprintf("std::move(*reinterpret_cast<%s%s*>(%s))", constQual, castType, varName)
	case p.Type.IsRef:
		return fmt.Sprintf("*reinterpret_cast<%s%s*>(%s)", constQual, castType, varName)
	case p.Type.IsPointer:
		return fmt.Sprintf("reinterpret_cast<%s%s*>(%s)", constQual, castType, varName)
	default:
		return fmt.Sprintf("std::move(*reinterpret_cast<%s%s*>(%s))", constQual, castType, varName)
	}
}

// isSharedPointerType reports whether the qualified type is a shared_ptr.
// shared_ptr requires different handle handling than unique_ptr: .release()
// does not exist; the bridge must heap-allocate a copy of the shared_ptr
// to retain joint ownership across the wasm boundary.
func isSharedPointerType(qualType string) bool {
	if qualType == "" {
		return false
	}
	qt := strings.TrimSpace(qualType)
	qt = strings.TrimPrefix(qt, "const ")
	return strings.HasPrefix(qt, "std::shared_ptr<") ||
		strings.HasPrefix(qt, "shared_ptr<")
}

// writeFieldExpr returns the C++ statement to write a field (for output params, getters).
// Unlike writeReturnExpr, output params for non-handle types are stored as values
// in the bridge (we declare them as pointee type and pass &var to the function).
// Handle output params are already pointers to heap-allocated objects, so we just
// return the pointer directly.
func writeFieldExpr(ref apispec.TypeRef, fieldNum int, varName string) string {
	if ref.Kind == apispec.TypeHandle {
		// Smart pointer output params: extract the raw pointer.
		// unique_ptr uses .release() (transfers ownership to caller).
		// shared_ptr uses .get() (caller gets a non-owning handle; the
		// shared_ptr must stay alive — typically as a class member).
		qt := ref.QualType
		if qt == "" {
			qt = ref.Name
		}
		if strings.Contains(qt, "unique_ptr") {
			return fmt.Sprintf("_pw.write_handle(%d, reinterpret_cast<uint64_t>(%s.release()));", fieldNum, varName)
		}
		if strings.Contains(qt, "shared_ptr") {
			return fmt.Sprintf("_pw.write_handle(%d, reinterpret_cast<uint64_t>(%s.get()));", fieldNum, varName)
		}
		// T** / T*& output: local is declared as T* pointer and the
		// callee writes the pointer value (either via `*ptr = ...`
		// for `T**` or `ref = ...` for `T*&`). Either way the local
		// holds the produced pointer when the call returns; emit it
		// directly to the response.
		stripped := strings.TrimSuffix(strings.TrimSpace(qt), "const")
		stripped = strings.TrimSpace(stripped)
		if strings.HasSuffix(stripped, "**") {
			return fmt.Sprintf("_pw.write_handle(%d, reinterpret_cast<uint64_t>(%s));", fieldNum, varName)
		}
		if isReferenceToPointerHandle(ref) {
			return fmt.Sprintf("_pw.write_handle(%d, reinterpret_cast<uint64_t>(%s));", fieldNum, varName)
		}
		// Output params: check if the local was declared as a pointer
		// (abstract/noNew types) vs a value type.
		typeName := cppTypeName(ref)
		resolved := resolveTypeName(typeName)
		if (classAbstract != nil && classAbstract[resolved]) ||
			(classNoNew != nil && classNoNew[resolved]) {
			// Declared as `T* out_var = nullptr;` — already a pointer
			return fmt.Sprintf("_pw.write_handle(%d, reinterpret_cast<uint64_t>(%s));", fieldNum, varName)
		}
		// Declared as `T out_var{};` — heap-allocate via new+move
		return fmt.Sprintf("{ auto* _hp = new %s(std::move(%s)); _pw.write_handle(%d, reinterpret_cast<uint64_t>(_hp)); }", typeName, varName, fieldNum)
	}
	// For other types, strip pointer flag since output params are stored by value
	stripped := ref
	stripped.IsPointer = false
	stripped.IsRef = false
	return writeReturnExpr(stripped, fieldNum, varName)
}

// ======================================
// Service/Method ID constants
// ======================================

func writeServiceMethodIDs(b *strings.Builder, freeFunctions []apispec.Function,
	handleClasses map[string]*apispec.Class, classNames []string, packageName string) {

	b.WriteString("// ======================================\n")
	b.WriteString("// Service and method IDs\n")
	b.WriteString("// ======================================\n\n")

	serviceID := 0

	if len(freeFunctions) > 0 {
		svcName := strings.ToUpper(toSnakeCase(packageName))
		fmt.Fprintf(b, "static const int32_t SERVICE_%s = %d;\n", svcName, serviceID)
		for i, fn := range freeFunctions {
			methodName := toMethodConstName(fn.Name)
			fmt.Fprintf(b, "static const int32_t METHOD_%s_%s = %d;\n", svcName, methodName, i)
		}
		b.WriteString("\n")
		serviceID++
	}

	for _, qualName := range classNames {
		c := handleClasses[qualName]
		svcName := strings.ToUpper(toSnakeCase(protoMessageName(qualName)))
		fmt.Fprintf(b, "static const int32_t SERVICE_%s = %d;\n", svcName, serviceID)
		methodID := 0
		methods := filterBridgeMethods(disambiguateMethodNames(c.Methods))
		// Track every constant suffix we've emitted on this service so
		// late-arriving methods that would alias an earlier constant get
		// a numeric tail appended, mirroring the per-service uniqueness
		// proto.go enforces on RPC names. Without this guard a class
		// that defines both a constructor and a regular method named
		// `New` (e.g. protobuf-generated message subclasses with
		// `static T* New(Arena*)`) emits two `METHOD_<X>_NEW` constants
		// at the top of the bridge translation unit and the C++ compile
		// fails with a redefinition error.
		usedConstSuffix := make(map[string]bool)
		uniqueConstSuffix := func(suffix string) string {
			candidate := suffix
			for i := 2; usedConstSuffix[candidate]; i++ {
				candidate = fmt.Sprintf("%s_%d", suffix, i)
			}
			usedConstSuffix[candidate] = true
			return candidate
		}
		// Constructors first. Abstract classes skip constructors.
		ctorCount := 0
		for _, m := range methods {
			if !m.IsConstructor {
				continue
			}
			if c.IsAbstract {
				continue
			}
			ctorCount++
			mName := "NEW"
			if ctorCount > 1 {
				mName = fmt.Sprintf("NEW%d", ctorCount)
			}
			mName = uniqueConstSuffix(mName)
			fmt.Fprintf(b, "static const int32_t METHOD_%s_%s = %d;\n", svcName, mName, methodID)
			methodID++
		}
		// Static factory methods
		factoryIdx := 0
		for _, m := range methods {
			if !m.IsStatic || !isStaticFactory(m) {
				continue
			}
			rpcName, _ := toProtoRPCName(m.Name)
			if rpcName == "" {
				continue
			}
			mName := fmt.Sprintf("FACTORY_%d_%s", factoryIdx, toMethodConstName(rpcName))
			mName = uniqueConstSuffix(mName)
			fmt.Fprintf(b, "static const int32_t METHOD_%s_%s = %d;\n", svcName, mName, methodID)
			methodID++
			factoryIdx++
		}
		// Regular methods
		for _, m := range methods {
			if m.IsConstructor || (m.IsStatic && isStaticFactory(m)) {
				continue
			}
			rpcName, _ := toProtoRPCName(m.Name)
			if rpcName == "" {
				continue
			}
			mName := uniqueConstSuffix(toMethodConstName(rpcName))
			fmt.Fprintf(b, "static const int32_t METHOD_%s_%s = %d;\n", svcName, mName, methodID)
			methodID++
		}
		for _, f := range filterBridgeFields(c.Fields) {
			gName := uniqueConstSuffix("GET_" + strings.ToUpper(toSnakeCase(f.Name)))
			fmt.Fprintf(b, "static const int32_t METHOD_%s_%s = %d;\n", svcName, gName, methodID)
			methodID++
		}
		// Downcast METHOD_X_TO_Y constants are intentionally NOT
		// emitted. Go type assertion covers abstract → concrete
		// conversion without a wasm round-trip. See CLAUDE.md:
		// "do not emit Downcast APIs".
		if isCallbackCandidateForBridge(c) {
			ctorVariants := collectTrampolineCtors(c)
			variantCount := len(ctorVariants)
			if variantCount == 0 {
				variantCount = 1
			}
			for i := 0; i < variantCount; i++ {
				name := "FROM_CALLBACK"
				if i > 0 {
					name = fmt.Sprintf("FROM_CALLBACK%d", i+1)
				}
				name = uniqueConstSuffix(name)
				fmt.Fprintf(b, "static const int32_t METHOD_%s_%s = %d;\n", svcName, name, methodID)
				methodID++
			}
		}
		// Emit the Free/release slot only when proto.go emits the RPC
		// (matches the c.HasPublicDtor check there). For factory-owned
		// types the slot is absent entirely so IDs line up.
		if c.HasPublicDtor {
			fmt.Fprintf(b, "static const int32_t METHOD_%s_RELEASE_HANDLE = %d;\n", svcName, methodID)
		}
		b.WriteString("\n")
		serviceID++
	}
}

// ======================================
// Callback trampolines
// ======================================

// writeCallbackTrampolines emits a C++ class for each callback-candidate
// abstract class. The trampoline subclasses the abstract, stores a
// callback_id (provided by the Go side via FromCallback), and forwards
// each pure-virtual method through wasmify_callback_invoke. This is the
// reverse direction of the host→wasm bridge: the per-method
// `w_<svc>_<mid>` exports are Go→C++ entry points; these trampolines
// route C++→Go callbacks through the imported `wasmify_callback_invoke`.
//
// The supported signatures at this stage cover primitive params/returns,
// const std::string& params, std::string return, and void return. Any
// pure-virtual with an unsupported signature is forwarded via a stub
// implementation that aborts at runtime — enough to let the class link
// while keeping the surface honest about what's really wired through.
func writeCallbackTrampolines(b *strings.Builder, handleClasses map[string]*apispec.Class, classNames []string) {
	var candidates []*apispec.Class
	for _, qualName := range classNames {
		c := handleClasses[qualName]
		if isCallbackCandidateForBridge(c) {
			candidates = append(candidates, c)
		}
	}
	if len(candidates) == 0 {
		return
	}

	b.WriteString("// ======================================\n")
	b.WriteString("// Callback trampolines (C++ -> Go virtual dispatch)\n")
	b.WriteString("// ======================================\n\n")

	for _, c := range candidates {
		writeCallbackTrampoline(b, c)
	}
}

// isCallbackCandidateForBridge mirrors proto.isCallbackCandidate but
// lives in bridge.go. Walks ALL pure virtuals (not filterBridgeMethods)
// because the trampoline must override every one to be instantiable.
//
// Two paths produce a callback service:
//
//  1. **Abstract classes** are picked up automatically. C++ refuses to
//     instantiate a class with unimplemented pure virtuals, so the
//     author has signalled "must be subclassed" at the language level.
//     wasmify mirrors that signal one-for-one.
//  2. **Concrete classes opt in** via `bridge.CallbackClasses` in
//     `wasmify.json`. C++ has no language-level way to say "this
//     concrete class is a customisation hook"; the same syntactic
//     shape (concrete + virtuals) is used for both true subclass
//     targets like `TableValuedFunction` and pure data carriers like
//     every concrete `AST*` / `Resolved*` node. Auto-picking every
//     concrete with virtuals would balloon the generated surface, so
//     the user names the small set that actually needs subclassing.
//
// See `docs/callback-services.md` for the full design rationale,
// including why naming-based heuristics are not used.
//
// Exclusions in either path:
//   - Classes whose immediate base has no default constructor are
//     excluded ONLY when no constructor-forwarding trampoline can be
//     emitted (the generator forwards base ctor args automatically;
//     this is not a user-tunable knob).
//   - Classes where any pure-virtual has a signature the trampoline
//     can't emit a valid C++ override declaration for — e.g. nested
//     types from shortened clang spellings (`api::VisitResult`), or
//     names that resolveTypeName can't map to a known class. Emitting
//     broken overrides pollutes the whole api_bridge.cc compile.
func isCallbackCandidateForBridge(c *apispec.Class) bool {
	// Path A — C++-abstract classes are auto-eligible. The author
	// has language-level forced subclassing by leaving at least one
	// inherited pure virtual unimplemented; wasmify mirrors that
	// signal one-for-one. See docs/callback-services.md for the rule
	// in full.
	if c.IsAbstract {
		// Abstract classes whose immediate base lacks an accessible
		// default ctor are intentionally NOT auto-callback-eligible
		// here: the explicit opt-in path (CallbackClasses below)
		// generates forwarding ctors and is the supported way to
		// callback-enable such classes. Auto-enabling them would
		// also bring in any inherited `final` virtuals from
		// partially-implemented bases, which the apispec parser
		// does not currently flag as final and which would silently
		// produce illegal `override` declarations on the trampoline.
		if classNoDefaultCtor != nil && classNoDefaultCtor[c.QualName] {
			return isCallbackOptIn(c.QualName) && hasOverridableForOptIn(c)
		}
		// Every pure virtual must be declarable (otherwise the
		// trampoline can't satisfy the vtable and the subclass stays
		// abstract). Non-pure virtuals can be skipped on a per-method
		// basis in the override set — the base class impl still runs
		// for those.
		pure := collectInheritedPureVirtuals(c)
		if len(pure) == 0 {
			return false
		}
		for _, m := range pure {
			if !trampolineSignatureDeclarable(&m) {
				return false
			}
		}
		return true
	}

	// Path B — concrete classes opt in via wasmify.json's
	// `bridge.CallbackClasses`. Concrete + virtuals is C++'s shape
	// for both "customisation hook with default impl" (e.g.
	// TableValuedFunction.Resolve) and "data carrier with visitor
	// dispatch" (every concrete AST/Resolved node), and the
	// generator can't tell them apart structurally.
	if !isCallbackOptIn(c.QualName) {
		return false
	}
	overridable := collectInheritedOverridableVirtuals(c)
	if len(overridable) == 0 {
		return false
	}
	// Pure virtuals (rare on concrete classes — usually only when
	// inherited from a partially-implemented abstract base) must
	// remain declarable for the same vtable-satisfaction reason as
	// path A.
	for _, m := range overridable {
		if !m.IsPureVirtual {
			continue
		}
		if !trampolineSignatureDeclarable(&m) {
			return false
		}
	}
	return true
}

// isCallbackOptIn reports whether qualName appears in
// `bridge.CallbackClasses`. Concrete classes need this opt-in to
// become callback candidates (see isCallbackCandidateForBridge for
// the rationale).
func isCallbackOptIn(qualName string) bool {
	if qualName == "" {
		return false
	}
	for _, n := range bridgeConfig.CallbackClasses {
		if n == qualName {
			return true
		}
	}
	return false
}

// hasOverridableForOptIn validates that an opt-in class has at least
// one virtual method we can usefully override, and that any
// inherited pure virtuals can be declared. Same per-method
// declarability check as the auto-abstract path uses.
func hasOverridableForOptIn(c *apispec.Class) bool {
	overridable := collectInheritedOverridableVirtuals(c)
	if len(overridable) == 0 {
		return false
	}
	for _, m := range overridable {
		if !m.IsPureVirtual {
			continue
		}
		if !trampolineSignatureDeclarable(&m) {
			return false
		}
	}
	return true
}

// collectTrampolineMethods returns every public virtual reachable
// from c that can be safely declared + marshalled: pure virtuals
// must always appear (they're required for instantiability), plus
// non-pure virtuals whose signatures are declarable. Non-pure
// virtuals with undeclarable signatures are silently dropped — the
// base class implementation still runs for them.
func collectTrampolineMethods(c *apispec.Class) []apispec.Function {
	all := collectInheritedOverridableVirtuals(c)
	var result []apispec.Function
	for _, m := range all {
		if m.IsPureVirtual || trampolineSignatureDeclarable(&m) {
			result = append(result, m)
		}
	}
	return result
}

// collectInheritedPureVirtuals walks up the class hierarchy and
// returns every public pure virtual method reachable from c, with
// name-based de-duplication (a derived class's re-declaration of a
// base's pure virtual counts only once). Results are in stable order
// (by name) for deterministic emission.
func collectInheritedPureVirtuals(c *apispec.Class) []apispec.Function {
	return collectInheritedVirtuals(c, true)
}

// collectInheritedOverridableVirtuals returns every public virtual
// reachable from c (pure or not). The callback trampoline overrides
// all of them, so the Go impl has full control over each dispatch.
// Non-pure virtuals that the Go impl doesn't care about are expected
// to be routed through the DefaultXCallback stub that returns a
// "not found / not implemented" equivalent — essentially mirroring
// what most default base implementations in googlesql do anyway.
func collectInheritedOverridableVirtuals(c *apispec.Class) []apispec.Function {
	return collectInheritedVirtuals(c, false)
}

func collectInheritedVirtuals(c *apispec.Class, pureOnly bool) []apispec.Function {
	seen := make(map[string]bool)
	var result []apispec.Function
	var visit func(cls *apispec.Class, path map[string]bool)
	visit = func(cls *apispec.Class, path map[string]bool) {
		if cls == nil || path[cls.QualName] {
			return
		}
		path[cls.QualName] = true
		methods := disambiguateMethodNames(cls.Methods)
		for _, m := range methods {
			if !m.IsVirtual {
				continue
			}
			if pureOnly && !m.IsPureVirtual {
				continue
			}
			if m.Access != "" && m.Access != "public" {
				continue
			}
			if isSkippedMethod(m.Name) {
				continue
			}
			if m.IsRvalueRef {
				continue
			}
			key := m.Name + "|" + overloadSortKey(m)
			if seen[key] {
				continue
			}
			seen[key] = true
			result = append(result, m)
		}
		if classByQualName != nil {
			if cls.Parent != "" {
				visit(classByQualName[cls.Parent], path)
				if !strings.Contains(cls.Parent, "::") {
					if resolved := resolveTypeName(cls.Parent); resolved != cls.Parent {
						visit(classByQualName[resolved], path)
					}
				}
			}
			for _, p := range cls.Parents {
				visit(classByQualName[p], path)
			}
		}
	}
	visit(c, make(map[string]bool))
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		return overloadSortKey(result[i]) < overloadSortKey(result[j])
	})
	return result
}

// trampolineSignatureDeclarable reports whether we can emit a C++
// override declaration for the method that matches the base class's
// vtable slot. It's conservatively true only when all types resolve
// to known classes / primitives / strings — anything exotic (nested
// types, `api::...` shortened spellings from clang, reference-to-
// pointer combos, etc.) disqualifies it.
func trampolineSignatureDeclarable(m *apispec.Function) bool {
	if !trampolineTypeDeclarable(m.ReturnType) {
		return false
	}
	for _, p := range m.Params {
		if !trampolineTypeDeclarable(p.Type) {
			return false
		}
	}
	return true
}

func trampolineTypeDeclarable(t apispec.TypeRef) bool {
	if isErrorOnlyReturnType(t) {
		return true
	}
	if isViewOfStringType(t) {
		return true
	}
	if inner, ok := statusOrInnerType(t); ok {
		// `absl::StatusOr<T>` is declarable when T is a known
		// handle-class. The override return type is preserved
		// verbatim (with T resolved to its fully-qualified form)
		// so the trampoline matches the base vtable slot.
		resolved := resolveTypeName(strings.TrimSpace(inner))
		if resolved == "" || (resolved == inner && !strings.Contains(resolved, "::")) {
			return false
		}
		return true
	}
	if isOutputPointerHandle(t) {
		// T** output params are declarable — we preserve qual_type and
		// skip decoding them into the trampoline body (the Go adapter
		// carries the value out on the response).
		return true
	}
	switch t.Kind {
	case apispec.TypeVoid:
		return true
	case apispec.TypePrimitive, apispec.TypeEnum:
		// Pointer/reference to primitive/enum (e.g. `int* out`,
		// `int& out`) would need an output-primitive marshalling path
		// we don't emit yet — skip so the override isn't declared with
		// a mismatching signature.
		if t.IsPointer || t.IsRef {
			return false
		}
		return true
	case apispec.TypeString:
		return true
	case apispec.TypeHandle:
		// Must resolve to a known qualified name.
		if t.Name == "" {
			return false
		}
		resolved := resolveTypeName(t.Name)
		if resolved == "" || resolved == t.Name && !strings.Contains(resolved, "::") {
			// Unresolved short name → can't emit a valid override.
			return false
		}
		// Pointer-to-reference (`T*&`) not yet wired. T** handled by
		// the output-pointer path checked above; if we land here with
		// `**`, it means the type wasn't recognised as handle-output
		// (shouldn't normally happen but be conservative).
		qt := t.QualType
		if strings.Contains(qt, "*&") {
			return false
		}
		return true
	case apispec.TypeVector:
		// `const std::vector<T>&` and `std::vector<T>` are declarable
		// when the element type is itself declarable. Mutable vector
		// references (`std::vector<T>&`) are output-shaped and would
		// need a different marshalling story; we limit declarability
		// to the read-only forms.
		if t.Inner == nil {
			return false
		}
		if t.IsRef && !t.IsConst {
			return false
		}
		return trampolineTypeDeclarable(*t.Inner)
	}
	return false
}

func writeCallbackTrampoline(b *strings.Builder, c *apispec.Class) {
	trampolineName := protoMessageName(c.QualName) + "Trampoline"
	cppType := c.QualName
	// collectTrampolineMethods covers pure virtuals (mandatory) plus
	// any non-pure virtuals whose signature the trampoline can marshal.
	// Non-pure virtuals we skip keep their base class impl.
	methods := collectTrampolineMethods(c)
	ctors := collectTrampolineCtors(c)

	fmt.Fprintf(b, "class %s : public %s {\n", trampolineName, cppType)
	b.WriteString("public:\n")

	if len(ctors) == 0 {
		// Default-ctor path: the base either declares an accessible
		// default ctor (typical for abstract classes like Catalog,
		// Logger, etc.) or its implicit default ctor is reachable.
		// The trampoline only needs a callback_id; everything else
		// comes from the base's default initialisation.
		fmt.Fprintf(b, "    explicit %s(int32_t callback_id) : _callback_id(callback_id) {}\n", trampolineName)
	} else {
		// Forwarding-ctor path: one trampoline ctor per base ctor.
		// Each trampoline ctor takes the same arguments as the base
		// plus a trailing callback_id, then forwards via the
		// initializer list. This is what lets concrete classes whose
		// only constructors take arguments (e.g. TableValuedFunction)
		// be subclassed from Go without the bridge ever calling a
		// non-existent default ctor.
		for _, ctor := range ctors {
			writeTrampolineCtor(b, trampolineName, c.QualName, ctor)
		}
	}
	fmt.Fprintf(b, "    ~%s() override = default;\n", trampolineName)

	for i := range methods {
		writeTrampolineMethod(b, &methods[i], i)
	}

	b.WriteString("private:\n")
	b.WriteString("    int32_t _callback_id;\n")
	b.WriteString("};\n\n")
}

// collectTrampolineCtors returns every public constructor of c whose
// every parameter has a usable type. Used by writeCallbackTrampoline
// to emit one trampoline ctor variant per base ctor variant. If the
// class has only an implicit default ctor (no IsConstructor entries
// in api-spec), the returned slice is empty and the caller emits the
// no-arg trampoline ctor that delegates to the base's default ctor.
func collectTrampolineCtors(c *apispec.Class) []apispec.Function {
	var result []apispec.Function
	for _, m := range disambiguateMethodNames(c.Methods) {
		if !m.IsConstructor {
			continue
		}
		if m.Access != "" && m.Access != "public" {
			continue
		}
		if m.IsRvalueRef {
			continue
		}
		usable := true
		for _, p := range m.Params {
			if !isUsableType(p.Type) || !isInstantiableType(p.Type) {
				usable = false
				break
			}
		}
		if !usable {
			continue
		}
		result = append(result, m)
	}
	return result
}

// writeTrampolineCtor emits one forwarding constructor for the
// trampoline subclass. The signature is `(<base ctor args>, int32_t
// callback_id)`; the initializer list forwards each base-ctor arg by
// name and stores callback_id.
//
// Argument types are taken straight from the base ctor's qual_type
// (after fully qualifying class names so the bridge's flat namespace
// resolves them). Each argument is stored on the trampoline as-is —
// no marshalling is performed at construction time. The wasm
// dispatch (`writeHandleDispatch`'s FromCallback emit) is responsible
// for reading the args off the wire and passing them to this ctor.
func writeTrampolineCtor(b *strings.Builder, trampolineName, baseName string, ctor apispec.Function) {
	var declParts []string
	var fwdParts []string
	for i, p := range ctor.Params {
		name := paramVarName(p, i)
		ty := trampolineParamTypeDecl(p.Type)
		declParts = append(declParts, fmt.Sprintf("%s %s", ty, name))
		fwdParts = append(fwdParts, fmt.Sprintf("std::move(%s)", name))
	}
	declParts = append(declParts, "int32_t callback_id")

	fmt.Fprintf(b,
		"    %s(%s) : %s(%s), _callback_id(callback_id) {}\n",
		trampolineName,
		strings.Join(declParts, ", "),
		baseName,
		strings.Join(fwdParts, ", "),
	)
}

// trampolineParamTypeDecl returns the C++ type declaration string for
// a base-ctor parameter when used in the trampoline's forwarding
// ctor signature. Uses the qual_type verbatim where it's already
// fully qualified, otherwise resolves bare class names through
// classQualNames so the bridge's flat namespace context can find
// them.
func trampolineParamTypeDecl(t apispec.TypeRef) string {
	qt := strings.TrimSpace(t.QualType)
	if qt == "" {
		qt = t.Name
	}
	// Strip rvalue-ref / lvalue-ref / pointer markers, qualify the
	// inner class, then put them back. We don't try to fully parse
	// the qual_type; we just pull the bare class identifier off the
	// front and resolve it.
	stripped := stripCppTypeQualifiers(qt)
	resolved := resolveTypeName(stripped)
	if resolved == "" {
		resolved = stripped
	}
	if resolved == stripped {
		return qt
	}
	// Replace the first occurrence of the bare name with the
	// resolved form. This handles both `T`, `const T&`, `T*`, etc.
	// without needing to know the exact decoration.
	return strings.Replace(qt, stripped, resolved, 1)
}

// writeTrampolineMethod emits the override for a single pure-virtual
// that marshals args, calls wasmify_callback_invoke, and unpacks the
// response. The override's declared C++ signature is taken verbatim
// from the api-spec's qual_type so the subclass satisfies the vtable
// slot even for methods whose types aren't wired to the marshalling
// layer — those still compile and link; they just __builtin_trap when
// actually invoked, rather than silently returning garbage.
func writeTrampolineMethod(b *strings.Builder, m *apispec.Function, methodID int) {
	retType := trampolineReturnDecl(m.ReturnType)
	paramDecls := trampolineParamDecls(m.Params)
	constQual := ""
	if m.IsConst {
		constQual = " const"
	}

	// `m.Name` may be the disambiguated proto-side name (e.g.
	// `Accept2` for an overloaded virtual), but the C++ override
	// must match the base class's actual method name to satisfy the
	// vtable. Use OriginalName when present.
	cppName := m.Name
	if m.OriginalName != "" {
		cppName = m.OriginalName
	}
	fmt.Fprintf(b, "    %s %s(%s)%s override {\n", retType, cppName, paramDecls, constQual)

	// Check if every signature piece is supported; if not, abort.
	if !trampolineSignatureSupported(m) {
		b.WriteString("        // Unsupported signature for callback trampoline.\n")
		b.WriteString("        __builtin_trap();\n")
		// __builtin_trap is [[noreturn]] so clang doesn't require a
		// follow-up return statement.
		b.WriteString("    }\n")
		return
	}

	// Serialize inputs into a ProtoWriter. Output handle params
	// (T** or smart-pointer-by-pointer) are skipped here and
	// captured on the response side instead.
	b.WriteString("        ProtoWriter _pw;\n")
	for i, p := range m.Params {
		if isCallbackOutputParam(p.Type) {
			continue
		}
		fieldNum := i + 1
		varName := paramVarName(p, i)
		writeTrampolineParamWrite(b, p, fieldNum, varName, "        ")
	}

	fmt.Fprintf(b, "        int64_t _rc = wasmify_callback_invoke(_callback_id, %d, _pw.data_, _pw.size_);\n", methodID)
	b.WriteString("        uintptr_t _resp_ptr = static_cast<uintptr_t>(static_cast<uint64_t>(_rc) >> 32);\n")
	b.WriteString("        int32_t _resp_len = static_cast<int32_t>(_rc & 0xFFFFFFFFu);\n")

	// If the method has output handle params, decode them from the
	// response and write through.
	hasOutputPtrs := false
	for _, p := range m.Params {
		if isCallbackOutputParam(p.Type) {
			hasOutputPtrs = true
			break
		}
	}
	if hasOutputPtrs {
		b.WriteString("        std::string _err_msg;\n")
		b.WriteString("        if (_resp_ptr != 0) {\n")
		b.WriteString("            ProtoReader _pr(reinterpret_cast<const void*>(_resp_ptr), _resp_len);\n")
		b.WriteString("            while (_pr.next()) {\n")
		b.WriteString("                switch (_pr.field()) {\n")
		for i, p := range m.Params {
			if !isCallbackOutputParam(p.Type) {
				continue
			}
			fieldNum := i + 1
			varName := paramVarName(p, i)
			qt := strings.TrimSpace(p.Type.QualType)
			// Two output shapes:
			//   - `T**` / `const T**`: writes a raw pointer.
			//   - `unique_ptr<T>*` / `shared_ptr<T>*`: writes a fresh
			//     smart-pointer wrapping the raw pointer.
			peeled := strings.TrimSpace(strings.TrimSuffix(qt, "*"))
			peeled = strings.TrimPrefix(peeled, "const ")
			peeled = strings.TrimSpace(peeled)
			isSmart := isSmartPointerType(peeled)

			fmt.Fprintf(b, "                    case %d: {\n", fieldNum)
			// Container output (set<T*>& / vector<T*>&): each
			// repeated wire entry is a single handle that we
			// insert into the caller-allocated container.
			if isCallbackOutputContainer(p.Type) {
				inner := callbackOutputInnerName(p.Type)
				resolvedInner := resolveTypeName(inner)
				if resolvedInner == "" {
					resolvedInner = inner
				}
				// Element pointer type: const T* by default
				// (read-only borrowed view); flip when the
				// container's parameterised element drops const.
				elemPtr := "const " + resolvedInner + "*"
				if t := p.Type.Inner; t != nil && !t.IsConst {
					elemPtr = resolvedInner + "*"
				}
				b.WriteString("                        uint64_t _p = read_handle_ptr(_pr);\n")
				inserter := "insert"
				if p.Type.Kind == apispec.TypeVector {
					inserter = "push_back"
				}
				fmt.Fprintf(b, "                        %s.%s(reinterpret_cast<%s>(_p));\n", varName, inserter, elemPtr)
				b.WriteString("                        break;\n")
				b.WriteString("                    }\n")
				continue
			}
			b.WriteString("                        uint64_t _p = read_handle_ptr(_pr);\n")
			if isSmart {
				inner := smartPointerInner(peeled)
				inner = strings.TrimPrefix(inner, "const ")
				inner = strings.TrimSpace(inner)
				resolvedInner := resolveTypeName(inner)
				if resolvedInner == "" {
					resolvedInner = inner
				}
				wrapperPrefix := peeled
				if open := strings.Index(peeled, "<"); open >= 0 {
					wrapperPrefix = peeled[:open]
				}
				fmt.Fprintf(b, "                        if (%s != nullptr) *%s = %s<%s>(reinterpret_cast<%s*>(_p));\n",
					varName, varName, wrapperPrefix, resolvedInner, resolvedInner)
			} else {
				// `T**` or `T*&` shape: strip the trailing `&` (if any),
				// then both stars, plus one optional const. The two
				// shapes differ only in the LHS of the assignment:
				// `T**` is a plain pointer (deref to write), `T*&` is
				// a reference (assign directly).
				peeled := strings.TrimSpace(strings.TrimSuffix(qt, "&"))
				stripped := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(peeled, "*")), "*"))
				hasConst := strings.HasPrefix(stripped, "const ")
				stripped = strings.TrimSpace(strings.TrimPrefix(stripped, "const "))
				resolved := resolveTypeName(stripped)
				if resolved == "" {
					resolved = stripped
				}
				ptrType := resolved + "*"
				if hasConst {
					ptrType = "const " + ptrType
				}
				if isReferenceToPointerHandle(p.Type) {
					fmt.Fprintf(b, "                        %s = reinterpret_cast<%s>(_p);\n", varName, ptrType)
				} else {
					fmt.Fprintf(b, "                        if (%s != nullptr) *%s = reinterpret_cast<%s>(_p);\n", varName, varName, ptrType)
				}
			}
			b.WriteString("                        break;\n")
			b.WriteString("                    }\n")
		}
		b.WriteString("                    case 15: _err_msg = _pr.read_string(); break;\n")
		b.WriteString("                    default: _pr.skip();\n")
		b.WriteString("                }\n")
		b.WriteString("            }\n")
		b.WriteString("            wasm_free(reinterpret_cast<void*>(_resp_ptr));\n")
		b.WriteString("        }\n")
		if isErrorOnlyReturnType(m.ReturnType) {
			name := matchErrorOnlyReturnType(m.ReturnType)
			if spec, ok := bridgeConfig.ErrorReconstruct[name]; ok {
				errExpr := strings.ReplaceAll(spec.ErrorExpr, "{err_msg}", "_err_msg")
				fmt.Fprintf(b, "        if (!_err_msg.empty()) return %s;\n", errExpr)
				fmt.Fprintf(b, "        return %s;\n", spec.OkExpr)
			}
		} else if m.ReturnType.Kind == apispec.TypeVoid {
			// Nothing to return.
		} else {
			// Other return kinds don't combine well with output ptrs
			// in practice; fall back to trap for safety.
			b.WriteString("        __builtin_trap();\n")
		}
		b.WriteString("    }\n")
		return
	}

	switch {
	case m.ReturnType.Kind == apispec.TypeVoid:
		b.WriteString("        if (_resp_ptr != 0) wasm_free(reinterpret_cast<void*>(_resp_ptr));\n")
	case isErrorOnlyReturnType(m.ReturnType):
		// Wire format: empty response = OK; any `string error = 15`
		// → the ErrorExpr reconstruction template with that message.
		// The existing error-field convention matches pbExtractError on
		// the Go side, so the user's Go impl can simply `return err`.
		writeErrorOnlyTrampolineRead(b, m.ReturnType, "        ")
	default:
		if _, ok := statusOrInnerType(m.ReturnType); ok {
			writeStatusOrTrampolineRead(b, m.ReturnType, "        ")
		} else {
			writeTrampolineResultRead(b, m.ReturnType, "        ")
		}
	}
	b.WriteString("    }\n")
}

// writeStatusOrTrampolineRead emits the C++ for a trampoline override
// whose return is a Status-or-value wrapper such as
// `absl::StatusOr<T>`. The wire format mirrors the proto schema
// generated by writeCallbackService: a single `<T> result = 1` field
// (a handle submessage) plus the standard `string error = 15`. The
// reconstruction is:
//
//   - Drain the response into `_err_msg` (field 15) and
//     `_result_handle` (field 1).
//   - If the error message is non-empty, return the configured
//     `ErrorExpr` for the wrapper type — this implicitly converts
//     to the wrapper (e.g. `absl::InternalError(...)` ⇢
//     `absl::StatusOr<T>`).
//   - If the handle is zero, surface a synthetic
//     "callback returned no value" error so the caller sees a clear
//     failure rather than a use-after-free.
//   - Otherwise dereference the handle and copy-return the value.
//
// Ownership: the handle was produced by the Go callback adapter,
// which takes the rawPtr of a Go-side wrapper. The C++ side does
// NOT take ownership — it copy-constructs the value and returns
// the copy. The Go-side wrapper's GC finalizer continues to manage
// the original storage.
func writeStatusOrTrampolineRead(b *strings.Builder, ret apispec.TypeRef, indent string) {
	inner, _ := statusOrInnerType(ret)
	resolvedInner := resolveTypeName(strings.TrimSpace(inner))
	if resolvedInner == "" {
		resolvedInner = inner
	}
	wrapperName := matchErrorType(strings.TrimSpace(ret.QualType))
	_ = wrapperName // recognised already, kept for clarity
	wrapperBase := matchErrorTypeBase(ret)
	spec, hasSpec := bridgeConfig.ErrorReconstruct[wrapperBase]

	fmt.Fprintf(b, "%sstd::string _err_msg;\n", indent)
	fmt.Fprintf(b, "%suint64_t _result_handle = 0;\n", indent)
	fmt.Fprintf(b, "%sif (_resp_ptr != 0) {\n", indent)
	fmt.Fprintf(b, "%s    ProtoReader _pr(reinterpret_cast<const void*>(_resp_ptr), _resp_len);\n", indent)
	fmt.Fprintf(b, "%s    while (_pr.next()) {\n", indent)
	fmt.Fprintf(b, "%s        switch (_pr.field()) {\n", indent)
	fmt.Fprintf(b, "%s        case 1: _result_handle = read_handle_ptr(_pr); break;\n", indent)
	fmt.Fprintf(b, "%s        case 15: _err_msg = _pr.read_string(); break;\n", indent)
	fmt.Fprintf(b, "%s        default: _pr.skip(); break;\n", indent)
	fmt.Fprintf(b, "%s        }\n", indent)
	fmt.Fprintf(b, "%s    }\n", indent)
	fmt.Fprintf(b, "%s    wasm_free(reinterpret_cast<void*>(_resp_ptr));\n", indent)
	fmt.Fprintf(b, "%s}\n", indent)

	if hasSpec {
		errExpr := strings.ReplaceAll(spec.ErrorExpr, "{err_msg}", "_err_msg")
		fmt.Fprintf(b, "%sif (!_err_msg.empty()) return %s;\n", indent, errExpr)
		fmt.Fprintf(b, "%sif (_result_handle == 0) return %s;\n",
			indent,
			strings.ReplaceAll(spec.ErrorExpr, "{err_msg}", "std::string(\"callback returned no value\")"))
	} else {
		// No reconstruct spec — fall back to a hard trap so the
		// missing config surfaces as a build/test failure rather
		// than silent garbage.
		fmt.Fprintf(b, "%sif (!_err_msg.empty()) __builtin_trap();\n", indent)
		fmt.Fprintf(b, "%sif (_result_handle == 0) __builtin_trap();\n", indent)
	}
	// std::move so move-only inner types (e.g. `unique_ptr<T>`)
	// can return; copy-constructible types are unaffected because
	// the StatusOr<T> ctor still picks the move overload first.
	fmt.Fprintf(b, "%sreturn std::move(*reinterpret_cast<%s*>(_result_handle));\n", indent, resolvedInner)
}

// matchErrorTypeBase returns the configured ErrorTypes key (without
// any template tail) that t matches. e.g. for
// `absl::StatusOr<TVFSignature>` returns `"absl::StatusOr"`. Used to
// look up the reconstruction spec for templated error wrappers.
func matchErrorTypeBase(t apispec.TypeRef) string {
	name := strings.TrimSpace(t.QualType)
	if name == "" {
		name = t.Name
	}
	name = strings.TrimPrefix(name, "const ")
	name = strings.TrimSpace(strings.TrimSuffix(name, "&"))
	name = strings.TrimSpace(strings.TrimSuffix(name, "*"))
	for typeName := range bridgeConfig.ErrorTypes {
		if name == typeName {
			return typeName
		}
		if !strings.Contains(typeName, "<") && strings.HasPrefix(name, typeName+"<") {
			return typeName
		}
	}
	return ""
}

// writeErrorOnlyTrampolineRead emits the C++ that reads the standard
// wire error field and reconstructs the configured error type via
// ErrorReconstruct (OkExpr / ErrorExpr templates).
func writeErrorOnlyTrampolineRead(b *strings.Builder, ret apispec.TypeRef, indent string) {
	name := matchErrorOnlyReturnType(ret)
	spec, ok := bridgeConfig.ErrorReconstruct[name]
	if !ok {
		// No reconstruction template — fall back to draining the
		// response. This should only happen if config is incomplete.
		fmt.Fprintf(b, "%sif (_resp_ptr != 0) wasm_free(reinterpret_cast<void*>(_resp_ptr));\n", indent)
		return
	}
	fmt.Fprintf(b, "%sstd::string _err_msg;\n", indent)
	fmt.Fprintf(b, "%sif (_resp_ptr != 0) {\n", indent)
	fmt.Fprintf(b, "%s    ProtoReader _pr(reinterpret_cast<const void*>(_resp_ptr), _resp_len);\n", indent)
	fmt.Fprintf(b, "%s    while (_pr.next()) {\n", indent)
	fmt.Fprintf(b, "%s        if (_pr.field() == 15) _err_msg = _pr.read_string();\n", indent)
	fmt.Fprintf(b, "%s        else _pr.skip();\n", indent)
	fmt.Fprintf(b, "%s    }\n", indent)
	fmt.Fprintf(b, "%s    wasm_free(reinterpret_cast<void*>(_resp_ptr));\n", indent)
	fmt.Fprintf(b, "%s}\n", indent)
	errExpr := strings.ReplaceAll(spec.ErrorExpr, "{err_msg}", "_err_msg")
	fmt.Fprintf(b, "%sif (!_err_msg.empty()) return %s;\n", indent, errExpr)
	fmt.Fprintf(b, "%sreturn %s;\n", indent, spec.OkExpr)
}

// trampolineSignatureSupported returns true when every param and the
// return type are shapes the trampoline knows how to marshal.
func trampolineSignatureSupported(m *apispec.Function) bool {
	for _, p := range m.Params {
		if !trampolineTypeSupported(p.Type) {
			return false
		}
	}
	return trampolineReturnSupported(m.ReturnType)
}

func trampolineTypeSupported(t apispec.TypeRef) bool {
	if isViewOfStringType(t) {
		return true
	}
	if isCallbackOutputParam(t) {
		// Output handles — both `T**` and the smart-pointer-by-
		// pointer idiom. The trampoline body skips reading them
		// off the wire (they live on the response side) and the
		// per-element write loop below handles serialisation.
		return true
	}
	if _, ok := statusOrInnerType(t); ok {
		// `absl::StatusOr<T>` (or any ErrorTypes-templated wrapper)
		// where T is a usable handle-class. The wire format is
		// `<HandleMsg> result = 1; string error = 15` and the
		// trampoline reconstructs the StatusOr<T> from those two
		// fields. See writeTrampolineStatusOrRead below.
		return true
	}
	switch t.Kind {
	case apispec.TypePrimitive:
		return primitiveToPbType(t.Name) != ""
	case apispec.TypeString:
		// std::string is marshalable. Views / compressed strings
		// (configured via UnsupportedStringTypes) aren't — they need
		// backing-store lifetime we can't guarantee across callbacks.
		// `ExtraStringTypes` opt-ins (e.g. absl::string_view) are
		// reclassified to TypeString upstream; they are marshalable
		// across the trampoline because the std::string read from
		// the wire converts implicitly to the view, and a view
		// argument coming from C++ converts to std::string before
		// it hits the wire. Honour the opt-in here so the
		// `string_view` substring used to reject unrelated types
		// (e.g. an unowned compressed-string view) doesn't catch
		// them in the same net.
		qt := strings.TrimSpace(t.QualType)
		for _, e := range bridgeConfig.ExtraStringTypes {
			if qt == e || t.Name == e {
				return true
			}
		}
		return !isUnsupportedStringType(t.QualType)
	case apispec.TypeHandle:
		return true
	case apispec.TypeVector:
		// `const std::vector<T>&` (and by-value forms) carry a list
		// of repeated elements on the wire — supported as long as
		// the inner element type is itself trampoline-supported.
		// Mutable vector references are output-shaped: handled via
		// isCallbackOutputContainer earlier (caught by
		// isCallbackOutputParam).
		if t.Inner == nil {
			return false
		}
		return trampolineTypeSupported(*t.Inner)
	case apispec.TypeVoid:
		return false
	}
	return false
}

// isViewOfStringType recognises "view of std::string" (e.g.
// absl::Span<const std::string>) — a shape trampolines support
// specifically because the backing vector<string> is reconstructable.
// View kinds come from BridgeConfig.ValueViewTypes.
func isViewOfStringType(t apispec.TypeRef) bool {
	if !matchesValueViewType(t.QualType) {
		return false
	}
	elem := extractTemplateArgFromQualType(t.QualType)
	if elem == "" {
		return false
	}
	elem = strings.TrimSpace(elem)
	elem = strings.TrimPrefix(elem, "const ")
	elem = strings.TrimSpace(elem)
	// Strip leading `::` (abs-scoped std::string).
	elem = strings.TrimPrefix(elem, "::")
	return elem == "std::string" || elem == "string"
}

// isOutputPointerHandle recognises `T**` (double-indirection) handle
// params — a convention for pure-output parameters that the callee
// writes. We move these from the request onto the response so the Go
// impl returns the handle rather than taking it.
func isOutputPointerHandle(t apispec.TypeRef) bool {
	if t.Kind != apispec.TypeHandle {
		return false
	}
	qt := strings.TrimSpace(t.QualType)
	if strings.Contains(qt, "**") {
		return true
	}
	// Reference-to-pointer (`T*&` / `T* &`) is the same output shape
	// as `T**` — caller passes a slot, callee writes the pointer.
	// Recognising it here lets the trampoline declarability check,
	// the callback request/response split, and the dispatch
	// emit-loop all treat it uniformly.
	if t.IsRef && (strings.Contains(qt, "*&") || strings.Contains(qt, "* &")) {
		return true
	}
	return false
}

// isReferenceToPointerHandle reports whether t is the `T*&` /
// `T* &` shape specifically (output via reference rather than
// `T**`). Used by the trampoline body emitter and the dispatch
// builder to pick the correct deref/assign syntax: `*var = ...` for
// `T**`, `var = ...` for `T*&`.
func isReferenceToPointerHandle(t apispec.TypeRef) bool {
	if t.Kind != apispec.TypeHandle {
		return false
	}
	if !t.IsRef {
		return false
	}
	qt := strings.TrimSpace(t.QualType)
	return strings.Contains(qt, "*&") || strings.Contains(qt, "* &")
}

// callbackOutputInnerName extracts the bare inner class name from
// a callback output parameter — strip the trailing `*` /  `**` plus
// any smart-pointer wrapper. e.g.
//
//	`const ResolvedNode**`              → `ResolvedNode`
//	`std::unique_ptr<TVFSignature>*`    → `TVFSignature`
//	`std::shared_ptr<const Foo>*`       → `Foo`
//
// Used by writeCallbackService to pick the response-field message
// type for an output param. The caller is responsible for verifying
// the param is an output param via isCallbackOutputParam.
func callbackOutputInnerName(t apispec.TypeRef) string {
	// Mutable container reference: extract the (already-resolved)
	// element type.
	if isCallbackOutputContainer(t) {
		// set-like first (works on TypeHandle/TypeValue with the
		// recognised template prefix; clang doesn't populate Inner
		// for these): setLikeContainerInfo returns
		// (containerType, elementType, ok) — we want the element.
		if _, elem, ok := setLikeContainerInfo(t); ok && elem != "" {
			elem = strings.TrimSpace(elem)
			elem = strings.TrimPrefix(elem, "const ")
			elem = strings.TrimSpace(strings.TrimSuffix(elem, "*"))
			return strings.TrimSpace(elem)
		}
		// vector<T>& — Inner is populated by classifyType.
		if t.Inner != nil && t.Inner.Name != "" {
			n := strings.TrimSpace(t.Inner.Name)
			n = strings.TrimPrefix(n, "const ")
			n = strings.TrimSuffix(n, "*")
			return strings.TrimSpace(n)
		}
	}
	qt := strings.TrimSpace(t.QualType)
	if qt == "" {
		qt = t.Name
	}
	// Reference-to-pointer (`T*&` / `T* &`): peel the reference
	// marker and one trailing `*` to reveal the bare T.
	qt = strings.TrimSpace(strings.TrimSuffix(qt, "&"))
	// Peel one trailing `*` (the writability marker).
	stripped := strings.TrimSpace(strings.TrimSuffix(qt, "*"))
	stripped = strings.TrimPrefix(stripped, "const ")
	stripped = strings.TrimSpace(stripped)
	if isSmartPointerType(stripped) {
		inner := smartPointerInner(stripped)
		inner = strings.TrimPrefix(inner, "const ")
		inner = strings.TrimSpace(inner)
		return inner
	}
	// `T**` shape: strip the second `*`.
	stripped = strings.TrimSpace(strings.TrimSuffix(stripped, "*"))
	stripped = strings.TrimPrefix(stripped, "const ")
	return strings.TrimSpace(stripped)
}

// isCallbackOutputParam reports whether a callback method parameter
// is an output slot — wider than `isOutputPointerHandle` because
// the callback request/response split also moves smart-pointer-by-
// pointer out-params (`unique_ptr<T>*`, `shared_ptr<T>*`) onto the
// response side. Keeping these two predicates separate avoids
// changing the trampoline-body emit path, which relies on
// `isOutputPointerHandle`'s strict `T**` semantics for its
// param-write loop.
func isCallbackOutputParam(t apispec.TypeRef) bool {
	if isOutputPointerHandle(t) {
		return true
	}
	// Mutable container reference (`set<T>&`, `vector<T>&`,
	// `flat_hash_set<T>&`) is an output: the callee fills the
	// caller-allocated container. The trampoline marshals the
	// produced items on the response, the C++ trampoline body
	// inserts each into the local set/vector before returning.
	if isCallbackOutputContainer(t) {
		return true
	}
	if t.Kind != apispec.TypeHandle {
		return false
	}
	qt := strings.TrimSpace(t.QualType)
	if !t.IsPointer || t.IsRef {
		return false
	}
	stripped := strings.TrimSpace(strings.TrimSuffix(qt, "*"))
	stripped = strings.TrimPrefix(stripped, "const ")
	stripped = strings.TrimSpace(stripped)
	return isSmartPointerType(stripped)
}

// isCallbackOutputOwning reports whether the C++ output parameter
// shape transfers ownership of the produced handle to the C++ side
// (the trampoline wraps the wire-decoded raw pointer in a fresh
// `std::unique_ptr<T>` / `std::shared_ptr<T>` and writes through to
// the caller's slot, so the C++ side will eventually delete it).
//
// Raw-pointer output shapes (`T**`, `T*&`) are NOT owning — those
// represent borrowed handles where the callee just hands a pointer
// back; the Go callback retains ownership and its finalizer must
// still free the underlying object.
//
// The plugin uses this signal (carried over via the
// `wasm_take_ownership` field option on the response message) to
// decide whether to call `clearPtr()` on the returned handle after
// writing it to the wire. clearPtr-on-borrowed would leak the
// memory; clearPtr-omitted-on-owning would double-free.
func isCallbackOutputOwning(t apispec.TypeRef) bool {
	if t.Kind != apispec.TypeHandle {
		return false
	}
	qt := strings.TrimSpace(t.QualType)
	if !t.IsPointer || t.IsRef {
		return false
	}
	stripped := strings.TrimSpace(strings.TrimSuffix(qt, "*"))
	stripped = strings.TrimPrefix(stripped, "const ")
	stripped = strings.TrimSpace(stripped)
	return isSmartPointerType(stripped)
}

// isCallbackOutputContainer reports whether t is a mutable
// reference to a set / vector / map of pointers — the canonical
// "fill this caller-allocated collection" output shape used by
// virtual methods like `Catalog::GetTables(vector<Table*>&)` or
// `PropertyGraph::GetNodeTables(flat_hash_set<const NodeTable*>&)`.
func isCallbackOutputContainer(t apispec.TypeRef) bool {
	if !t.IsRef || t.IsConst {
		return false
	}
	switch t.Kind {
	case apispec.TypeVector:
		return t.Inner != nil
	case apispec.TypeValue, apispec.TypeHandle:
		// set-like containers come through as TypeValue / TypeHandle
		// because clang doesn't classify them as TypeVector. Use the
		// project's set-like recogniser if available.
		if _, _, ok := setLikeContainerInfo(t); ok {
			return true
		}
	}
	return false
}

func trampolineReturnSupported(t apispec.TypeRef) bool {
	if isErrorOnlyReturnType(t) {
		return true
	}
	if _, ok := statusOrInnerType(t); ok {
		return true
	}
	if isViewOfStringType(t) {
		// `absl::Span<const std::string>` — the trampoline
		// reconstructs a vector<string> from the wire and returns
		// a Span over it. See writeTrampolineResultRead.
		return true
	}
	switch t.Kind {
	case apispec.TypeVoid:
		return true
	case apispec.TypePrimitive:
		return primitiveToPbType(t.Name) != ""
	case apispec.TypeString:
		if t.IsPointer {
			return false
		}
		return !isUnsupportedStringType(t.QualType)
	case apispec.TypeHandle:
		return true
	}
	return false
}

// statusOrInnerType extracts the template argument T of a
// `BridgeConfig.ErrorTypes`-templated wrapper (e.g.
// `absl::StatusOr<TVFSignature>` → `TVFSignature`). Returns
// ("", false) for plain non-templated error types like `absl::Status`
// (which carry no value) and for non-error types.
//
// Used by the trampoline writer to recognise and round-trip
// "Status-or-value" returns: the wire format is `<HandleMsg> result
// = 1; string error = 15`, mirrored on the Go callback side via
// `(*T, error)`. The trampoline body reads both fields and either
// returns `absl::InternalError(err)` (which implicitly converts to
// the StatusOr<T>) or constructs the StatusOr<T> from a copy of
// the heap-allocated T whose handle the Go adapter shipped back.
//
// The recognition is library-agnostic: any name-prefix entry in
// `BridgeConfig.ErrorTypes` participates, so a project's own
// status-or-value wrappers (e.g. `myapp::Result<T>`) are picked up
// the same way as abseil's `absl::StatusOr`.
func statusOrInnerType(t apispec.TypeRef) (string, bool) {
	name := strings.TrimSpace(t.QualType)
	if name == "" {
		name = t.Name
	}
	name = strings.TrimPrefix(name, "const ")
	name = strings.TrimSpace(strings.TrimSuffix(name, "&"))
	name = strings.TrimSpace(strings.TrimSuffix(name, "*"))
	name = strings.TrimSpace(name)

	if !strings.Contains(name, "<") {
		return "", false
	}
	if matchErrorType(name) == "" {
		return "", false
	}
	open := strings.Index(name, "<")
	inner := name[open+1:]
	if end := strings.LastIndex(inner, ">"); end >= 0 {
		inner = inner[:end]
	}
	inner = strings.TrimSpace(inner)
	if inner == "" {
		return "", false
	}
	return inner, true
}

// writeStatusOrSuccessExpr emits the C++ statement that writes the
// payload of a StatusOr-like wrapper to `fieldNum` on the success
// branch. `inner` is the wrapper's template argument spelling (e.g.
// "const Type *", "std::unique_ptr<T>", "bool"); `varName` is the
// local holding the wrapper. The success branch dereferences via
// `*varName`, mirroring how absl::StatusOr exposes its payload.
//
// Returns "" if the inner type isn't recognised, in which case the
// caller should fall back to emitting only the error branch (the
// safest degradation — the Go side observes (nil, nil) instead of a
// crash). Adding a case here unbreaks the corresponding family of
// bridge exports.
func writeStatusOrSuccessExpr(inner string, fieldNum int, varName string) string {
	inner = strings.TrimSpace(inner)
	if inner == "" {
		return ""
	}

	// Pointer-to-handle (`T*`, `const T*`): the most common case for
	// factory-style StatusOr returns. The wire format is a single
	// handle field; the Go side reads it as a typed handle pointer.
	if strings.HasSuffix(inner, "*") {
		return fmt.Sprintf("_pw.write_handle(%d, reinterpret_cast<uint64_t>(*%s));", fieldNum, varName)
	}

	// Smart pointers — the payload is a unique_ptr / shared_ptr<T>
	// owning a freshly-constructed object. unique_ptr transfers sole
	// ownership across the boundary via release(); shared_ptr must
	// preserve joint ownership, so heap-allocate a copy of the
	// shared_ptr.
	if isSharedPointerType(inner) {
		return fmt.Sprintf("_pw.write_handle(%d, reinterpret_cast<uint64_t>(new auto(std::move(*%s))));", fieldNum, varName)
	}
	if isUniquePointerType(inner) {
		return fmt.Sprintf("_pw.write_handle(%d, reinterpret_cast<uint64_t>((*%s).release()));", fieldNum, varName)
	}

	// Primitives — the response message uses the matching scalar
	// field. cppPrimitiveType maps unknown names to "int64_t", so
	// gate the lookup on a known-primitive set.
	switch inner {
	case "bool":
		return fmt.Sprintf("_pw.write_bool(%d, *%s);", fieldNum, varName)
	case "float":
		return fmt.Sprintf("_pw.write_float(%d, *%s);", fieldNum, varName)
	case "double", "long double":
		return fmt.Sprintf("_pw.write_double(%d, *%s);", fieldNum, varName)
	case "int", "int32_t", "short", "int16_t", "char", "int8_t", "signed char":
		return fmt.Sprintf("_pw.write_int32(%d, static_cast<int32_t>(*%s));", fieldNum, varName)
	case "unsigned int", "uint32_t", "unsigned short", "uint16_t", "unsigned char", "uint8_t":
		return fmt.Sprintf("_pw.write_uint32(%d, static_cast<uint32_t>(*%s));", fieldNum, varName)
	case "long", "long long", "int64_t", "ssize_t", "ptrdiff_t", "intptr_t":
		return fmt.Sprintf("_pw.write_int64(%d, static_cast<int64_t>(*%s));", fieldNum, varName)
	case "unsigned long", "unsigned long long", "uint64_t", "size_t", "uintptr_t":
		return fmt.Sprintf("_pw.write_uint64(%d, static_cast<uint64_t>(*%s));", fieldNum, varName)
	}

	// std::string payload.
	if inner == "std::string" || inner == "string" {
		return fmt.Sprintf("_pw.write_string(%d, std::string(*%s));", fieldNum, varName)
	}

	// By-value class payload — heap-allocate a copy via move so the
	// emitted handle outlives the StatusOr local.
	return fmt.Sprintf("_pw.write_handle(%d, reinterpret_cast<uint64_t>(new auto(std::move(*%s))));", fieldNum, varName)
}

// trampolineTypeDecl returns the C++ type used in the override's
// declaration. It resolves the short name to its fully qualified form
// (so `Column*` becomes `googlesql::Column*` — important because the
// trampoline class lives in global namespace) and reapplies the
// const/pointer/reference qualifiers from the TypeRef.
func trampolineTypeDecl(t apispec.TypeRef) string {
	// Error-only return types (e.g. absl::Status) are classified as
	// handles by the parser; preserve the qual_type verbatim so the
	// override declaration matches the base's vtable signature.
	if name := matchErrorOnlyReturnType(t); name != "" {
		return name
	}
	if inner, ok := statusOrInnerType(t); ok {
		// Preserve the wrapper spelling (e.g. `absl::StatusOr<...>`)
		// verbatim, but qualify the inner type so the override
		// declaration resolves correctly in the bridge's flat
		// namespace. Without this `absl::StatusOr<TVFSignature>`
		// would compile against the wrong (or no) `TVFSignature`
		// in scope.
		resolvedInner := resolveTypeName(strings.TrimSpace(inner))
		if resolvedInner == "" {
			resolvedInner = inner
		}
		qt := strings.TrimSpace(t.QualType)
		if qt == "" {
			qt = t.Name
		}
		if resolvedInner != inner {
			qt = strings.Replace(qt, inner, resolvedInner, 1)
		}
		return qt
	}
	if isViewOfStringType(t) {
		// Keep qual_type verbatim so `const View<const std::string>&`
		// vs `View<const std::string>` both round-trip unchanged.
		if t.QualType != "" {
			return t.QualType
		}
	}
	if isOutputPointerHandle(t) {
		// Preserve qual_type (`const T**` or `const T*&`) verbatim;
		// fully-qualify the inner class so the override matches the
		// base's vtable slot. We branch on which output shape the
		// signature uses — both shapes share the same recogniser so
		// the trampoline body emitter can treat them uniformly, but
		// the override DECLARATION must keep the exact spelling.
		qt := strings.TrimSpace(t.QualType)
		isRefToPtr := isReferenceToPointerHandle(t)
		// Peel the trailing decorator: `&` for `T*&`, second `*` for
		// `T**`. Then peel one more `*` and any leading `const`.
		if isRefToPtr {
			qt = strings.TrimSpace(strings.TrimSuffix(qt, "&"))
		}
		stripped := strings.TrimSpace(strings.TrimSuffix(qt, "*"))
		stripped = strings.TrimSpace(strings.TrimSuffix(stripped, "*"))
		hasConst := strings.HasPrefix(stripped, "const ")
		stripped = strings.TrimSpace(strings.TrimPrefix(stripped, "const "))
		resolved := resolveTypeName(stripped)
		if resolved == "" {
			resolved = stripped
		}
		suffix := "**"
		if isRefToPtr {
			suffix = "*&"
		}
		if hasConst {
			return "const " + resolved + suffix
		}
		return resolved + suffix
	}
	if t.Kind == apispec.TypeVoid {
		// `void` itself is valid only as a return type — `void*` is a
		// legitimate param / return shape.
		if t.IsPointer {
			if t.IsConst {
				return "const void*"
			}
			return "void*"
		}
		return "void"
	}
	if t.Kind == apispec.TypePrimitive {
		return cppPrimitiveType(t.Name)
	}
	if t.Kind == apispec.TypeString {
		// Preserve the original view/cord/etc. spelling from qual_type —
		// the trampoline override must match the base class's vtable
		// slot byte-for-byte on return type, and non-std string types
		// (absl::string_view, absl::Cord, …) are distinct from
		// std::string. We only fall back to std::string when qual_type
		// isn't available.
		if t.QualType != "" && isUnsupportedStringType(t.QualType) {
			return strings.TrimSpace(t.QualType)
		}
		base := "std::string"
		if t.IsPointer {
			if t.IsConst {
				return "const " + base + "*"
			}
			return base + "*"
		}
		if t.IsRef {
			if t.IsConst {
				return "const " + base + "&"
			}
			return base + "&"
		}
		return base
	}
	// Handle, enum, and everything else: resolve the base class name
	// then reapply qualifiers. resolveTypeName returns the fully
	// qualified form when available (e.g. "googlesql::Column").
	base := resolveTypeName(t.Name)
	if base == "" {
		// Fall back to qual_type as a last resort; may not resolve
		// ambiguous short names but is at least syntactically valid.
		if t.QualType != "" {
			return t.QualType
		}
		return "void"
	}
	var sb strings.Builder
	if t.IsConst {
		sb.WriteString("const ")
	}
	sb.WriteString(base)
	if t.IsPointer {
		sb.WriteString("*")
	} else if t.IsRef {
		sb.WriteString("&")
	}
	return sb.String()
}

func trampolineReturnDecl(t apispec.TypeRef) string { return trampolineTypeDecl(t) }

func trampolineParamDecls(params []apispec.Param) string {
	var parts []string
	for i, p := range params {
		name := paramVarName(p, i)
		cppType := trampolineTypeDecl(p.Type)
		// `std::string` by-value params are rare and inconvenient;
		// prefer `const std::string&`. Don't touch unsupported-string
		// types (views, compressed) — those pass by value intentionally
		// and the override must match the base vtable spelling.
		if p.Type.Kind == apispec.TypeString && !p.Type.IsPointer && !p.Type.IsRef {
			if !isUnsupportedStringType(p.Type.QualType) {
				cppType = "const std::string&"
			}
		}
		parts = append(parts, fmt.Sprintf("%s %s", cppType, name))
	}
	return strings.Join(parts, ", ")
}

func writeTrampolineParamWrite(b *strings.Builder, p apispec.Param, fieldNum int, varName, indent string) {
	// Special-case shapes checked before kind-based dispatch.
	if isViewOfStringType(p.Type) {
		fmt.Fprintf(b, "%sfor (const auto& _s : %s) { _pw.write_string(%d, _s); }\n",
			indent, varName, fieldNum)
		return
	}
	switch p.Type.Kind {
	case apispec.TypePrimitive:
		cpp := cppPrimitiveType(p.Type.Name)
		switch cpp {
		case "bool":
			fmt.Fprintf(b, "%s_pw.write_bool(%d, %s);\n", indent, fieldNum, varName)
		case "float":
			fmt.Fprintf(b, "%s_pw.write_float(%d, %s);\n", indent, fieldNum, varName)
		case "double":
			fmt.Fprintf(b, "%s_pw.write_double(%d, %s);\n", indent, fieldNum, varName)
		case "int32_t":
			fmt.Fprintf(b, "%s_pw.write_int32(%d, %s);\n", indent, fieldNum, varName)
		case "uint32_t":
			fmt.Fprintf(b, "%s_pw.write_uint32(%d, %s);\n", indent, fieldNum, varName)
		case "int64_t":
			fmt.Fprintf(b, "%s_pw.write_int64(%d, %s);\n", indent, fieldNum, varName)
		case "uint64_t":
			fmt.Fprintf(b, "%s_pw.write_uint64(%d, %s);\n", indent, fieldNum, varName)
		default:
			fmt.Fprintf(b, "%s_pw.write_int64(%d, static_cast<int64_t>(%s));\n", indent, fieldNum, varName)
		}
	case apispec.TypeString:
		// `absl::string_view` (and other ExtraStringTypes) reach
		// here as TypeString after reclassification, but they don't
		// implicitly convert to `const std::string&`. Use the
		// (data, size) overload of ProtoWriter::write_string so
		// any string-like type with `.data()` / `.size()` works
		// without materialising a temporary std::string.
		qt := strings.TrimSpace(p.Type.QualType)
		isView := false
		for _, e := range bridgeConfig.ExtraStringTypes {
			if qt == e || p.Type.Name == e {
				isView = true
				break
			}
		}
		if isView {
			fmt.Fprintf(b, "%s_pw.write_string(%d, %s.data(), static_cast<uint32_t>(%s.size()));\n",
				indent, fieldNum, varName, varName)
		} else {
			fmt.Fprintf(b, "%s_pw.write_string(%d, %s);\n", indent, fieldNum, varName)
		}
	case apispec.TypeHandle:
		// Handle params wire as a submessage { uint64 ptr = 1 } so the
		// shape matches the Go client's pbAppendHandle encoding. Take
		// the address when passed by reference.
		if p.Type.IsPointer {
			fmt.Fprintf(b, "%s_pw.write_handle(%d, reinterpret_cast<uint64_t>(%s));\n", indent, fieldNum, varName)
		} else {
			fmt.Fprintf(b, "%s_pw.write_handle(%d, reinterpret_cast<uint64_t>(&%s));\n", indent, fieldNum, varName)
		}
	case apispec.TypeVector:
		// `const vector<T>&` (and friends) flow per-element on the
		// wire. The Go callback adapter accumulates each repeated
		// field N entry into a slice. Without this the trampoline
		// silently dropped the vector — leaving callbacks like
		// TVF.Resolve receiving `actualArguments` as an empty
		// slice even when the caller passed real elements.
		if p.Type.Inner == nil {
			fmt.Fprintf(b, "%s/* vector with unknown inner skipped */\n", indent)
			break
		}
		inner := *p.Type.Inner
		switch inner.Kind {
		case apispec.TypeHandle:
			fmt.Fprintf(b, "%sfor (const auto& _e : %s) {\n", indent, varName)
			if inner.IsPointer {
				fmt.Fprintf(b, "%s    _pw.write_handle(%d, reinterpret_cast<uint64_t>(_e));\n", indent, fieldNum)
			} else {
				fmt.Fprintf(b, "%s    _pw.write_handle(%d, reinterpret_cast<uint64_t>(&_e));\n", indent, fieldNum)
			}
			fmt.Fprintf(b, "%s}\n", indent)
		case apispec.TypeString:
			fmt.Fprintf(b, "%sfor (const auto& _e : %s) { _pw.write_string(%d, _e); }\n",
				indent, varName, fieldNum)
		case apispec.TypePrimitive:
			cpp := cppPrimitiveType(inner.Name)
			writer := "write_int64"
			cast := "static_cast<int64_t>"
			switch cpp {
			case "bool":
				writer, cast = "write_bool", ""
			case "float":
				writer, cast = "write_float", ""
			case "double":
				writer, cast = "write_double", ""
			case "int32_t":
				writer, cast = "write_int32", ""
			case "uint32_t":
				writer, cast = "write_uint32", ""
			case "int64_t":
				writer, cast = "write_int64", ""
			case "uint64_t":
				writer, cast = "write_uint64", ""
			}
			if cast != "" {
				fmt.Fprintf(b, "%sfor (const auto& _e : %s) { _pw.%s(%d, %s(_e)); }\n",
					indent, varName, writer, fieldNum, cast)
			} else {
				fmt.Fprintf(b, "%sfor (const auto& _e : %s) { _pw.%s(%d, _e); }\n",
					indent, varName, writer, fieldNum)
			}
		default:
			fmt.Fprintf(b, "%s/* vector inner kind %v not yet wired for trampoline */\n", indent, inner.Kind)
		}
	}
}

func writeTrampolineResultRead(b *strings.Builder, t apispec.TypeRef, indent string) {
	// `absl::Span<const std::string>` (and other configured
	// ValueViewTypes) — the Go callback returns a `[]string`; the
	// trampoline stores the strings in a per-instance buffer and
	// returns a Span pointing into it. Lifetime: the buffer is a
	// thread-local static so consecutive calls invalidate prior
	// Spans, but a single caller that consumes the Span before
	// the next callback fires sees stable data.
	if isViewOfStringType(t) {
		fmt.Fprintf(b, "%sthread_local std::vector<std::string> _backing;\n", indent)
		fmt.Fprintf(b, "%s_backing.clear();\n", indent)
		fmt.Fprintf(b, "%sif (_resp_ptr != 0) {\n", indent)
		fmt.Fprintf(b, "%s    ProtoReader _pr(reinterpret_cast<const void*>(_resp_ptr), _resp_len);\n", indent)
		fmt.Fprintf(b, "%s    while (_pr.next()) {\n", indent)
		fmt.Fprintf(b, "%s        if (_pr.field() == 1) _backing.push_back(_pr.read_string());\n", indent)
		fmt.Fprintf(b, "%s        else _pr.skip();\n", indent)
		fmt.Fprintf(b, "%s    }\n", indent)
		fmt.Fprintf(b, "%s    wasm_free(reinterpret_cast<void*>(_resp_ptr));\n", indent)
		fmt.Fprintf(b, "%s}\n", indent)
		fmt.Fprintf(b, "%sreturn absl::MakeConstSpan(_backing);\n", indent)
		return
	}
	// Default-construct a holder, parse field 1 into it, free the
	// response buffer, and return.
	switch t.Kind {
	case apispec.TypePrimitive:
		cpp := cppPrimitiveType(t.Name)
		fmt.Fprintf(b, "%s%s _result = {};\n", indent, cpp)
		fmt.Fprintf(b, "%sif (_resp_ptr != 0) {\n", indent)
		fmt.Fprintf(b, "%s    ProtoReader _pr(reinterpret_cast<const void*>(_resp_ptr), _resp_len);\n", indent)
		fmt.Fprintf(b, "%s    while (_pr.next()) {\n", indent)
		fmt.Fprintf(b, "%s        if (_pr.field() == 1) {\n", indent)
		switch cpp {
		case "bool":
			fmt.Fprintf(b, "%s            _result = _pr.read_bool();\n", indent)
		case "float":
			fmt.Fprintf(b, "%s            _result = _pr.read_float();\n", indent)
		case "double":
			fmt.Fprintf(b, "%s            _result = _pr.read_double();\n", indent)
		case "int32_t":
			fmt.Fprintf(b, "%s            _result = _pr.read_int32();\n", indent)
		case "uint32_t":
			fmt.Fprintf(b, "%s            _result = _pr.read_uint32();\n", indent)
		case "int64_t":
			fmt.Fprintf(b, "%s            _result = _pr.read_int64();\n", indent)
		case "uint64_t":
			fmt.Fprintf(b, "%s            _result = _pr.read_uint64();\n", indent)
		default:
			fmt.Fprintf(b, "%s            _result = static_cast<%s>(_pr.read_int64());\n", indent, cpp)
		}
		fmt.Fprintf(b, "%s        } else {\n", indent)
		fmt.Fprintf(b, "%s            _pr.skip();\n", indent)
		fmt.Fprintf(b, "%s        }\n", indent)
		fmt.Fprintf(b, "%s    }\n", indent)
		fmt.Fprintf(b, "%s    wasm_free(reinterpret_cast<void*>(_resp_ptr));\n", indent)
		fmt.Fprintf(b, "%s}\n", indent)
		fmt.Fprintf(b, "%sreturn _result;\n", indent)
	case apispec.TypeString:
		fmt.Fprintf(b, "%sstd::string _result;\n", indent)
		fmt.Fprintf(b, "%sif (_resp_ptr != 0) {\n", indent)
		fmt.Fprintf(b, "%s    ProtoReader _pr(reinterpret_cast<const void*>(_resp_ptr), _resp_len);\n", indent)
		fmt.Fprintf(b, "%s    while (_pr.next()) {\n", indent)
		fmt.Fprintf(b, "%s        if (_pr.field() == 1) _result = _pr.read_string();\n", indent)
		fmt.Fprintf(b, "%s        else _pr.skip();\n", indent)
		fmt.Fprintf(b, "%s    }\n", indent)
		fmt.Fprintf(b, "%s    wasm_free(reinterpret_cast<void*>(_resp_ptr));\n", indent)
		fmt.Fprintf(b, "%s}\n", indent)
		fmt.Fprintf(b, "%sreturn _result;\n", indent)
	case apispec.TypeHandle:
		// The Go side marshals a handle as a submessage with the raw
		// uint64 ptr in field 1 (same format as pbAppendHandle). Read
		// field 1 of the response, look inside, extract ptr.
		//
		// Use trampolineTypeDecl to get the fully-qualified type so the
		// reinterpret_cast compiles — the trampoline class is at global
		// namespace, so unqualified `Column*` in qual_type wouldn't
		// resolve.
		declType := trampolineTypeDecl(t)
		if declType == "" {
			declType = "void*"
		}
		fmt.Fprintf(b, "%suint64_t _ptr = 0;\n", indent)
		fmt.Fprintf(b, "%sif (_resp_ptr != 0) {\n", indent)
		fmt.Fprintf(b, "%s    ProtoReader _pr(reinterpret_cast<const void*>(_resp_ptr), _resp_len);\n", indent)
		fmt.Fprintf(b, "%s    while (_pr.next()) {\n", indent)
		fmt.Fprintf(b, "%s        if (_pr.field() == 1) _ptr = read_handle_ptr(_pr);\n", indent)
		fmt.Fprintf(b, "%s        else _pr.skip();\n", indent)
		fmt.Fprintf(b, "%s    }\n", indent)
		fmt.Fprintf(b, "%s    wasm_free(reinterpret_cast<void*>(_resp_ptr));\n", indent)
		fmt.Fprintf(b, "%s}\n", indent)
		if t.IsRef {
			// Reference return: dereference the handle pointer.
			// Strip trailing `&` from declType so the cast becomes a
			// pointer cast.
			ptrType := strings.TrimSuffix(strings.TrimSpace(declType), "&") + "*"
			fmt.Fprintf(b, "%sreturn *reinterpret_cast<%s>(_ptr);\n", indent, ptrType)
		} else {
			fmt.Fprintf(b, "%sreturn reinterpret_cast<%s>(_ptr);\n", indent, declType)
		}
	}
}

// ======================================
// Free function dispatch
// ======================================

// writeFreeFunctionDispatch emits one WASM_EXPORT per free function.
//
// The free-function group is treated as service id 0 when the project
// has any (matches the .proto and Go binding numbering). Each function
// becomes its own export named `w_<svc>_<mid>` so the host can call it
// directly via wazero's `mod.ExportedFunction(name)`. The body is
// identical to what a single `case` arm in the old switch dispatch
// produced.
func writeFreeFunctionDispatch(b *strings.Builder, functions []apispec.Function, packageName string, spec *apispec.APISpec, serviceID int) {
	fmt.Fprintf(b, "// Service %d: %s (free functions)\n", serviceID, packageName)
	for i, fn := range functions {
		exportName := fmt.Sprintf("w_%d_%d", serviceID, i)
		fmt.Fprintf(b, "WASM_EXPORT(%s)\n", exportName)
		fmt.Fprintf(b, "int64_t %s(void* req, int32_t req_len) { // %s\n", exportName, fn.Name)
		writeCallBody(b, &fn, "", spec, "    ")
		b.WriteString("}\n\n")
	}
}

// ======================================
// Handle dispatch
// ======================================

// writeHandleDispatch emits one WASM_EXPORT per (service, method) pair
// for a handle class. Replaces the legacy giant switch + function-
// pointer table with direct exports the host can look up by name. The
// numbering (constructors → static factories → regular methods →
// getters → FromCallback → Free) is preserved exactly so the generated
// .proto's `wasm_method_id` ordering still matches what the Go binding
// expects.
func writeHandleDispatch(b *strings.Builder, c *apispec.Class, allHandles map[string]*apispec.Class, spec *apispec.APISpec, serviceID int) {
	msgName := protoMessageName(c.QualName)
	fmt.Fprintf(b, "// Service %d: %sService\n", serviceID, msgName)

	emit := func(methodID int, label string, body func()) {
		exportName := fmt.Sprintf("w_%d_%d", serviceID, methodID)
		fmt.Fprintf(b, "WASM_EXPORT(%s)\n", exportName)
		fmt.Fprintf(b, "int64_t %s(void* req, int32_t req_len) { // %s\n", exportName, label)
		body()
		b.WriteString("}\n\n")
	}

	methodID := 0
	methods := filterBridgeMethods(disambiguateMethodNames(c.Methods))

	// Constructors first (matching proto.go service ordering).
	// Abstract classes cannot be instantiated — skip constructors.
	ctorCount := 0
	for _, m := range methods {
		if !m.IsConstructor {
			continue
		}
		if c.IsAbstract {
			continue
		}
		ctorCount++
		name := "New"
		if ctorCount > 1 {
			name = fmt.Sprintf("New%d", ctorCount)
		}
		mCopy := m
		label := fmt.Sprintf("%s (%s)", name, m.Name)
		emit(methodID, label, func() {
			writeConstructorBody(b, &mCopy, c.QualName, spec, "    ")
		})
		methodID++
	}

	// Static factory methods (treated like constructors but call ClassName::Method())
	for _, m := range methods {
		if !m.IsStatic || !isStaticFactory(m) {
			continue
		}
		mCopy := m
		label := fmt.Sprintf("%s (static factory)", m.Name)
		emit(methodID, label, func() {
			writeStaticFactoryBody(b, &mCopy, c.QualName, spec, "    ")
		})
		methodID++
	}

	// Regular methods
	for _, m := range methods {
		if m.IsConstructor || (m.IsStatic && isStaticFactory(m)) {
			continue
		}
		mCopy := m
		emit(methodID, m.Name, func() {
			writeCallBody(b, &mCopy, c.QualName, spec, "    ")
		})
		methodID++
	}

	// Getters
	for _, f := range filterBridgeFields(c.Fields) {
		fCopy := f
		emit(methodID, "Get"+toUpperCamel(f.Name), func() {
			writeGetterBody(b, c, &fCopy, "    ")
		})
		methodID++
	}

	// Downcast dispatch is intentionally NOT emitted. Go type assertion
	// handles abstract → concrete conversion natively; a wasm round-trip
	// for dynamic_cast would only add latency. CLAUDE.md:
	// "do not emit Downcast APIs".

	// FromCallback (for callback candidates). Allocates a trampoline
	// subclass instance bound to the supplied Go callback_id and returns
	// it as a handle pointer. Must appear at the same method_id the
	// proto schema placed it.
	//
	// One FromCallback variant per base ctor: the trampoline forwards
	// each base ctor's arguments through the wire so the Go-side
	// `NewXxxFromImpl(impl, args...)` can drive the construction
	// without the bridge ever needing to know an args-less form. For
	// classes with no ctors in api-spec (typical of abstract classes
	// whose only ctor is implicit-default), a single variant with
	// just callback_id is emitted.
	if isCallbackCandidateForBridge(c) {
		trampolineName := protoMessageName(c.QualName) + "Trampoline"
		ctorVariants := collectTrampolineCtors(c)
		emitVariant := func(variantIdx int, ctor *apispec.Function) {
			label := "FromCallback"
			if variantIdx > 0 {
				label = fmt.Sprintf("FromCallback%d", variantIdx+1)
			}
			emit(methodID, label, func() {
				// Use `reader` as the ProtoReader name — readExpr's
				// generated read calls reference it by that name
				// (existing convention shared with writeCallBody /
				// writeConstructorBody).
				b.WriteString("    ProtoReader reader(req, req_len);\n")
				b.WriteString("    int32_t _callback_id = 0;\n")
				if ctor != nil {
					for i, p := range ctor.Params {
						varName := paramVarName(p, i)
						localType := cppLocalType(p.Type)
						fmt.Fprintf(b, "    %s %s{};\n", localType, varName)
					}
				}
				b.WriteString("    while (reader.has_data() && reader.next()) {\n")
				b.WriteString("        switch (reader.field()) {\n")
				b.WriteString("        case 1: _callback_id = reader.read_int32(); break;\n")
				if ctor != nil {
					for i, p := range ctor.Params {
						varName := paramVarName(p, i)
						fieldNum := i + 2
						fmt.Fprintf(b, "        case %d: %s break;\n", fieldNum, readExpr(p.Type, varName))
					}
				}
				b.WriteString("        default: reader.skip(); break;\n")
				b.WriteString("        }\n")
				b.WriteString("    }\n")
				if ctor == nil {
					fmt.Fprintf(b, "    auto* _t = new %s(_callback_id);\n", trampolineName)
				} else {
					var args []string
					for i, p := range ctor.Params {
						varName := paramVarName(p, i)
						if p.Type.Kind == apispec.TypeHandle {
							castType := cppTypeNameInContext(p.Type, c.QualName)
							constQual := ""
							if p.Type.IsConst {
								constQual = "const "
							}
							args = append(args, handleArgExpr(p, varName, castType, constQual))
						} else if p.Type.Kind == apispec.TypeVector &&
							(!p.Type.IsRef || strings.HasSuffix(strings.TrimSpace(p.Type.QualType), "&&")) {
							args = append(args, "std::move("+varName+")")
						} else {
							args = append(args, varName)
						}
					}
					args = append(args, "_callback_id")
					fmt.Fprintf(b, "    auto* _t = new %s(%s);\n", trampolineName, strings.Join(args, ", "))
				}
				b.WriteString("    ProtoWriter _pw;\n")
				b.WriteString("    _pw.write_uint64(1, reinterpret_cast<uint64_t>(_t));\n")
				b.WriteString("    return _pw.finish();\n")
			})
			methodID++
		}
		if len(ctorVariants) == 0 {
			emitVariant(0, nil)
		} else {
			for i := range ctorVariants {
				emitVariant(i, &ctorVariants[i])
			}
		}
	}

	// Free — only when the class has a public destructor. proto.go
	// skips the RPC for non-public dtor; emitting an unreachable export
	// here would just bloat the EXPORT section.
	if c.HasPublicDtor {
		emit(methodID, "Free", func() {
			writeFreeBody(b, c, "    ")
		})
	}
}

// ======================================
// Case body generation
// ======================================

// writeCallBody generates deserialization, call, and serialization for a function/method.
// If handleClass is non-empty, this is a method call (field 1 = handle, params from field 2).
func writeCallBody(b *strings.Builder, fn *apispec.Function, handleClass string, spec *apispec.APISpec, indent string) {
	isMethod := handleClass != ""

	// Demote handle-typed params whose underlying class is classified as a
	// value type (IsHandle=false). Clang reports any class parameter passed
	// by reference or pointer as TypeHandle, but when the proto emits the
	// class as a value-type message (public-POD aggregate), the bridge
	// should parse its fields from a nested submessage rather than reading
	// a heap pointer — the Go client marshals the proto-message form. After
	// demotion the local declaration, submessage reader, and call-site
	// handling below all take the TypeValue path.
	for i := range fn.Params {
		p := &fn.Params[i]
		if p.Type.Kind == apispec.TypeHandle {
			resolved := resolveTypeName(cppTypeName(p.Type))
			if c, ok := valueClasses[resolved]; ok && !c.IsHandle {
				p.Type.Kind = apispec.TypeValue
				recordValueTypeParserNeeded(c)
			}
		}
	}
	// Promote value-type params with no public default constructor to handle
	// type. These cannot be declared as local variables with default init;
	// instead we pass them by pointer (uint64_t handle). We promote on
	// fn.Params so that buildCallExpr also sees the promoted types.
	for i := range fn.Params {
		p := &fn.Params[i]
		if p.Type.Kind == apispec.TypeValue {
			resolved := resolveTypeName(cppTypeName(p.Type))
			if classNoDefaultCtor != nil && classNoDefaultCtor[resolved] {
				p.Type.Kind = apispec.TypeHandle
			}
		}
	}
	// Separate input and output params (after promotion)
	var inputParams []apispec.Param
	var outputParams []apispec.Param
	for _, p := range fn.Params {
		if isOutputParam(p) {
			outputParams = append(outputParams, p)
		} else {
			inputParams = append(inputParams, p)
		}
	}

	// Declare locals
	if isMethod {
		fmt.Fprintf(b, "%suint64_t _handle_ptr = 0;\n", indent)
	}
	resolvedClass := ""
	if handleClass != "" {
		resolvedClass = resolveTypeName(handleClass)
	}
	for i, p := range inputParams {
		varName := paramVarName(p, i)
		cppType := cppLocalTypeInContext(p.Type, resolvedClass)
		fmt.Fprintf(b, "%s%s %s{};\n", indent, cppType, varName)
	}
	for i, p := range outputParams {
		varName := "out_" + paramVarName(p, i)
		// Output params are `T*` pointers. We declare the pointee type `T`
		// and pass `&out_var` to the function.
		var cppType string
		if p.Type.Kind == apispec.TypeHandle {
			typeName := cppTypeName(p.Type)
			resolved := resolveTypeName(typeName)
			// T** / T*& output: callee writes a pointer value into the
			// caller-provided slot. Declare the local as
			// `T* out_var = nullptr;` and pass either `&out_var`
			// (binds to `T**`) or `out_var` (binds to `T*&`) at the
			// call site (handled in buildCallExpr). Preserve const
			// qualifiers so the call expression's pointer type
			// matches the function param type.
			qtTrim := strings.TrimSpace(p.Type.QualType)
			qtNoConst := strings.TrimSuffix(qtTrim, "const")
			qtNoConst = strings.TrimSpace(qtNoConst)
			isPtrPtr := strings.HasSuffix(qtNoConst, "**")
			isRefToPtr := isReferenceToPointerHandle(p.Type)
			if isPtrPtr || isRefToPtr {
				if strings.HasPrefix(qtTrim, "const ") {
					cppType = "const " + typeName + "*"
				} else {
					cppType = typeName + "*"
				}
				fmt.Fprintf(b, "%s%s %s = nullptr;\n", indent, cppType, varName)
				continue
			}
			// Abstract/noNew classes can't be value-declared. Declare as
			// pointer instead: `ASTNode* out_lhs = nullptr;`
			if (classAbstract != nil && classAbstract[resolved]) ||
				(classNoNew != nil && classNoNew[resolved]) {
				cppType = typeName + "*"
				fmt.Fprintf(b, "%s%s %s = nullptr;\n", indent, cppType, varName)
				continue
			}
			cppType = typeName
		} else if p.Type.Kind == apispec.TypeString &&
			isUnsupportedStringType(p.Type.QualType) {
			// Preserve the original spelling (e.g. absl::string_view)
			// so the output-ptr signature matches the base vtable slot.
			// The qual_type includes the trailing `*` for output-ptr
			// params; strip it so we get the inner type.
			qt := strings.TrimSpace(p.Type.QualType)
			qt = strings.TrimSpace(strings.TrimSuffix(qt, "*"))
			cppType = qt
		} else if p.Type.Kind == apispec.TypePrimitive {
			// For output params, use the original C++ type name (e.g., size_t)
			// not the bridge's canonical mapping (uint64_t). This ensures
			// &out_var matches the function's pointer param type.
			qt := strings.TrimSpace(p.Type.QualType)
			qt = strings.TrimSuffix(qt, "*")
			qt = strings.TrimSpace(qt)
			if qt != "" {
				cppType = qt
			} else {
				cppType = cppLocalType(p.Type)
			}
		} else if p.Type.Kind == apispec.TypeVector &&
			strings.Contains(p.Type.QualType, "string_view") {
			// vector<string_view> output: use actual type
			qt := strings.TrimSpace(p.Type.QualType)
			qt = strings.TrimSuffix(qt, "*")
			qt = strings.TrimSpace(qt)
			if qt != "" {
				cppType = qt
			} else {
				cppType = cppLocalType(p.Type)
			}
		} else {
			cppType = cppLocalType(p.Type)
		}
		fmt.Fprintf(b, "%s%s %s{};\n", indent, cppType, varName)
	}

	// Parse request
	fmt.Fprintf(b, "%sProtoReader reader(req, req_len);\n", indent)
	fmt.Fprintf(b, "%swhile (reader.has_data() && reader.next()) {\n", indent)
	fmt.Fprintf(b, "%s    switch (reader.field()) {\n", indent)

	if isMethod {
		// Field 1 = handle submessage
		fmt.Fprintf(b, "%s    case 1: _handle_ptr = read_handle_ptr(reader); break;\n", indent)
	}

	fieldOffset := 1
	if isMethod {
		fieldOffset = 2
	}
	for i, p := range inputParams {
		fieldNum := i + fieldOffset
		varName := paramVarName(p, i)
		fmt.Fprintf(b, "%s    case %d: %s break;\n", indent, fieldNum, readExpr(p.Type, varName))
	}

	fmt.Fprintf(b, "%s    default: reader.skip(); break;\n", indent)
	fmt.Fprintf(b, "%s    }\n", indent)
	fmt.Fprintf(b, "%s}\n", indent)

	// Null handle check
	if isMethod {
		fmt.Fprintf(b, "%sif (_handle_ptr == 0) {\n", indent)
		fmt.Fprintf(b, "%s    ProtoWriter _pw;\n", indent)
		fmt.Fprintf(b, "%s    _pw.write_error(\"null handle\");\n", indent)
		fmt.Fprintf(b, "%s    return _pw.finish();\n", indent)
		fmt.Fprintf(b, "%s}\n", indent)

		constQual := ""
		if fn.IsConst {
			constQual = "const "
		}
		fmt.Fprintf(b, "%sauto* _self = reinterpret_cast<%s%s*>(_handle_ptr);\n",
			indent, constQual, resolvedClass)
	}

	// Build call expression
	callExpr := buildCallExpr(fn, handleClass, inputParams, outputParams)

	if fn.ReturnType.Kind == apispec.TypeVoid {
		fmt.Fprintf(b, "%s%s;\n", indent, callExpr)
	} else if fn.ReturnType.Kind == apispec.TypeHandle && !fn.ReturnType.IsPointer && !fn.ReturnType.IsRef &&
		!isSmartPointerType(cppTypeName(fn.ReturnType)) && !isSmartPointerType(fn.ReturnType.QualType) {
		// Handle by-value return: use direct heap construction to
		// avoid needing a copy or move constructor (guaranteed
		// copy elision in C++17). When the resolved type is
		// abstract, `new <T>(...)` is illegal — discard the
		// result and emit a wire-format error so the Go side
		// observes the call was attempted on a non-instantiable
		// surface rather than failing the C++ compile. The parser-
		// side `postProcessQualifyShortNames` pass is the primary
		// fix for the historical short-name ambiguity that landed
		// resolution on the wrong (abstract) candidate; this guard
		// is a defence-in-depth safety net for any future spec
		// that legitimately exposes a method whose declared return
		// type is abstract (e.g. a covariant return whose top-level
		// declaration names the abstract base).
		typeName := resolveTypeNameInContext(cppTypeName(fn.ReturnType), handleClass)
		if classAbstract != nil && classAbstract[typeName] {
			fmt.Fprintf(b, "%s(void)(%s);\n", indent, callExpr)
			fmt.Fprintf(b, "%sProtoWriter _pw;\n", indent)
			fmt.Fprintf(b, "%s_pw.write_error(\"cannot construct abstract class %s\");\n", indent, typeName)
			fmt.Fprintf(b, "%sreturn _pw.finish();\n", indent)
			return
		}
		fmt.Fprintf(b, "%sauto* _result = new %s(%s);\n", indent, typeName, callExpr)
	} else {
		retType := cppReturnType(fn.ReturnType)
		fmt.Fprintf(b, "%s%s _result = %s;\n", indent, retType, callExpr)
	}

	// Serialize response
	fmt.Fprintf(b, "%sProtoWriter _pw;\n", indent)
	respFieldNum := 1
	if fn.ReturnType.Kind != apispec.TypeVoid {
		fmt.Fprintf(b, "%s%s\n", indent, writeReturnExpr(fn.ReturnType, respFieldNum, "_result"))
		respFieldNum++
	}
	for i, p := range outputParams {
		varName := "out_" + paramVarName(p, i)
		fmt.Fprintf(b, "%s%s\n", indent, writeFieldExpr(p.Type, respFieldNum, varName))
		respFieldNum++
	}
	fmt.Fprintf(b, "%sreturn _pw.finish();\n", indent)
}

func buildCallExpr(fn *apispec.Function, handleClass string, inputParams, outputParams []apispec.Param) string {
	resolvedClass := ""
	if handleClass != "" {
		resolvedClass = resolveTypeName(handleClass)
	}
	var args []string
	inputIdx := 0
	outputIdx := 0
	for _, p := range fn.Params {
		if isOutputParam(p) {
			varName := "out_" + paramVarName(p, outputIdx)
			// `T*&` callee receives a reference to a local pointer:
			// pass the pointer lvalue directly. `T**` callee receives
			// a pointer to a local pointer: pass its address.
			if isReferenceToPointerHandle(p.Type) {
				args = append(args, varName)
			} else {
				args = append(args, "&"+varName)
			}
			outputIdx++
		} else {
			varName := paramVarName(p, inputIdx)
			// Set-like containers were materialised as locals of the
			// container type (see setLikeContainerInfo + cppLocalType).
			// The call site adapts the local to the callee's accepted
			// shape: pointer params get `&name`, rvalue-ref and
			// by-value params get `std::move(name)` so move-only
			// containers (or move-friendly ones) avoid an extra copy,
			// and lvalue-ref params bind directly.
			if _, _, ok := setLikeContainerInfo(p.Type); ok {
				args = append(args, setLikeCallExpr(p, varName))
				inputIdx++
				continue
			}
			// Cast handle params according to how the callee receives them:
			//   - T&   : dereference the bridge-side pointer once (`*ptr`)
			//   - T*   : pass the pointer as-is
			//   - T    : by-value parameter — also dereference. This case
			//            arises for classes promoted from TypeValue to
			//            TypeHandle (post-process rule for encapsulated
			//            types); the bridge stores a uint64 pointer but
			//            the callee expects a value copy, so we need to
			//            dereference and let the C++ copy constructor run.
			if p.Type.Kind == apispec.TypeHandle {
				castType := cppTypeNameInContext(p.Type, resolvedClass)
				constQual := ""
				if p.Type.IsConst {
					constQual = "const "
				}
				qt := strings.TrimSpace(p.Type.QualType)
				// Detect pointer-by-reference `T *&`: QualType contains
				// "*&" or "* &". The clang AST parser reports this as
				// IsRef=true, IsPointer=false because the outermost
				// type is reference. The callee receives a reference to
				// an existing pointer; materialize a named lvalue pointer
				// via a lambda-returning-reference.
				isPtrRef := p.Type.IsRef && (strings.Contains(qt, "*&") || strings.Contains(qt, "* &"))
				if isPtrRef {
					// Use an immediately-invoked lambda that returns a ref
					// to a local pointer. This creates an lvalue that can
					// bind to `T*&`. The local is mutated by the callee;
					// since we discard the return, callers observing the
					// mutation must read it via an output param.
					args = append(args, fmt.Sprintf("([&]() -> %s%s*& { static thread_local %s%s* _ptr; _ptr = reinterpret_cast<%s%s*>(%s); return _ptr; })()", constQual, castType, constQual, castType, constQual, castType, varName))
				} else {
					args = append(args, handleArgExpr(p, varName, castType, constQual))
				}
			} else if p.Type.Kind == apispec.TypeString && strings.Contains(p.Type.QualType, "char") && !strings.Contains(p.Type.QualType, "std::") && !strings.Contains(p.Type.QualType, "string") {
				// C-style string: func takes char* or const char*, local is
				// std::string. Use .data() (C++17+ returns char* for non-const
				// strings) so it binds to both const char* and char* params.
				if strings.Contains(p.Type.QualType, "const") {
					args = append(args, varName+".c_str()")
				} else {
					args = append(args, varName+".data()")
				}
			} else if p.Type.Kind == apispec.TypeString &&
				strings.Contains(p.Type.QualType, "std::string") &&
				p.Type.IsPointer {
				// std::string* / const std::string* param: local is std::string,
				// pass address.
				args = append(args, "&"+varName)
			} else if p.Type.Kind == apispec.TypePrimitive && p.Type.IsPointer && p.Type.IsConst {
				// `const T*` input pointer to primitive: local stores the
				// value, pass its address so the callee gets the right type.
				args = append(args, "&"+varName)
			} else if p.Type.Kind == apispec.TypeVector &&
				(!p.Type.IsRef ||
					strings.HasSuffix(strings.TrimSpace(p.Type.QualType), "&&")) {
				// Vector passed by value or rvalue reference: std::move to
				// avoid copying elements. Elements may be move-only
				// (unique_ptr) or non-copyable handle-like types.
				// Skip when passed by lvalue reference (`vector<...>&` or
				// `const vector<...>&`) — std::move would produce an rvalue
				// which cannot bind to a non-const lvalue reference, and a
				// const ref binding doesn't need a move anyway.
				args = append(args, "std::move("+varName+")")
			} else {
				// For rvalue reference params (T&&), wrap with std::move
				if p.Type.IsRef && !p.Type.IsConst &&
					strings.HasSuffix(strings.TrimSpace(p.Type.QualType), "&&") {
					args = append(args, "std::move("+varName+")")
				} else {
					args = append(args, varName)
				}
			}
			inputIdx++
		}
	}

	argStr := strings.Join(args, ", ")
	// Use OriginalName if set (disambiguateOverloads/Methods renames
	// overloaded functions for proto uniqueness; the actual C++ call must
	// use the original, unmodified name).
	cppName := fn.Name
	if fn.OriginalName != "" {
		cppName = fn.OriginalName
	}
	if handleClass != "" {
		return fmt.Sprintf("_self->%s(%s)", cppName, argStr)
	}
	qualName := fn.QualName
	if qualName == "" {
		qualName = cppName
	} else if fn.OriginalName != "" {
		// Replace the last component of qualName with the original name
		if idx := strings.LastIndex(qualName, "::"); idx >= 0 {
			qualName = qualName[:idx+2] + fn.OriginalName
		} else {
			qualName = fn.OriginalName
		}
	}
	return fmt.Sprintf("%s(%s)", qualName, argStr)
}

// isStaticFactory returns true if the method is a static method that returns
// an instance of the containing class (by value or pointer). These factory
// methods are treated like constructors in the bridge.
//
// Two factory shapes are recognised:
//
//  1. By-value return: `static T Foo::Make(args...)` — the original
//     pattern. The C++ call site is `auto* obj = new T(T::Make(args...))`.
//
//  2. Status-with-out-param: `static absl::Status Foo::Create(args...,
//     std::unique_ptr<T>* out)` or the equivalent `T**` shape. The
//     constructed instance is delivered through the out-parameter, and
//     a non-OK Status is reported as an error to the Go caller.
//     This is the conventional ZetaSQL / Abseil "factory returning Status"
//     idiom; the bridge unifies it with the by-value form by recognising
//     the out-parameter as the produced value.
func isStaticFactory(m apispec.Function) bool {
	if !m.IsStatic {
		return false
	}
	// Skip static methods listed in the bridge config (e.g., protobuf internals).
	for _, skip := range bridgeConfig.SkipStaticMethods {
		if m.Name == skip {
			return false
		}
	}
	rpcName, _ := toProtoRPCName(m.Name)
	if rpcName == "" {
		return false
	}

	containingClass := m.QualName
	if idx := strings.LastIndex(containingClass, "::"); idx >= 0 {
		containingClass = containingClass[:idx]
	}

	// Shape (2): Status-with-out-param. When the return type is an error
	// type, look for an output param whose pointee is the containing class.
	if matchErrorType(m.ReturnType.Name) != "" || matchErrorType(m.ReturnType.QualType) != "" {
		if !hasFactoryOutParam(m, containingClass) {
			return false
		}
		// All other (non-out) params must still be marshalable.
		for _, p := range m.Params {
			if isOutputParam(p) {
				continue
			}
			if !isUsableType(p.Type) {
				return false
			}
			if !isInstantiableType(p.Type) {
				return false
			}
		}
		// Class must still be heap-allocatable on the C++ side.
		if classAbstract != nil && classAbstract[containingClass] {
			return false
		}
		if classNoNew != nil && classNoNew[containingClass] {
			return false
		}
		return true
	}

	// Shape (1): by-value return of the containing class.
	if m.ReturnType.Kind != apispec.TypeHandle && m.ReturnType.Kind != apispec.TypeValue {
		return false
	}
	// Static factory must return by value (not pointer/reference).
	if m.ReturnType.IsPointer || m.ReturnType.IsRef {
		return false
	}
	// The return type must be the containing class.
	// Methods returning unrelated types are not factories.
	retName := cppTypeName(m.ReturnType)
	retQual := resolveTypeName(retName)
	if retQual != containingClass {
		return false
	}
	// The return type must be instantiable (not abstract, not deleted-new).
	if classAbstract != nil && classAbstract[retQual] {
		return false
	}
	if classNoNew != nil && classNoNew[retQual] {
		return false
	}
	// If the return type's source file is not from the project, skip.
	// This filters out protobuf-generated message classes and other
	// external types whose constructors may not be directly callable.
	if classSourceFiles != nil {
		src, known := classSourceFiles[retQual]
		if known && !isProjectSource(src) {
			return false
		}
	}
	// Check all params are usable and instantiable
	for _, p := range m.Params {
		if !isUsableType(p.Type) {
			return false
		}
		if !isInstantiableType(p.Type) {
			return false
		}
	}
	return true
}

// hasFactoryOutParam reports whether m has at least one output parameter
// whose pointee resolves to the containing class. The pointee may be
// `T**` (raw pointer-to-pointer) or `std::unique_ptr<T>*` (smart pointer
// out-param transferring ownership). Both forms are recognised by
// `isOutputParam` already; this helper additionally requires the
// pointee type to match the containing class so that we can interpret
// the parameter as the factory's produced value.
func hasFactoryOutParam(m apispec.Function, containingClass string) bool {
	for _, p := range m.Params {
		if !isOutputParam(p) {
			continue
		}
		if factoryOutParamMatches(p.Type, containingClass) {
			return true
		}
	}
	return false
}

// factoryOutParamMatches checks whether t — which must already have
// passed isOutputParam — points at the containing class. The helper
// peels exactly one level of indirection from the parameter type and
// asks whether what remains resolves to the same class as the method
// is declared on. The peeled level can be:
//
//   - One trailing `*` of a `T**` (the outer pointer is what makes the
//     parameter writable; the inner pointer is the factory's produced
//     value).
//   - One `std::unique_ptr<T>` wrapper. Only `unique_ptr` is
//     accepted (not `shared_ptr`) because the factory contract is
//     exclusive ownership transfer: the produced object becomes the
//     caller's, and the bridge releases the wrapper into a raw
//     pointer to populate the response.
//
// `isUniquePointerType` is the canonical check for the unique-
// ownership wrapper. Centralising the recognition there keeps the
// representation-level matching (string-prefix check on the wrapper
// name) in one place, separated from this function's structural job
// of comparing the pointee class.
func factoryOutParamMatches(t apispec.TypeRef, containingClass string) bool {
	qt := strings.TrimSpace(t.QualType)

	// `unique_ptr<T>*` (or `std::unique_ptr<const T>*` etc.): peel
	// the trailing `*` that makes the parameter writable, confirm
	// the wrapper is unique_ptr (not shared_ptr), then ask the
	// canonical `smartPointerInner` to extract T. Reusing
	// smartPointerInner keeps the inner-type extraction logic
	// shared with `handleArgExpr` and `paramTakesOwnership`, so any
	// future change to inner-type resolution (qualifier preservation,
	// nested-template handling, etc.) automatically flows through.
	stripped := strings.TrimSpace(strings.TrimSuffix(qt, "*"))
	stripped = strings.TrimPrefix(stripped, "const ")
	stripped = strings.TrimSpace(stripped)
	if isUniquePointerType(stripped) {
		inner := smartPointerInner(stripped)
		if inner == "" {
			return false
		}
		// smartPointerInner re-attaches a leading `const` when the
		// wrapper held one; strip it here because containingClass
		// is the bare class name without cv-qualifiers.
		inner = strings.TrimPrefix(inner, "const ")
		inner = strings.TrimSpace(inner)
		return resolveTypeName(inner) == containingClass
	}

	// `T**` / `const T**` — strip both stars and one optional const.
	doubleStripped := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(qt, "*")), "*"))
	doubleStripped = strings.TrimPrefix(doubleStripped, "const ")
	doubleStripped = strings.TrimSpace(doubleStripped)
	if doubleStripped == "" {
		doubleStripped = cppTypeName(t)
	}
	return resolveTypeName(doubleStripped) == containingClass
}

// bridgeReservedNames are variable names used internally in bridge dispatch
// functions. Parameter names matching these get a "_param" prefix.
var bridgeReservedNames = map[string]bool{
	"reader": true, "req": true, "req_len": true, "method_id": true,
	"_handle_ptr": true, "_self": true, "_pw": true, "_result": true,
	"_obj": true, "service_id": true,
}

func paramVarName(p apispec.Param, idx int) string {
	name := p.Name
	if name == "" {
		name = fmt.Sprintf("arg%d", idx)
	}
	// Sanitize for C++
	name = toCIdentifier(name)
	if bridgeReservedNames[name] {
		name = "_param_" + name
	}
	return name
}

// writeGetterBody reads handle directly (request IS the handle message), accesses field.
// Getters access public data members directly (they are annotated as "getter" in proto).
// c.Fields represents data members; method-based accessors are in c.Methods.
func writeGetterBody(b *strings.Builder, c *apispec.Class, f *apispec.Field, indent string) {
	fmt.Fprintf(b, "%suint64_t _handle_ptr = read_handle_direct(req, req_len);\n", indent)
	fmt.Fprintf(b, "%sif (_handle_ptr == 0) {\n", indent)
	fmt.Fprintf(b, "%s    ProtoWriter _pw;\n", indent)
	fmt.Fprintf(b, "%s    _pw.write_error(\"null handle\");\n", indent)
	fmt.Fprintf(b, "%s    return _pw.finish();\n", indent)
	fmt.Fprintf(b, "%s}\n", indent)
	fmt.Fprintf(b, "%sauto* _self = reinterpret_cast<const %s*>(_handle_ptr);\n", indent, c.QualName)

	// Fields are data members. For handle types stored by value the bridge
	// must NOT copy (the object may be non-copyable or move-only); it
	// returns the address of the member instead so the caller gets a
	// stable handle whose lifetime matches _self.
	fmt.Fprintf(b, "%sProtoWriter _pw;\n", indent)
	if f.Type.Kind == apispec.TypeHandle && !f.Type.IsPointer && !f.Type.IsRef {
		qt := f.Type.QualType
		if qt == "" {
			qt = f.Type.Name
		}
		if isSharedPointerType(qt) {
			// shared_ptr: heap-allocate a copy so the caller has joint
			// ownership independent of _self's lifetime.
			fmt.Fprintf(b, "%sconst auto& _result = _self->%s;\n", indent, f.Name)
			fmt.Fprintf(b, "%s_pw.write_handle(1, reinterpret_cast<uint64_t>(new auto(_result)));\n", indent)
		} else if isSmartPointerType(qt) {
			// unique_ptr field: borrow via .get(); do not transfer ownership.
			fmt.Fprintf(b, "%sconst auto& _result = _self->%s;\n", indent, f.Name)
			fmt.Fprintf(b, "%s_pw.write_handle(1, reinterpret_cast<uint64_t>(_result.get()));\n", indent)
		} else {
			// Plain handle field: return address of the member.
			fmt.Fprintf(b, "%s_pw.write_handle(1, reinterpret_cast<uint64_t>(&_self->%s));\n", indent, f.Name)
		}
	} else if f.Type.Kind == apispec.TypeVector {
		// Vector fields: bind by const reference to avoid copying.
		// Copying vectors of move-only elements (e.g., unique_ptr) is
		// impossible; copying vectors of non-default-constructible types
		// also fails. writeReturnExpr handles iteration — it works the
		// same whether the source is by-value or by-reference.
		fmt.Fprintf(b, "%sconst auto& _result = _self->%s;\n", indent, f.Name)
		fmt.Fprintf(b, "%s%s\n", indent, writeReturnExpr(f.Type, 1, "_result"))
	} else {
		fmt.Fprintf(b, "%sauto _result = _self->%s;\n", indent, f.Name)
		fmt.Fprintf(b, "%s%s\n", indent, writeReturnExpr(f.Type, 1, "_result"))
	}
	fmt.Fprintf(b, "%sreturn _pw.finish();\n", indent)
}

// writeFreeBody generates handle deletion code.
// For classes with non-public destructors (e.g., abstract types managed by
// arenas or parent owners), the Free handler is a no-op — the handle is
// released without calling delete.
func writeFreeBody(b *strings.Builder, c *apispec.Class, indent string) {
	fmt.Fprintf(b, "%suint64_t _handle_ptr = read_handle_direct(req, req_len);\n", indent)
	if c.HasPublicDtor {
		fmt.Fprintf(b, "%sif (_handle_ptr != 0) {\n", indent)
		fmt.Fprintf(b, "%s    delete reinterpret_cast<const %s*>(_handle_ptr);\n", indent, c.QualName)
		fmt.Fprintf(b, "%s}\n", indent)
	} else {
		fmt.Fprintf(b, "%s// %s has non-public dtor — handle released without delete.\n", indent, c.QualName)
		fmt.Fprintf(b, "%s(void)_handle_ptr;\n", indent)
	}
	fmt.Fprintf(b, "%sreturn encode_result(nullptr, 0);\n", indent)
}

// writeStaticFactoryBody generates the dispatch body for a static factory method.
// Static factory methods like ParseResumeLocation::FromStringView(input) return
// an instance of the class. The bridge calls ClassName::Method(params) and
// heap-allocates the result via new + copy/move.
func writeStaticFactoryBody(b *strings.Builder, fn *apispec.Function, handleClass string, spec *apispec.APISpec, indent string) {
	// Same parameter handling as constructors (no self handle)
	var inputParams []apispec.Param
	for _, p := range fn.Params {
		if !isOutputParam(p) {
			inputParams = append(inputParams, p)
		}
	}

	resolvedClass := resolveTypeName(handleClass)

	// Promote value-type params with no public default constructor to handle
	for i := range inputParams {
		p := &inputParams[i]
		if p.Type.Kind == apispec.TypeValue {
			resolved := resolveTypeName(cppTypeName(p.Type))
			if classNoDefaultCtor != nil && classNoDefaultCtor[resolved] {
				p.Type.Kind = apispec.TypeHandle
			}
			if classDeletedCopy != nil && classDeletedCopy[resolved] {
				p.Type.Kind = apispec.TypeHandle
			}
		}
	}

	// Declare locals for each parameter
	for i, p := range inputParams {
		varName := paramVarName(p, i)
		localType := cppLocalType(p.Type)
		fmt.Fprintf(b, "%s%s %s{};\n", indent, localType, varName)
	}

	fmt.Fprintf(b, "%sProtoReader reader(req, req_len);\n", indent)
	fmt.Fprintf(b, "%swhile (reader.has_data() && reader.next()) {\n", indent)
	fmt.Fprintf(b, "%s    switch (reader.field()) {\n", indent)

	for i, p := range inputParams {
		varName := paramVarName(p, i)
		fmt.Fprintf(b, "%s    case %d: %s break;\n", indent, i+1,
			readExpr(p.Type, varName))
	}

	fmt.Fprintf(b, "%s    default: reader.skip(); break;\n", indent)
	fmt.Fprintf(b, "%s    }\n", indent)
	fmt.Fprintf(b, "%s}\n", indent)

	// Build call expression: ClassName::MethodName(args)
	var args []string
	for i, p := range inputParams {
		varName := paramVarName(p, i)
		// Set-like container locals are passed by name directly (see
		// setLikeContainerInfo + cppLocalType).
		if _, _, ok := setLikeContainerInfo(p.Type); ok {
			args = append(args, varName)
			continue
		}
		if p.Type.Kind == apispec.TypeHandle {
			castType := cppTypeNameInContext(p.Type, handleClass)
			constQual := ""
			if p.Type.IsConst {
				constQual = "const "
			}
			args = append(args, handleArgExpr(p, varName, castType, constQual))
		} else if p.Type.Kind == apispec.TypeVector &&
			(!p.Type.IsRef ||
				strings.HasSuffix(strings.TrimSpace(p.Type.QualType), "&&")) {
			// Vector params declared as rvalue-ref or by value need a
			// std::move to bind our local to the callee. Mirrors the
			// logic in writeConstructorBody / writeCallBody.
			args = append(args, "std::move("+varName+")")
		} else if p.Type.Kind == apispec.TypeString &&
			strings.Contains(p.Type.QualType, "char") &&
			!strings.Contains(p.Type.QualType, "std::") &&
			!strings.Contains(p.Type.QualType, "string") {
			// C-style string param (`const char *` / `char *`):
			// the local is std::string from the wire, but the
			// callee wants char data. Pick c_str() for const,
			// data() for mutable. Mirrors writeCallBody — without
			// this branch, static factories like
			// SourceLocation::DoNotInvokeDirectly(unsigned, const
			// char *) failed to compile because the bridge passed
			// the std::string local directly.
			if strings.Contains(p.Type.QualType, "const") {
				args = append(args, varName+".c_str()")
			} else {
				args = append(args, varName+".data()")
			}
		} else {
			args = append(args, varName)
		}
	}
	methodName := fn.Name
	if fn.OriginalName != "" {
		methodName = fn.OriginalName
	}

	// Status-with-out-param factory: declare an out-pointer local, pass
	// it as the trailing argument, then translate the Status into either
	// a populated handle response (success) or a wire-format error
	// (field 15) so the Go side can `return err`.
	containingClass := fn.QualName
	if idx := strings.LastIndex(containingClass, "::"); idx >= 0 {
		containingClass = containingClass[:idx]
	}
	if matchErrorType(fn.ReturnType.Name) != "" || matchErrorType(fn.ReturnType.QualType) != "" {
		var outIsUniquePointer bool
		var outUniquePointerWrapper string // e.g. "std::unique_ptr"
		for _, p := range fn.Params {
			if !isOutputParam(p) {
				continue
			}
			if !factoryOutParamMatches(p.Type, containingClass) {
				continue
			}
			// Strip the trailing `*` (which is what makes the
			// parameter writable) and any leading `const ` to
			// recover the plain wrapper spelling, then ask the
			// canonical unique-pointer recogniser whether we are
			// looking at one. shared_ptr is intentionally NOT
			// matched: factory ownership is exclusive transfer, and
			// the bridge's `release()`-then-emit-raw-pointer path is
			// only correct under unique ownership.
			peeled := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(p.Type.QualType), "*"))
			peeled = strings.TrimPrefix(peeled, "const ")
			peeled = strings.TrimSpace(peeled)
			if isUniquePointerType(peeled) {
				outIsUniquePointer = true
				if open := strings.Index(peeled, "<"); open >= 0 {
					outUniquePointerWrapper = peeled[:open]
				}
			}
			break
		}
		// Declare the local that backs the out-parameter. Unique-
		// pointer out-params take ownership of the produced object
		// at construction time and we hand it to the bridge by
		// `release()`-ing the wrapper. Raw `T**` out-params yield a
		// borrowed pointer that we forward to the response channel
		// directly. In both cases the inner / pointee type is the
		// containing class — verified above by
		// factoryOutParamMatches — so the local always uses the
		// fully-qualified containing-class name regardless of how
		// the api-spec qual-type happened to spell it.
		if outIsUniquePointer {
			fmt.Fprintf(b, "%s%s<%s> _out_obj;\n", indent, outUniquePointerWrapper, resolvedClass)
			args = append(args, "&_out_obj")
		} else {
			fmt.Fprintf(b, "%s%s* _out_obj = nullptr;\n", indent, resolvedClass)
			args = append(args, "&_out_obj")
		}
		callArgStr := strings.Join(args, ", ")
		retName := matchErrorOnlyReturnType(fn.ReturnType)
		if retName == "" {
			// matchErrorOnlyReturnType handles types listed in
			// ErrorOnlyReturnTypes; matchErrorType handles ErrorTypes
			// (a superset). Fall back to the latter so we always have a
			// name for the reconstruction lookup below.
			retName = strings.TrimSpace(fn.ReturnType.QualType)
			if retName == "" {
				retName = fn.ReturnType.Name
			}
		}
		// retName / ErrorReconstruct are tracked here for parity with
		// the trampoline path, even though the bridge body writes the
		// error message directly via field 15 rather than going through
		// the reconstruction template.
		_ = retName
		_ = bridgeConfig.ErrorReconstruct[retName]
		fmt.Fprintf(b, "%sauto _st = %s::%s(%s);\n", indent, resolvedClass, methodName, callArgStr)
		fmt.Fprintf(b, "%sProtoWriter _pw;\n", indent)
		fmt.Fprintf(b, "%sif (!_st.ok()) {\n", indent)
		fmt.Fprintf(b, "%s    _pw.write_string(15, std::string(_st.message()));\n", indent)
		fmt.Fprintf(b, "%s    return _pw.finish();\n", indent)
		fmt.Fprintf(b, "%s}\n", indent)
		// _st is OK — extract the constructed instance. Unique-pointer
		// wrappers transferred sole ownership to us; release() pulls
		// the raw pointer out so the bridge owns the heap object after
		// the wrapper destructs. Raw `T**` factories yield a borrowed
		// pointer that we forward as-is.
		if outIsUniquePointer {
			fmt.Fprintf(b, "%s_pw.write_uint64(1, reinterpret_cast<uint64_t>(_out_obj.release()));\n", indent)
		} else {
			fmt.Fprintf(b, "%s_pw.write_uint64(1, reinterpret_cast<uint64_t>(_out_obj));\n", indent)
		}
		fmt.Fprintf(b, "%sreturn _pw.finish();\n", indent)
		return
	}

	argStr := strings.Join(args, ", ")
	// Static factory returns by value — heap-allocate via move construction
	fmt.Fprintf(b, "%sauto* _obj = new %s(%s::%s(%s));\n",
		indent, resolvedClass, resolvedClass, methodName, argStr)
	fmt.Fprintf(b, "%sProtoWriter _pw;\n", indent)
	fmt.Fprintf(b, "%s_pw.write_uint64(1, reinterpret_cast<uint64_t>(_obj));\n", indent)
	fmt.Fprintf(b, "%sreturn _pw.finish();\n", indent)
}

// writeConstructorBody generates the dispatch body for a constructor RPC.
// Unlike regular methods, constructors:
//   - have no `handle` field in the request (parameters start at field 1)
//   - call `new ClassName(args...)` instead of `_self->method(args...)`
//   - return the newly-allocated pointer as a handle
func writeConstructorBody(b *strings.Builder, fn *apispec.Function, handleClass string, spec *apispec.APISpec, indent string) {
	// Resolve enclosing class for context-aware type resolution
	resolvedClass := resolveTypeName(handleClass)

	// Promote value-type params with no public default constructor to handle
	// type (same as writeCallBody). Promote on fn.Params so the arg-building
	// loop also sees the promoted types.
	for i := range fn.Params {
		p := &fn.Params[i]
		if p.Type.Kind == apispec.TypeValue {
			resolved := resolveTypeName(cppTypeName(p.Type))
			if classNoDefaultCtor != nil && classNoDefaultCtor[resolved] {
				p.Type.Kind = apispec.TypeHandle
			}
		}
	}
	// Separate input and output params after promotion. Output params in a
	// ctor are unusual but we preserve the handling for consistency with
	// writeCallBody.
	var inputParams []apispec.Param
	var outputParams []apispec.Param
	for _, p := range fn.Params {
		if isOutputParam(p) {
			outputParams = append(outputParams, p)
		} else {
			inputParams = append(inputParams, p)
		}
	}

	// Declare locals for input params
	for i, p := range inputParams {
		varName := paramVarName(p, i)
		cppType := cppLocalTypeInContext(p.Type, resolvedClass)
		fmt.Fprintf(b, "%s%s %s{};\n", indent, cppType, varName)
	}
	for i, p := range outputParams {
		varName := "out_" + paramVarName(p, i)
		var cppType string
		if p.Type.Kind == apispec.TypeHandle {
			cppType = cppTypeNameInContext(p.Type, resolvedClass)
		} else {
			cppType = cppLocalTypeInContext(p.Type, resolvedClass)
		}
		fmt.Fprintf(b, "%s%s %s{};\n", indent, cppType, varName)
	}

	// Parse request: constructor params start at field 1 (no handle field)
	fmt.Fprintf(b, "%sProtoReader reader(req, req_len);\n", indent)
	fmt.Fprintf(b, "%swhile (reader.has_data() && reader.next()) {\n", indent)
	fmt.Fprintf(b, "%s    switch (reader.field()) {\n", indent)
	for i, p := range inputParams {
		fieldNum := i + 1
		varName := paramVarName(p, i)
		fmt.Fprintf(b, "%s    case %d: %s break;\n", indent, fieldNum, readExpr(p.Type, varName))
	}
	fmt.Fprintf(b, "%s    default: reader.skip(); break;\n", indent)
	fmt.Fprintf(b, "%s    }\n", indent)
	fmt.Fprintf(b, "%s}\n", indent)

	// Build call argument list (shared with buildCallExpr minus the `this`)
	var args []string
	inputIdx := 0
	outputIdx := 0
	for _, p := range fn.Params {
		if isOutputParam(p) {
			varName := "out_" + paramVarName(p, outputIdx)
			// `T*&` callee receives a reference to a local pointer:
			// pass the pointer lvalue directly. `T**` callee receives
			// a pointer to a local pointer: pass its address.
			if isReferenceToPointerHandle(p.Type) {
				args = append(args, varName)
			} else {
				args = append(args, "&"+varName)
			}
			outputIdx++
		} else {
			varName := paramVarName(p, inputIdx)
			// Set-like container: pass the local container directly.
			if _, _, ok := setLikeContainerInfo(p.Type); ok {
				args = append(args, varName)
				inputIdx++
				continue
			}
			if p.Type.Kind == apispec.TypeHandle {
				castType := cppTypeNameInContext(p.Type, resolvedClass)
				constQual := ""
				if p.Type.IsConst {
					constQual = "const "
				}
				args = append(args, handleArgExpr(p, varName, castType, constQual))
			} else if p.Type.Kind == apispec.TypeString && strings.Contains(p.Type.QualType, "char") && !strings.Contains(p.Type.QualType, "std::") && !strings.Contains(p.Type.QualType, "string") {
				// C-style string: pass non-const .data() for `char*` or
				// .c_str() for `const char*`.
				if strings.Contains(p.Type.QualType, "const") {
					args = append(args, varName+".c_str()")
				} else {
					args = append(args, varName+".data()")
				}
			} else if p.Type.Kind == apispec.TypeString &&
				strings.Contains(p.Type.QualType, "std::string") &&
				p.Type.IsPointer {
				args = append(args, "&"+varName)
			} else if p.Type.Kind == apispec.TypeVector &&
				(!p.Type.IsRef ||
					strings.HasSuffix(strings.TrimSpace(p.Type.QualType), "&&")) {
				args = append(args, "std::move("+varName+")")
			} else {
				args = append(args, varName)
			}
			inputIdx++
		}
	}
	argStr := strings.Join(args, ", ")

	fmt.Fprintf(b, "%sauto* _obj = new %s(%s);\n", indent, resolvedClass, argStr)

	// Serialize response: the response IS the handle message, which has
	// `uint64 ptr = 1` at the top level (not wrapped in another submessage).
	fmt.Fprintf(b, "%sProtoWriter _pw;\n", indent)
	fmt.Fprintf(b, "%s_pw.write_uint64(1, reinterpret_cast<uint64_t>(_obj));\n", indent)

	// Write output params (if any) starting at field 2
	respFieldNum := 2
	for i, p := range outputParams {
		varName := "out_" + paramVarName(p, i)
		fmt.Fprintf(b, "%s%s\n", indent, writeFieldExpr(p.Type, respFieldNum, varName))
		respFieldNum++
	}
	fmt.Fprintf(b, "%sreturn _pw.finish();\n", indent)
}

// ======================================
// Main dispatcher and init/shutdown
// ======================================

// writeMainDispatcher used to emit a function-pointer table indexed by
// service_id and a single `wasm_invoke(service_id, method_id, …)`
// export. Both have been replaced by per-method exports
// (`w_<svc>_<mid>`) so the host can dispatch directly via
// `mod.ExportedFunction(name)`. The only piece that survives here is
// the typeid helper, exposed under its own export.
func writeMainDispatcher(b *strings.Builder, freeFunctions []apispec.Function, classNames []string, packageName string, handleClasses map[string]*apispec.Class) {
	b.WriteString("// ======================================\n")
	b.WriteString("// Runtime helpers\n")
	b.WriteString("// ======================================\n\n")

	// typeid helper: returns the demangled C++ type name for any handle
	// pointer, used by the Go runtime to dispatch abstract return types
	// to concrete Go types.
	b.WriteString("#include <typeinfo>\n")
	b.WriteString("#include <cxxabi.h>\n\n")
	b.WriteString("WASM_EXPORT(wasmify_get_type_name)\n")
	b.WriteString("int64_t wasmify_get_type_name(void* req, int32_t req_len) {\n")
	b.WriteString("    ProtoReader reader(req, req_len);\n")
	b.WriteString("    uint64_t ptr = 0;\n")
	b.WriteString("    while (reader.has_data() && reader.next()) {\n")
	b.WriteString("        if (reader.field() == 1) ptr = reader.read_uint64();\n")
	b.WriteString("        else reader.skip();\n")
	b.WriteString("    }\n")
	b.WriteString("    if (ptr == 0) { ProtoWriter pw; pw.write_error(\"null handle\"); return pw.finish(); }\n")

	// Collect abstract classes that have known concrete subclasses, then
	// generate a chain of `dynamic_cast` checks. The first successful
	// cast gives us the base pointer typeid uses for runtime resolution.
	abstractBases := []string{}
	for _, qualName := range classNames {
		c := handleClasses[qualName]
		if c != nil && c.IsAbstract {
			abstractBases = append(abstractBases, qualName)
		}
	}

	if len(abstractBases) > 0 {
		first := true
		for _, qualName := range abstractBases {
			if first {
				fmt.Fprintf(b, "    if (auto* p = reinterpret_cast<%s*>(ptr)) {\n", qualName)
				first = false
			} else {
				fmt.Fprintf(b, "    } else if (auto* p = reinterpret_cast<%s*>(ptr)) {\n", qualName)
			}
			b.WriteString("        int status = 0;\n")
			b.WriteString("        const char* mangled = typeid(*p).name();\n")
			b.WriteString("        char* demangled = abi::__cxa_demangle(mangled, nullptr, nullptr, &status);\n")
			b.WriteString("        ProtoWriter pw;\n")
			b.WriteString("        if (status == 0 && demangled) {\n")
			b.WriteString("            pw.write_string(1, std::string(demangled));\n")
			b.WriteString("            free(demangled);\n")
			b.WriteString("        } else {\n")
			b.WriteString("            pw.write_string(1, std::string(mangled));\n")
			b.WriteString("        }\n")
			b.WriteString("        return pw.finish();\n")
		}
		b.WriteString("    }\n")
	}
	b.WriteString("    ProtoWriter pw;\n")
	b.WriteString("    pw.write_error(\"unknown type\");\n")
	b.WriteString("    return pw.finish();\n")
	b.WriteString("}\n\n")
}

func writeInitShutdown(b *strings.Builder) {
	b.WriteString("WASM_EXPORT(wasm_init)\nint32_t wasm_init() { return 0; }\n\n")
	b.WriteString("WASM_EXPORT(wasm_shutdown)\nvoid wasm_shutdown() { }\n\n")
}

// ======================================
// Helper utilities
// ======================================

// Headers known to cause compilation errors when included in the bridge
// (e.g., typedef redefinitions between conflicting headers). These are
// excluded from the bridge's #include list. The types they define are
// still usable if another header forward-declares them.
// skipBridgeHeaders and skipBridgeClasses are populated from BridgeConfig
// at the start of GenerateBridgeWithConfig.
var skipBridgeHeadersMap map[string]bool
var skipBridgeClassesMap map[string]bool

// collectBridgeSourceFiles gathers source files only for types used in the bridge dispatch.
func collectBridgeSourceFiles(freeFunctions []apispec.Function, handleClasses map[string]*apispec.Class, projectRoot string) []string {
	seen := make(map[string]bool)
	var result []string
	add := func(f string) {
		f = normalizeHeaderPath(f, projectRoot)
		if f != "" && !seen[f] && !skipBridgeHeadersMap[f] {
			seen[f] = true
			result = append(result, f)
		}
	}
	for _, fn := range freeFunctions {
		add(fn.SourceFile)
	}
	for _, c := range handleClasses {
		add(c.SourceFile)
		for _, m := range c.Methods {
			add(m.SourceFile)
		}
	}
	sort.Strings(result)
	return result
}

// collectAllSourceFiles gathers unique source file paths from all API elements.
// Paths are normalized: absolute paths are converted to include-path-relative
// by extracting the portion after common project prefixes (e.g., "mylib/public/foo.h").
func collectAllSourceFiles(spec *apispec.APISpec, projectRoot string) []string {
	seen := make(map[string]bool)
	var result []string
	add := func(f string) {
		f = normalizeHeaderPath(f, projectRoot)
		if f != "" && !seen[f] {
			seen[f] = true
			result = append(result, f)
		}
	}
	for _, fn := range spec.Functions {
		add(fn.SourceFile)
	}
	for _, c := range spec.Classes {
		add(c.SourceFile)
		for _, m := range c.Methods {
			add(m.SourceFile)
		}
	}
	for _, e := range spec.Enums {
		add(e.SourceFile)
	}
	sort.Strings(result)
	return result
}

// normalizeHeaderPath converts an absolute header path to a relative include path.
func normalizeHeaderPath(path string, projectRoot string) string {
	if path == "" {
		return ""
	}
	// Bazel execroot pattern: .../execroot/_main/path → path
	if idx := strings.Index(path, "execroot/_main/"); idx >= 0 {
		path = path[idx+len("execroot/_main/"):]
	}
	// Bazel output pattern: bazel-out/.../bin/path → path
	if strings.HasPrefix(path, "bazel-out/") {
		if idx := strings.Index(path, "/bin/"); idx >= 0 {
			return path[idx+len("/bin/"):]
		}
	}
	// Already relative
	if !strings.HasPrefix(path, "/") {
		return path
	}
	// Try to make relative to project root
	if projectRoot != "" {
		root := strings.TrimSuffix(projectRoot, "/") + "/"
		if strings.HasPrefix(path, root) {
			return path[len(root):]
		}
	}
	// Absolute path: return as-is (will work with -I flags)
	return path
}

func collectHandleClasses(spec *apispec.APISpec) map[string]*apispec.Class {
	handles := make(map[string]*apispec.Class)
	for i := range spec.Classes {
		c := &spec.Classes[i]
		if !c.IsHandle {
			continue
		}
		if !isBridgeableClass(c) {
			continue
		}
		handles[c.QualName] = c
	}
	return handles
}

// GenerateBridgeHeader generates the api_bridge.h header.
func GenerateBridgeHeader(spec *apispec.APISpec, packageName string) string {
	var b strings.Builder
	guard := strings.ToUpper(packageName) + "_API_BRIDGE_H_"

	b.WriteString("// Auto-generated by wasmify gen-proto. DO NOT EDIT.\n")
	fmt.Fprintf(&b, "#ifndef %s\n", guard)
	fmt.Fprintf(&b, "#define %s\n\n", guard)
	b.WriteString("#include <cstdint>\n\n")
	b.WriteString("extern \"C\" {\n\n")
	b.WriteString("void* wasm_alloc(int32_t size);\n")
	b.WriteString("void wasm_free(void* ptr);\n")
	b.WriteString("int32_t wasm_init();\n")
	b.WriteString("void wasm_shutdown();\n\n")
	b.WriteString("// Per-method entry points are exported as `w_<svc>_<mid>` and\n")
	b.WriteString("// `wasmify_get_type_name`; callers look them up by name via\n")
	b.WriteString("// `mod.ExportedFunction(...)` rather than going through a\n")
	b.WriteString("// single dispatch wrapper.\n\n")
	b.WriteString("} // extern \"C\"\n\n")
	fmt.Fprintf(&b, "#endif // %s\n", guard)

	return b.String()
}

// SaveBridge writes the bridge .cc and .h files to the data directory.
func SaveBridge(dataDir string, ccContent, hContent string) error {
	srcDir := filepath.Join(dataDir, "wasm-build", "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		return fmt.Errorf("failed to create src directory: %w", err)
	}

	ccPath := filepath.Join(srcDir, "api_bridge.cc")
	if err := os.WriteFile(ccPath, []byte(ccContent), 0o644); err != nil {
		return fmt.Errorf("failed to write api_bridge.cc: %w", err)
	}

	hPath := filepath.Join(srcDir, "api_bridge.h")
	if err := os.WriteFile(hPath, []byte(hContent), 0o644); err != nil {
		return fmt.Errorf("failed to write api_bridge.h: %w", err)
	}

	return nil
}

// extractTemplateArgFromQualType extracts the first template argument from a qualified type like "Span<const Foo>".
func extractTemplateArgFromQualType(qt string) string {
	start := strings.Index(qt, "<")
	if start < 0 {
		return ""
	}
	end := strings.LastIndex(qt, ">")
	if end <= start {
		return ""
	}
	return strings.TrimSpace(qt[start+1 : end])
}
