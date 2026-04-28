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
        // Handle is a submessage with field 1 = uint64 ptr
        ProtoWriter sub;
        sub.write_uint64(1, ptr);
        write_submessage(field, sub);
        free(sub.data_);
    }

    // ---- Repeated primitive helpers (packed encoding) ----
    // Note: this class lives inside extern "C" {}, so we cannot declare
    // member templates. Each packed variant is written out explicitly.

    void write_repeated_int32(uint32_t field, const std::vector<int32_t>& vec) {
        if (vec.empty()) return;
        ProtoWriter sub;
        for (int32_t v : vec) sub.write_varint(static_cast<uint64_t>(static_cast<uint32_t>(v)));
        write_submessage(field, sub);
        free(sub.data_);
    }
    void write_repeated_int64(uint32_t field, const std::vector<int64_t>& vec) {
        if (vec.empty()) return;
        ProtoWriter sub;
        for (int64_t v : vec) sub.write_varint(static_cast<uint64_t>(v));
        write_submessage(field, sub);
        free(sub.data_);
    }
    void write_repeated_uint32(uint32_t field, const std::vector<uint32_t>& vec) {
        if (vec.empty()) return;
        ProtoWriter sub;
        for (uint32_t v : vec) sub.write_varint(static_cast<uint64_t>(v));
        write_submessage(field, sub);
        free(sub.data_);
    }
    void write_repeated_uint64(uint32_t field, const std::vector<uint64_t>& vec) {
        if (vec.empty()) return;
        ProtoWriter sub;
        for (uint64_t v : vec) sub.write_varint(v);
        write_submessage(field, sub);
        free(sub.data_);
    }
    void write_repeated_bool(uint32_t field, const std::vector<bool>& vec) {
        if (vec.empty()) return;
        ProtoWriter sub;
        for (bool v : vec) sub.write_varint(v ? 1u : 0u);
        write_submessage(field, sub);
        free(sub.data_);
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
        return encode_result(data_, size_);
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
		innerName := ref.Inner.Name
		if innerName == "" {
			innerName = ref.Inner.QualType
		}
		if isSmartPointerType(innerName) || isSmartPointerType(cppTypeName(*ref.Inner)) {
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
	if !isProjectSource(c.SourceFile) {
		return false
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
	// Skip classes from disabled libraries.
	lib := classifyLibrary(c.SourceFile)
	if !isLibraryEnabled(lib) {
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
	// Strip qualifiers iteratively
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
	// Resolve short names to fully qualified names
	return resolveTypeName(name)
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
		// Two cases for vector<Handle>:
		//   - Pointer elements (std::vector<Foo*> or const Foo*):
		//     reinterpret the raw uint64 as the declared pointer
		//     type and push_back directly.
		//   - Value elements (std::vector<Foo> where Foo is bridged
		//     as a handle but the vector holds it by value): we
		//     receive a uint64 pointer from the Go side, dereference
		//     it to copy-construct the element, then push_back.
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

// vectorElementEmplaceBackExpr emits a C++ block that reads one
// submessage into per-field locals and emplace_back's into `varName`
// with the locals in field order. Use this for value classes that
// have no public default constructor but expose a constructor that
// takes their public fields positionally.
//
// Handle fields are stored as uint64_t locals to keep the reader
// assignment straightforward; the emplace_back arg-list casts them
// back to the declared pointer type so the constructor picks the
// right overload.
func vectorElementEmplaceBackExpr(c *apispec.Class, varName string) string {
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
	for i, f := range c.Fields {
		if i > 0 {
			b.WriteString(", ")
		}
		if f.Type.Kind == apispec.TypeHandle {
			cpp := cppTypeName(f.Type) + "*"
			if f.Type.IsConst {
				cpp = "const " + cpp
			}
			fmt.Fprintf(&b, "reinterpret_cast<%s>(_f%d)", cpp, i)
		} else {
			fmt.Fprintf(&b, "std::move(_f%d)", i)
		}
	}
	b.WriteString(");\n")
	b.WriteString("}")
	return b.String()
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
		return fmt.Sprintf("for (const auto& _elem : %s) {\n    ProtoWriter _subw;\n    %s\n    _pw.write_submessage(%d, _subw);\n    free(_subw.data_);\n}", varName, perElem, fieldNum)
	}
	return fmt.Sprintf("// TODO: serialize vector<%s>", inner.Name)
}

// writeValueReturnExpr emits C++ to serialize a POD/value struct return as
// a nested submessage at fieldNum. It looks the class up in valueClasses to
// find its fields, then emits field-by-field writes into a sub ProtoWriter.
func writeValueReturnExpr(ref apispec.TypeRef, fieldNum int, varName string) string {
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
	fmt.Fprintf(&sb, "    _pw.write_submessage(%d, _subw);\n    free(_subw.data_);\n}", fieldNum)
	return sb.String()
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
		// T** output: local is declared as T* pointer and the callee
		// writes the pointer value. Return the pointer directly.
		stripped := strings.TrimSuffix(strings.TrimSpace(qt), "const")
		stripped = strings.TrimSpace(stripped)
		if strings.HasSuffix(stripped, "**") {
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
			mName := toMethodConstName(rpcName)
			fmt.Fprintf(b, "static const int32_t METHOD_%s_%s = %d;\n", svcName, mName, methodID)
			methodID++
		}
		for _, f := range filterBridgeFields(c.Fields) {
			gName := "GET_" + strings.ToUpper(toSnakeCase(f.Name))
			fmt.Fprintf(b, "static const int32_t METHOD_%s_%s = %d;\n", svcName, gName, methodID)
			methodID++
		}
		// Downcast METHOD_X_TO_Y constants are intentionally NOT
		// emitted. Go type assertion covers abstract → concrete
		// conversion without a wasm round-trip. See CLAUDE.md:
		// "do not emit Downcast APIs".
		if isCallbackCandidateForBridge(c) {
			fmt.Fprintf(b, "static const int32_t METHOD_%s_FROM_CALLBACK = %d;\n", svcName, methodID)
			methodID++
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
// Exclusions:
//   - Non-abstract classes.
//   - Classes whose immediate base has no default constructor (the
//     trampoline's initializer list can't invoke a non-default parent
//     ctor without knowing the right arguments).
//   - Classes where any pure-virtual has a signature the trampoline
//     can't emit a valid C++ override declaration for — e.g. nested
//     types from shortened clang spellings (`api::VisitResult`), or
//     names that resolveTypeName can't map to a known class. Emitting
//     broken overrides pollutes the whole api_bridge.cc compile.
func isCallbackCandidateForBridge(c *apispec.Class) bool {
	if !c.IsAbstract {
		return false
	}
	if classNoDefaultCtor != nil && classNoDefaultCtor[c.QualName] {
		return false
	}
	// Every pure virtual must be declarable (otherwise the trampoline
	// can't satisfy the vtable and the subclass stays abstract).
	// Non-pure virtuals can be skipped on a per-method basis in the
	// override set — the base class impl still runs for those.
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
		// Vectors in callback declarations surface through qual_type;
		// unsupported in this iteration.
		return false
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

	fmt.Fprintf(b, "class %s : public %s {\n", trampolineName, cppType)
	b.WriteString("public:\n")
	fmt.Fprintf(b, "    explicit %s(int32_t callback_id) : _callback_id(callback_id) {}\n", trampolineName)
	fmt.Fprintf(b, "    ~%s() override = default;\n", trampolineName)

	for i := range methods {
		writeTrampolineMethod(b, &methods[i], i)
	}

	b.WriteString("private:\n")
	b.WriteString("    int32_t _callback_id;\n")
	b.WriteString("};\n\n")
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

	fmt.Fprintf(b, "    %s %s(%s)%s override {\n", retType, m.Name, paramDecls, constQual)

	// Check if every signature piece is supported; if not, abort.
	if !trampolineSignatureSupported(m) {
		b.WriteString("        // Unsupported signature for callback trampoline.\n")
		b.WriteString("        __builtin_trap();\n")
		// __builtin_trap is [[noreturn]] so clang doesn't require a
		// follow-up return statement.
		b.WriteString("    }\n")
		return
	}

	// Serialize inputs into a ProtoWriter. Output pointer params are
	// skipped here and captured on the response side instead.
	b.WriteString("        ProtoWriter _pw;\n")
	for i, p := range m.Params {
		if isOutputPointerHandle(p.Type) {
			continue
		}
		fieldNum := i + 1
		varName := paramVarName(p, i)
		writeTrampolineParamWrite(b, p, fieldNum, varName, "        ")
	}

	fmt.Fprintf(b, "        int64_t _rc = wasmify_callback_invoke(_callback_id, %d, _pw.data_, _pw.size_);\n", methodID)
	b.WriteString("        uintptr_t _resp_ptr = static_cast<uintptr_t>(static_cast<uint64_t>(_rc) >> 32);\n")
	b.WriteString("        int32_t _resp_len = static_cast<int32_t>(_rc & 0xFFFFFFFFu);\n")

	// If the method has output pointer params, decode them from the
	// response and write through.
	hasOutputPtrs := false
	for _, p := range m.Params {
		if isOutputPointerHandle(p.Type) {
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
			if !isOutputPointerHandle(p.Type) {
				continue
			}
			fieldNum := i + 1
			varName := paramVarName(p, i)
			// Inner type: strip both pointers and const from qual_type,
			// resolve, then re-add const+*.
			qt := strings.TrimSpace(p.Type.QualType)
			stripped := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(qt, "*")), "*"))
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
			fmt.Fprintf(b, "                    case %d: {\n", fieldNum)
			b.WriteString("                        uint64_t _p = read_handle_ptr(_pr);\n")
			fmt.Fprintf(b, "                        if (%s != nullptr) *%s = reinterpret_cast<%s>(_p);\n", varName, varName, ptrType)
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
		writeTrampolineResultRead(b, m.ReturnType, "        ")
	}
	b.WriteString("    }\n")
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
	if isOutputPointerHandle(t) {
		return true
	}
	switch t.Kind {
	case apispec.TypePrimitive:
		return primitiveToPbType(t.Name) != ""
	case apispec.TypeString:
		// std::string is marshalable. Views / compressed strings
		// (configured via UnsupportedStringTypes) aren't — they need
		// backing-store lifetime we can't guarantee across callbacks.
		return !isUnsupportedStringType(t.QualType)
	case apispec.TypeHandle:
		return true
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
	return strings.Contains(qt, "**")
}

func trampolineReturnSupported(t apispec.TypeRef) bool {
	if isErrorOnlyReturnType(t) {
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
	if isViewOfStringType(t) {
		// Keep qual_type verbatim so `const View<const std::string>&`
		// vs `View<const std::string>` both round-trip unchanged.
		if t.QualType != "" {
			return t.QualType
		}
	}
	if isOutputPointerHandle(t) {
		// Preserve qual_type (`const T**`) verbatim; fully-qualify the
		// inner class so the override matches the base's vtable slot.
		qt := strings.TrimSpace(t.QualType)
		// Normalize: `const T **` / `const T **` / `const T** `
		// → resolve the stripped base name and reassemble.
		stripped := qt
		stripped = strings.TrimSuffix(stripped, "*")
		stripped = strings.TrimSpace(stripped)
		stripped = strings.TrimSuffix(stripped, "*")
		stripped = strings.TrimSpace(stripped)
		hasConst := strings.HasPrefix(stripped, "const ")
		stripped = strings.TrimPrefix(stripped, "const ")
		stripped = strings.TrimSpace(stripped)
		resolved := resolveTypeName(stripped)
		if resolved == "" {
			resolved = stripped
		}
		if hasConst {
			return "const " + resolved + "**"
		}
		return resolved + "**"
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
		fmt.Fprintf(b, "%s_pw.write_string(%d, %s);\n", indent, fieldNum, varName)
	case apispec.TypeHandle:
		// Handle params wire as a submessage { uint64 ptr = 1 } so the
		// shape matches the Go client's pbAppendHandle encoding. Take
		// the address when passed by reference.
		if p.Type.IsPointer {
			fmt.Fprintf(b, "%s_pw.write_handle(%d, reinterpret_cast<uint64_t>(%s));\n", indent, fieldNum, varName)
		} else {
			fmt.Fprintf(b, "%s_pw.write_handle(%d, reinterpret_cast<uint64_t>(&%s));\n", indent, fieldNum, varName)
		}
	}
}

func writeTrampolineResultRead(b *strings.Builder, t apispec.TypeRef, indent string) {
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
	if isCallbackCandidateForBridge(c) {
		trampolineName := protoMessageName(c.QualName) + "Trampoline"
		emit(methodID, "FromCallback", func() {
			b.WriteString("    ProtoReader _pr(req, req_len);\n")
			b.WriteString("    int32_t _callback_id = 0;\n")
			b.WriteString("    while (_pr.next()) {\n")
			b.WriteString("        if (_pr.field() == 1) _callback_id = _pr.read_int32();\n")
			b.WriteString("        else _pr.skip();\n")
			b.WriteString("    }\n")
			fmt.Fprintf(b, "    auto* _t = new %s(_callback_id);\n", trampolineName)
			b.WriteString("    ProtoWriter _pw;\n")
			b.WriteString("    _pw.write_uint64(1, reinterpret_cast<uint64_t>(_t));\n")
			b.WriteString("    return _pw.finish();\n")
		})
		methodID++
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
			// T** output: callee writes a pointer value into *out_var.
			// Declare the local as `T* out_var = nullptr;` and pass
			// `&out_var` to produce `T**`. Preserve `const T**` by
			// adding `const` to the pointee type.
			qtTrim := strings.TrimSpace(p.Type.QualType)
			qtNoConst := strings.TrimSuffix(qtTrim, "const")
			qtNoConst = strings.TrimSpace(qtNoConst)
			if strings.HasSuffix(qtNoConst, "**") {
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
		// Handle by-value return: use direct heap construction to avoid
		// needing a copy or move constructor (guaranteed copy elision in C++17).
		// e.g., `auto* _result = new ScopedTimer(MakeScopedTimerStarted(...));`
		typeName := resolveTypeName(cppTypeName(fn.ReturnType))
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
			args = append(args, "&"+varName)
			outputIdx++
		} else {
			varName := paramVarName(p, inputIdx)
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
func isStaticFactory(m apispec.Function) bool {
	if !m.IsStatic {
		return false
	}
	if m.ReturnType.Kind != apispec.TypeHandle && m.ReturnType.Kind != apispec.TypeValue {
		return false
	}
	// Skip if return type is an error type (e.g., absl::Status)
	if matchErrorType(m.ReturnType.Name) != "" || matchErrorType(m.ReturnType.QualType) != "" {
		return false
	}
	// Skip static methods listed in the bridge config (e.g., protobuf internals).
	for _, skip := range bridgeConfig.SkipStaticMethods {
		if m.Name == skip {
			return false
		}
	}
	// Static factory must return by value (not pointer/reference).
	if m.ReturnType.IsPointer || m.ReturnType.IsRef {
		return false
	}
	// The return type must be the containing class.
	// Methods returning unrelated types are not factories.
	retName := cppTypeName(m.ReturnType)
	retQual := resolveTypeName(retName)
	containingClass := m.QualName
	if idx := strings.LastIndex(containingClass, "::"); idx >= 0 {
		containingClass = containingClass[:idx]
	}
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
	rpcName, _ := toProtoRPCName(m.Name)
	return rpcName != ""
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
		if p.Type.Kind == apispec.TypeHandle {
			castType := cppTypeName(p.Type)
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
		} else {
			args = append(args, varName)
		}
	}
	argStr := strings.Join(args, ", ")

	methodName := fn.Name
	if fn.OriginalName != "" {
		methodName = fn.OriginalName
	}

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
			args = append(args, "&"+varName)
			outputIdx++
		} else {
			varName := paramVarName(p, inputIdx)
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
