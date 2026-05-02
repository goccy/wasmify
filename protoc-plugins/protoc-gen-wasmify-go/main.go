// protoc-gen-wasmify-go generates Go client code for wasmify Protobuf
// services. The generator emits hand-rolled wire-format helpers, a
// singleton wazero-backed Module, and typed Go wrappers for each
// handle message — intentionally avoiding proto.Marshal/Unmarshal to
// keep runtime dependencies minimal and produce ergonomic APIs (no
// context.Context, no Handle suffix, no Module field on wrapper
// types, no public Free()).
//
// The plugin emits a single file named after the proto package
// (e.g. `mylib.go`) containing, in order:
//
//	- varint/wire helpers (pbAppend*, pbReader)
//	- Module struct + wazero init + callback host module
//	- abstract interfaces + cppTypeToGoType factory
//	- enum types + constants
//	- concrete handle types with methods
//	- free-function service methods
//	- env-import stubs and callback dispatch
//
// The wasm binary is //go:embed-ed by filename matching the package.
// Module dispatch caches one wazero api.Function per (service_id,
// method_id) export — the C++ bridge exposes each as a standalone
// `w_<svc>_<mid>` wasm export plus a separate `wasmify_get_type_name`
// for runtime typeid.
package main

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	wasmifyopts "github.com/goccy/wasmify/proto/wasmify"
)

// -----------------------------------------------------------------------------
// Entry point
// -----------------------------------------------------------------------------

func main() {
	protogen.Options{}.Run(func(plugin *protogen.Plugin) error {
		for _, f := range plugin.Files {
			if !f.Generate {
				continue
			}
			if !fileHasAny(f) {
				continue
			}
			generateFile(plugin, f)
		}
		return nil
	})
}

func fileHasAny(f *protogen.File) bool {
	if len(f.Services) > 0 {
		return true
	}
	for _, m := range f.Messages {
		if isHandleMessage(m) {
			return true
		}
	}
	return len(f.Enums) > 0
}

// -----------------------------------------------------------------------------
// Analyzed shapes (mirrors gengo.go's internal state)
// -----------------------------------------------------------------------------

// fieldInfo mirrors gengo.go's fieldInfo exactly — it drives encode/decode.
type fieldInfo struct {
	fieldNum      int
	fieldName     string
	goName        string
	goType        string
	wireType      int
	isHandle      bool
	handleName    string
	isAbstract    bool
	isRepeated    bool
	// isValueMsg is true when the field's proto type is a non-handle message
	// — it surfaces as a `*<valueTypeName>` struct in Go with its own
	// marshal()/unmarshalX() pair emitted in wasmify_values.go.
	isValueMsg    bool
	valueTypeName string
	kind          protoreflect.Kind
}

type msgInfo struct {
	name       string // Go type name (protoToGoType of proto message name)
	isAbstract bool
	parent     string   // Primary Go parent name (C++ first base class)
	parents    []string // Secondary parents (multiple-inheritance bases)
	// comment is the doc string lifted from the source C++ header
	// (via wasm_message_comment proto option). Empty when the source
	// declaration carried no comment.
	comment string
}

type svcMethodInfo struct {
	name         string
	methodID     int32
	methodType   string
	inputName    string
	outputName   string
	inputFields  []fieldInfo
	outputFields []fieldInfo
	// comment is the doc string lifted from the source C++ method
	// (via wasm_method_comment proto option). Empty when absent.
	comment string
	// originalName carries the un-renamed C++ identifier when the
	// proto-side rpcName was rewritten to dodge a proto3 lookup
	// collision (e.g. `empty` → `EmptyMethod` because Service.Empty
	// would shadow the global `Empty` message). The plugin uses this
	// to recover the natural Go method name (`Empty`) on the
	// receiver, since Go scopes methods to the receiver type and
	// has no proto-style lookup conflict.
	originalName string
	// goName is the identifier the plugin uses on the Go side. It
	// equals `name` for methods that did not need a proto-side
	// rename, and recovers the camel-cased originalName when it
	// can be safely re-introduced (no collision with the receiver's
	// embedded parent or with another method on the same handle).
	// Pre-computed once per service so every writeXxx site agrees.
	goName string
}

type svcInfo struct {
	name      string
	serviceID int32
	methods   []svcMethodInfo
	msgName   string // handle message Go name (or "" for free-function services)
}

type enumValueInfo struct {
	name    string
	value   int32
	comment string
}

type enumInfo struct {
	goName  string
	values  []enumValueInfo
	comment string
}

// -----------------------------------------------------------------------------
// Per-file generation
// -----------------------------------------------------------------------------

func generateFile(plugin *protogen.Plugin, file *protogen.File) {
	pkg := string(file.GoPackageName)
	importPath := file.GoImportPath

	// ---------- Phase 0: enums ----------
	var enums []enumInfo
	for _, e := range file.Enums {
		ei := enumInfo{
			goName:  protoToGoType(string(e.Desc.Name())),
			comment: getEnumComment(e),
		}
		for _, v := range e.Values {
			ei.values = append(ei.values, enumValueInfo{
				name:    string(v.Desc.Name()),
				value:   int32(v.Desc.Number()),
				comment: getEnumValueComment(v),
			})
		}
		enums = append(enums, ei)
	}

	// ---------- Phase 1: handle messages ----------
	messages := map[string]msgInfo{}
	for _, m := range file.Messages {
		if !isHandleMessage(m) {
			continue
		}
		goName := protoToGoType(string(m.Desc.Name()))
		var secondary []string
		for _, p := range getWasmParents(m) {
			secondary = append(secondary, protoToGoType(p))
		}
		messages[goName] = msgInfo{
			name:       goName,
			isAbstract: isAbstractMessage(m),
			parent:     protoToGoType(getWasmParent(m)),
			parents:    secondary,
			comment:    getMessageComment(m),
		}
	}

	// descriptor lookup by proto name → *protogen.Message for field analysis
	byProtoName := map[string]*protogen.Message{}
	for _, m := range file.Messages {
		byProtoName[string(m.Desc.Name())] = m
	}

	// ---------- Phase 2: services + fields ----------
	var services []svcInfo
	var callbackServices []svcInfo
	for _, svc := range file.Services {
		svcName := string(svc.Desc.Name())
		msgName := ""
		// Callback services pair with a handle service: "LoggerCallbackService"
		// describes the vtable of the "Logger" handle. Record that handle
		// name so the callback-adapter generator can construct the
		// `*Logger`-typed factory on top of RegisterCallback / invoke.
		isCallback := isCallbackService(svc)
		if isCallback {
			if strings.HasSuffix(svcName, "CallbackService") {
				msgName = protoToGoType(strings.TrimSuffix(svcName, "CallbackService"))
			}
		} else if strings.HasSuffix(svcName, "Service") {
			msgName = protoToGoType(strings.TrimSuffix(svcName, "Service"))
		}
		var methods []svcMethodInfo
		for _, m := range svc.Methods {
			inputName := string(m.Input.Desc.Name())
			outputName := string(m.Output.Desc.Name())

			var inputFields []fieldInfo
			for _, f := range m.Input.Fields {
				inputFields = append(inputFields, analyzeField(f, messages))
			}
			var outputFields []fieldInfo
			for _, f := range m.Output.Fields {
				fi := analyzeField(f, messages)
				if fi.fieldNum == 15 && fi.fieldName == "error" {
					continue
				}
				outputFields = append(outputFields, fi)
			}
			methods = append(methods, svcMethodInfo{
				name:         string(m.Desc.Name()),
				methodID:     int32(getWasmMethodId(m)),
				methodType:   getWasmMethodType(m),
				inputName:    inputName,
				outputName:   outputName,
				inputFields:  inputFields,
				outputFields: outputFields,
				comment:      getMethodComment(m),
				originalName: getOriginalName(m),
			})
		}
		// Resolve the Go-side method name once per service. Empty
		// msgName means a free-function service, where there is no
		// receiver and no parent-embedding to clash with.
		resolveGoMethodNames(methods, msgName, messages)
		info := svcInfo{
			name:      svcName,
			serviceID: int32(getWasmServiceId(svc)),
			methods:   methods,
			msgName:   msgName,
		}
		if isCallback {
			callbackServices = append(callbackServices, info)
		} else {
			services = append(services, info)
		}
	}

	// Prefer the real C++ namespace recorded by `wasmify gen-proto` in
	// the file-level wasm_cpp_namespace option. Fall back to deriving
	// it from the proto package ("wasmify.mylib" → "mylib") only when
	// the option is absent (older generators, hand-written protos).
	cppNS := getWasmCppNamespace(file)
	if cppNS == "" {
		if pkgPath := string(file.Desc.Package()); pkgPath != "" {
			if idx := strings.LastIndex(pkgPath, "."); idx >= 0 {
				cppNS = pkgPath[idx+1:]
			} else {
				cppNS = pkgPath
			}
		}
	}

	// ---------- Phase 2.5: collect value-type messages referenced in services ----------
	// Every non-handle message that surfaces as a field of a method input/output
	// gets a Go struct with marshal()/unmarshalX helpers so callers can populate
	// it field-by-field. Request/Response wrappers themselves are assembled
	// inline at call sites, so we look only at their fields' types, not at the
	// wrappers. Walk recursively: a value-type message may itself reference
	// other value-type messages in its own fields.
	valueMsgs := map[string]*protogen.Message{}
	var visit func(m *protogen.Message)
	visit = func(m *protogen.Message) {
		for _, f := range m.Fields {
			if f.Desc.Kind() != protoreflect.MessageKind || f.Message == nil {
				continue
			}
			goName := protoToGoType(string(f.Message.Desc.Name()))
			if _, isHandle := messages[goName]; isHandle {
				continue
			}
			if _, seen := valueMsgs[goName]; seen {
				continue
			}
			valueMsgs[goName] = f.Message
			visit(f.Message)
		}
	}
	for _, svc := range file.Services {
		if isCallbackService(svc) {
			continue
		}
		for _, m := range svc.Methods {
			visit(m.Input)
			visit(m.Output)
		}
	}

	// ---------- Phase 3: collect orphan / handle / free-function sets ----------
	// Compute handleServices first so orphan detection can see which
	// classes actually get a struct via the service batch vs which
	// need an orphan declaration.
	var handleServices []svcInfo
	for _, svc := range services {
		if svc.msgName == "" {
			continue
		}
		info, ok := messages[svc.msgName]
		if !ok {
			continue
		}
		_ = info
		// Abstract handles without a callback factory were historically
		// skipped here — but that meant their inherited accessor
		// methods (e.g. *ResolvedScan.ColumnList) were invisible from
		// Go, since Go has no inheritance and C++ subclasses can't
		// auto-promote methods. Emit the service batch unconditionally
		// so the bare abstract struct gets its methods; downstream code
		// obtains a *ResolvedScan via a downcast RPC and dispatches
		// against it directly.
		handleServices = append(handleServices, svc)
	}
	sort.SliceStable(handleServices, func(i, j int) bool {
		return handleServices[i].msgName < handleServices[j].msgName
	})

	// Orphans: any handle message that didn't make it into
	// handleServices needs a bare struct declaration so method
	// signatures that reference it (downcasts, abstract params, etc.)
	// can compile. Abstract classes without callback factories land
	// here — they're not instantiable from Go, but their types still
	// have to exist.
	handleServiceNames := map[string]bool{}
	for _, svc := range handleServices {
		handleServiceNames[svc.msgName] = true
	}
	var orphans []string
	for name := range messages {
		if handleServiceNames[name] {
			continue
		}
		orphans = append(orphans, name)
	}
	sort.Strings(orphans)

	var freeServices []svcInfo
	for _, svc := range services {
		if svc.msgName == "" {
			freeServices = append(freeServices, svc)
		}
	}

	// ---------- Phase 4: emit a single file per proto ----------
	// The generated code all lands in one file named after the proto
	// (`googlesql.proto` -> `googlesql.go`) so consuming repos see a
	// clean, project-scoped identifier rather than a suite of
	// `wasmify_*.go` helpers. file.GeneratedFilenamePrefix already
	// carries the correct module-relative path without the `.proto`
	// extension — `module=` aware buf strips the import-path prefix.
	var out strings.Builder
	out.WriteString("// Code generated by protoc-gen-wasmify-go. DO NOT EDIT.\n\n")
	fmt.Fprintf(&out, "package %s\n\n", pkg)
	out.WriteString(unifiedImports)
	out.WriteString("\n")

	// Quiet "imported and not used" when a particular section is absent
	// (e.g. a proto with only handles has no runtime.SetFinalizer users).
	out.WriteString(unifiedImportAnchors)
	out.WriteString("\n")

	// Build msgName → svc map early so generateInterfaces can lift
	// each abstract handle's own-service method signatures up into
	// the corresponding XNode interface — that's what gives
	// interface-typed variables callable inherited methods.
	handleServiceByMsg := map[string]svcInfo{}
	for _, svc := range handleServices {
		handleServiceByMsg[svc.msgName] = svc
	}

	out.WriteString(stripHeader(generateProtoHelpers(pkg)))
	out.WriteString(stripHeader(generateModule(pkg)))
	out.WriteString(stripHeader(generateInterfaces(pkg, messages, cppNS, handleServiceByMsg)))
	if len(enums) > 0 {
		out.WriteString(stripHeader(generateEnums(pkg, enums)))
	}
	if len(valueMsgs) > 0 {
		out.WriteString(stripHeader(generateValueTypes(pkg, valueMsgs, messages)))
	}
	if len(orphans) > 0 {
		out.WriteString(stripHeader(generateOrphanHandles(pkg, orphans, messages)))
	}
	// Callback services emit before handles so the adapter types (and the
	// `<Handle>Callback` interfaces they reference) are defined by the
	// time the handle batch's NewXFromImpl constructors mention them.
	if len(callbackServices) > 0 {
		out.WriteString(stripHeader(generateCallbackAdapters(pkg, callbackServices, handleServiceByMsg, messages)))
	}
	if len(handleServices) > 0 {
		out.WriteString(stripHeader(generateHandleBatch(pkg, handleServices, messages)))
	}
	if len(freeServices) > 0 {
		out.WriteString(stripHeader(generateClient(pkg, freeServices, messages)))
	}

	emit(plugin, importPath, file.GeneratedFilenamePrefix+".go", out.String())
}

// stripHeader removes the leading `// Code generated` comment, the
// `package ...` line, and any `import ( ... )` block from a generator's
// output so the body can be concatenated into the consolidated file.
// The individual `generate*` helpers still emit full Go files (useful
// for testing them in isolation) and this keeps that contract intact.
func stripHeader(src string) string {
	s := src
	// Drop the "Code generated" marker line.
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimLeft(s, "\n")
	// Drop the "package ..." line.
	if strings.HasPrefix(s, "package ") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
	}
	s = strings.TrimLeft(s, "\n")
	// Drop any leading import block.
	if strings.HasPrefix(s, "import (") {
		if i := strings.Index(s, "\n)"); i >= 0 {
			s = s[i+2:]
		}
	} else if strings.HasPrefix(s, "import ") {
		// Single-line `import "x"`.
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
	}
	return strings.TrimLeft(s, "\n")
}

// unifiedImports is the union of every import block any of the per-
// section generators produce. Keeping it centralized lets stripHeader
// throw away each section's local import list without missing a package.
const unifiedImports = `import (
	_ "embed"

	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)
`

// unifiedImportAnchors references every package in unifiedImports so
// that trivial proto files (no handles, no enums, etc.) still compile.
// Individual generator bodies already reference most of these, but a
// minimal file might not touch, say, runtime or math.
const unifiedImportAnchors = `var (
	_ = context.Background
	_ = binary.LittleEndian
	_ = errors.New
	_ = fmt.Errorf
	_ = math.Float32bits
	_ = runtime.SetFinalizer
	_ = sort.Search
	_ = strings.TrimSpace
	_ = strconv.Itoa
	_ = sync.Once{}
	_ = unsafe.Sizeof(byte(0))
	_ = wazero.NewRuntime
	_ = api.ValueType(0)
	_ = wasi_snapshot_preview1.Instantiate
)
`

func emit(plugin *protogen.Plugin, importPath protogen.GoImportPath, filename, content string) {
	g := plugin.NewGeneratedFile(filename, importPath)
	// g.P adds trailing newline per call; use raw writes instead so the
	// emitted file matches gengo.go byte-for-byte where possible.
	g.P(strings.TrimRight(content, "\n"))
}

// -----------------------------------------------------------------------------
// Field analysis (direct port of gengo.go analyzeField)
// -----------------------------------------------------------------------------

func analyzeField(f *protogen.Field, handles map[string]msgInfo) fieldInfo {
	fi := fieldInfo{
		fieldNum:   int(f.Desc.Number()),
		fieldName:  string(f.Desc.Name()),
		goName:     snakeToCamel(string(f.Desc.Name())),
		isRepeated: f.Desc.IsList(),
		kind:       f.Desc.Kind(),
	}
	switch f.Desc.Kind() {
	case protoreflect.BoolKind:
		fi.goType, fi.wireType = "bool", 0
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		fi.goType, fi.wireType = "int32", 0
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		fi.goType, fi.wireType = "int64", 0
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		fi.goType, fi.wireType = "uint32", 0
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		fi.goType, fi.wireType = "uint64", 0
	case protoreflect.FloatKind:
		fi.goType, fi.wireType = "float32", 5
	case protoreflect.DoubleKind:
		fi.goType, fi.wireType = "float64", 1
	case protoreflect.StringKind:
		fi.goType, fi.wireType = "string", 2
	case protoreflect.BytesKind:
		fi.goType, fi.wireType = "[]byte", 2
	case protoreflect.EnumKind:
		fi.wireType = 0
		if f.Enum != nil {
			fi.goType = protoToGoType(string(f.Enum.Desc.Name()))
		} else {
			fi.goType = "int32"
		}
	case protoreflect.MessageKind:
		fi.wireType = 2
		msgName := string(f.Message.Desc.Name())
		goName := protoToGoType(msgName)
		if info, ok := handles[goName]; ok {
			fi.isHandle = true
			fi.handleName = goName
			fi.isAbstract = info.isAbstract
			if fi.isAbstract {
				fi.goType = nodeIfaceName(goName)
			} else {
				fi.goType = "*" + goName
			}
		} else {
			// Non-handle message: exposed as a Go struct that carries its own
			// marshal()/unmarshalX helpers (emitted in wasmify_values.go). This
			// lets callers populate structured parameters (e.g. BuiltinFunction-
			// Options) field-by-field rather than handing the RPC opaque bytes.
			fi.isValueMsg = true
			fi.valueTypeName = goName
			fi.goType = "*" + goName
		}
	default:
		fi.goType, fi.wireType = "[]byte", 2
	}
	if fi.isRepeated {
		fi.goType = "[]" + fi.goType
	}
	return fi
}

// -----------------------------------------------------------------------------
// Code generators — static string constants
// -----------------------------------------------------------------------------

func generateProtoHelpers(pkg string) string {
	return "// Code generated by protoc-gen-wasmify-go. DO NOT EDIT.\n\n" +
		"package " + pkg + "\n\n" + protoHelpersBody
}

const protoHelpersBody = `import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
)

// pbAppendVarint appends a varint-encoded uint64.
func pbAppendVarint(buf []byte, v uint64) []byte {
	for v >= 0x80 {
		buf = append(buf, byte(v)|0x80)
		v >>= 7
	}
	return append(buf, byte(v))
}

// pbAppendTag appends a field tag.
func pbAppendTag(buf []byte, field, wireType uint32) []byte {
	return pbAppendVarint(buf, uint64(field<<3|wireType))
}

func pbAppendUint64(buf []byte, field uint32, v uint64) []byte {
	buf = pbAppendTag(buf, field, 0)
	return pbAppendVarint(buf, v)
}

func pbAppendInt32(buf []byte, field uint32, v int32) []byte {
	buf = pbAppendTag(buf, field, 0)
	return pbAppendVarint(buf, uint64(v))
}

func pbAppendInt64(buf []byte, field uint32, v int64) []byte {
	buf = pbAppendTag(buf, field, 0)
	return pbAppendVarint(buf, uint64(v))
}

func pbAppendBool(buf []byte, field uint32, v bool) []byte {
	buf = pbAppendTag(buf, field, 0)
	if v {
		return append(buf, 1)
	}
	return append(buf, 0)
}

func pbAppendString(buf []byte, field uint32, s string) []byte {
	buf = pbAppendTag(buf, field, 2)
	buf = pbAppendVarint(buf, uint64(len(s)))
	return append(buf, s...)
}

func pbAppendBytes(buf []byte, field uint32, data []byte) []byte {
	buf = pbAppendTag(buf, field, 2)
	buf = pbAppendVarint(buf, uint64(len(data)))
	return append(buf, data...)
}

func pbAppendFloat(buf []byte, field uint32, v float32) []byte {
	buf = pbAppendTag(buf, field, 5)
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], math.Float32bits(v))
	return append(buf, tmp[:]...)
}

func pbAppendDouble(buf []byte, field uint32, v float64) []byte {
	buf = pbAppendTag(buf, field, 1)
	var tmp [8]byte
	binary.LittleEndian.PutUint64(tmp[:], math.Float64bits(v))
	return append(buf, tmp[:]...)
}

func pbAppendSubmessage(buf []byte, field uint32, sub []byte) []byte {
	buf = pbAppendTag(buf, field, 2)
	buf = pbAppendVarint(buf, uint64(len(sub)))
	return append(buf, sub...)
}

func pbAppendHandle(buf []byte, field uint32, ptr uint64) []byte {
	sub := pbAppendUint64(nil, 1, ptr)
	return pbAppendSubmessage(buf, field, sub)
}

// pbReader is a streaming protobuf decoder.
type pbReader struct {
	data []byte
	pos  int
}

func (r *pbReader) hasData() bool { return r.pos < len(r.data) }

func (r *pbReader) readVarint() uint64 {
	var v uint64
	var shift uint
	for r.pos < len(r.data) {
		b := r.data[r.pos]
		r.pos++
		v |= uint64(b&0x7f) << shift
		if b < 0x80 {
			return v
		}
		shift += 7
	}
	return v
}

func (r *pbReader) next() (field, wireType uint32, ok bool) {
	if !r.hasData() {
		return 0, 0, false
	}
	tag := r.readVarint()
	return uint32(tag >> 3), uint32(tag & 7), true
}

func (r *pbReader) readString() string {
	n := int(r.readVarint())
	if r.pos+n > len(r.data) {
		n = len(r.data) - r.pos
	}
	s := string(r.data[r.pos : r.pos+n])
	r.pos += n
	return s
}

func (r *pbReader) readBytes() []byte {
	n := int(r.readVarint())
	if r.pos+n > len(r.data) {
		n = len(r.data) - r.pos
	}
	b := make([]byte, n)
	copy(b, r.data[r.pos:r.pos+n])
	r.pos += n
	return b
}

func (r *pbReader) readBool() bool {
	return r.readVarint() != 0
}

func (r *pbReader) readInt32() int32 {
	return int32(r.readVarint())
}

func (r *pbReader) readInt64() int64 {
	return int64(r.readVarint())
}

func (r *pbReader) readUint32() uint32 {
	return uint32(r.readVarint())
}

func (r *pbReader) readUint64() uint64 {
	return r.readVarint()
}

func (r *pbReader) readFloat() float32 {
	if r.pos+4 > len(r.data) {
		r.pos = len(r.data)
		return 0
	}
	v := binary.LittleEndian.Uint32(r.data[r.pos : r.pos+4])
	r.pos += 4
	return math.Float32frombits(v)
}

func (r *pbReader) readDouble() float64 {
	if r.pos+8 > len(r.data) {
		r.pos = len(r.data)
		return 0
	}
	v := binary.LittleEndian.Uint64(r.data[r.pos : r.pos+8])
	r.pos += 8
	return math.Float64frombits(v)
}

func (r *pbReader) readSubmessage() *pbReader {
	n := int(r.readVarint())
	if r.pos+n > len(r.data) {
		n = len(r.data) - r.pos
	}
	sub := &pbReader{data: r.data[r.pos : r.pos+n]}
	r.pos += n
	return sub
}

func (r *pbReader) skip(wireType uint32) {
	switch wireType {
	case 0: // varint
		r.readVarint()
	case 1: // 64-bit
		r.pos += 8
	case 2: // length-delimited
		n := int(r.readVarint())
		r.pos += n
	case 5: // 32-bit
		r.pos += 4
	}
	if r.pos > len(r.data) {
		r.pos = len(r.data)
	}
}

// pbExtractError scans a response for field 15 (error string).
func pbExtractError(resp []byte) error {
	r := &pbReader{data: resp}
	for f, w, ok := r.next(); ok; f, w, ok = r.next() {
		if f == 15 {
			return errors.New(r.readString())
		}
		r.skip(w)
	}
	return nil
}

// readHandlePtr reads the ptr from a response. Handles two patterns:
// 1. Direct varint: field 1, wireType 0 → read varint directly
// 2. Submessage: field 1, wireType 2 → read submessage, then field 1 varint inside
func readHandlePtr(data []byte) uint64 {
	r := &pbReader{data: data}
	for f, w, ok := r.next(); ok; f, w, ok = r.next() {
		if f == 1 {
			if w == 0 {
				// Direct varint (constructor pattern)
				return r.readVarint()
			}
			if w == 2 {
				// Submessage (write_handle pattern)
				sub := r.readSubmessage()
				for sf, sw, sok := sub.next(); sok; sf, sw, sok = sub.next() {
					if sf == 1 && sw == 0 {
						return sub.readVarint()
					}
					sub.skip(sw)
				}
			}
			return 0
		}
		r.skip(w)
	}
	return 0
}

// invokeMethod fans the wasm call out into the runtime, then folds in
// the (very common) pbExtractError check on the response. Every per-
// method client wrapper used to repeat the same three lines; this
// helper collapses them to one call.
func invokeMethod(svc, mid int32, req []byte) ([]byte, error) {
	resp, err := module().invoke(svc, mid, req)
	if err != nil {
		return nil, err
	}
	if e := pbExtractError(resp); e != nil {
		return nil, e
	}
	return resp, nil
}

// readPtrAtField returns the handle pointer encoded at response field f.
// f == 1 takes the fast path via readHandlePtr (which already handles
// both the direct-varint constructor shape and the submessage write_handle
// shape); other fields are walked once to find the correct submessage.
// Returns 0 if the field is not present, which is the standard "absent
// handle" sentinel — callers translate that to (nil, nil).
func readPtrAtField(resp []byte, f uint32) uint64 {
	if f == 1 {
		return readHandlePtr(resp)
	}
	pr := &pbReader{data: resp}
	for ff, w, ok := pr.next(); ok; ff, w, ok = pr.next() {
		if ff == f {
			if w == 2 {
				sub := pr.readSubmessage()
				return readHandlePtr(sub.data)
			}
			return 0
		}
		pr.skip(w)
	}
	return 0
}

// decodeChildHandle is the standard accessor-return shape. It reads
// the handle pointer at field f, returns nil for missing/zero, and
// otherwise constructs the child via ctor and pins the parent (owner)
// onto it via setKeepAlive when owner is non-nil. Constructor-style
// callers (free functions) pass owner=nil; accessors pass the
// receiver. The child must already implement the unexported keepAlive
// interface, which every generated handle struct does.
func decodeChildHandle[T any](resp []byte, field uint32, owner any, ctor func(uint64) *T) *T {
	ptr := readPtrAtField(resp, field)
	if ptr == 0 {
		return nil
	}
	child := ctor(ptr)
	if owner != nil {
		if ka, ok := any(child).(interface{ setKeepAlive(any) }); ok {
			ka.setKeepAlive(owner)
		}
	}
	return child
}

// decodeAbstractAs is the abstract-return counterpart of
// decodeChildHandle: read field f, look up the runtime concrete type
// via resolveAbstractHandle, type-assert to I, attach keepAlive when
// owner is non-nil. ifaceName is only used in the error message when
// the runtime type does not satisfy I (which is a real bug, not a
// "missing field" condition).
func decodeAbstractAs[I any](resp []byte, field uint32, owner any, ifaceName string) (I, error) {
	var z I
	ptr := readPtrAtField(resp, field)
	if ptr == 0 {
		return z, nil
	}
	resolved, err := resolveAbstractHandle(ptr)
	if err != nil {
		return z, err
	}
	if v, ok := resolved.(I); ok {
		if owner != nil {
			if ka, okKA := any(v).(interface{ setKeepAlive(any) }); okKA {
				ka.setKeepAlive(owner)
			}
		}
		return v, nil
	}
	return z, fmt.Errorf("resolved type %T does not implement %s", resolved, ifaceName)
}

// readScalarAtField walks resp once for field f and reads the value via
// the read closure. Missing field returns the zero value. The closure is
// always one of (*pbReader).readBool / readInt32 / readInt64 / readUint32
// / readUint64 / readFloat / readDouble / readString / readBytes - small
// values, but they fold the per-method scan-and-read loop into one call
// site.
func readScalarAtField[T any](resp []byte, f uint32, read func(*pbReader) T) T {
	var z T
	pr := &pbReader{data: resp}
	for ff, w, ok := pr.next(); ok; ff, w, ok = pr.next() {
		if ff == f {
			return read(pr)
		}
		pr.skip(w)
	}
	return z
}

// factoryFor curries a typed constructor (newXNoFinalizer) into the
// untyped factory shape stored in cppTypeFactories. Without it the
// generated cppTypeToGoType map had to repeat
//   "googlesql::Foo": func(ptr uint64) interface{} { return newFooNoFinalizer(ptr) }
// per concrete type — ~1,400 structurally-identical closures.
func factoryFor[T any](ctor func(uint64) *T) func(uint64) interface{} {
	return func(ptr uint64) interface{} { return ctor(ptr) }
}

// enumString backs the per-enum String() methods. Generated code emits
// parallel _vals_<E> []int32 and _names_<E> []string slices keyed in
// canonical order; this helper does a linear scan and falls back to
// the decimal form for values that aren't in the table (forward
// compatibility when new values land before regeneration).
func enumString(v int32, vals []int32, names []string) string {
	for i, x := range vals {
		if x == v {
			return names[i]
		}
	}
	return strconv.Itoa(int(v))
}

var _ = errors.New
var _ = binary.LittleEndian
var _ = fmt.Errorf
var _ = math.Float32bits
var _ = strconv.Itoa
`

func generateModule(pkg string) string {
	body := strings.ReplaceAll(moduleBody, "__WASM_FILE__", pkg+".wasm")
	return "// Code generated by protoc-gen-wasmify-go. DO NOT EDIT.\n\n" +
		"package " + pkg + "\n\n" + body
}

const moduleBody = `import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// embeddedWasm is the wasm binary that backs every API call in this
// package. The plugin emits a //go:embed directive whose filename
// matches the Go package name (e.g. ` + "`googlesql.wasm`" + ` for package
// ` + "`googlesql`" + `); ship that file alongside the generated sources so
// ` + "`go build`" + ` can resolve the embed at compile time.
//
//go:embed __WASM_FILE__
var embeddedWasm []byte

// Module wraps a wazero wasm module with auto-generated bridge methods.
//
// The wasm bridge exposes one wasm export per (service, method) pair
// (named ` + "`w_<svc>_<mid>`" + `) instead of routing through a single
// ` + "`wasm_invoke`" + ` dispatcher. exportFns caches the looked-up
// ` + "`api.Function`" + ` keyed by serviceID<<32 | methodID so the per-call
// overhead is one map read after the first invocation.
type Module struct {
	mu               sync.Mutex
	runtime          wazero.Runtime
	mod              api.Module
	allocFn          api.Function
	freeFn           api.Function
	typeNameFn       api.Function
	exportFns        map[uint64]api.Function
	callbacks        map[int32]CallbackHandler
	nextCBID         int32
	compilationCache wazero.CompilationCache // owned; closed in Close()
}

var (
	globalModule *Module
	initOnce     sync.Once
	initErr      error
)

// Option configures the wasm runtime.
type Option func(*options)
type options struct {
	compilationMode     CompilationMode
	compilationCacheDir string
}

// CompilationMode selects the wazero execution engine.
type CompilationMode int

const (
	// CompilationModeInterpreter uses the wazero interpreter (default).
	CompilationModeInterpreter CompilationMode = iota
	// CompilationModeCompiler uses the wazero compiler for faster execution.
	CompilationModeCompiler
)

// WithCompilationMode sets the wazero compilation mode.
func WithCompilationMode(mode CompilationMode) Option {
	return func(o *options) { o.compilationMode = mode }
}

// WithCompilationCache enables the wazero on-disk compilation cache
// rooted at dir. The first Init() call with a fresh dir pays the
// compile-from-bytes cost; every subsequent process pointed at the
// same dir starts up substantially faster because the precompiled
// module bytes are served straight from disk.
//
// Only honored when CompilationMode is CompilationModeCompiler — the
// interpreter does no AOT compile and so has nothing to cache; the
// option is silently ignored in that case rather than panicking, so
// callers can flip CompilationMode without rewriting their option
// list. The directory is created (mkdir -p) on first use.
//
// The cache's lifecycle is owned internally: Init constructs it,
// Close (or program exit) flushes and closes it. An empty dir is
// treated as "no cache" — the cache directory must be a valid path
// that the process can read and write.
func WithCompilationCache(dir string) Option {
	return func(o *options) { o.compilationCacheDir = dir }
}

// CallbackHandler is implemented by Go types that need to be called from C++.
// The C++ bridge calls wasmify_callback_invoke(callbackID, methodID, reqPtr, reqLen),
// which dispatches to the registered handler.
type CallbackHandler interface {
	HandleCallback(methodID int32, req []byte) ([]byte, error)
}

// RegisterCallback registers a Go callback handler and returns its ID.
// The ID can be passed to C++ as a handle pointer for callback dispatch.
func RegisterCallback(handler CallbackHandler) int32 {
	m := module()
	if m.callbacks == nil {
		m.callbacks = make(map[int32]CallbackHandler)
	}
	m.nextCBID++
	id := m.nextCBID
	m.callbacks[id] = handler
	return id
}

// UnregisterCallback removes a previously registered callback handler.
func UnregisterCallback(id int32) {
	module().callbacks[id] = nil
	delete(module().callbacks, id)
}

func (m *Module) handleCallback(callbackID, methodID, reqPtr, reqLen int32) int64 {
	handler, ok := m.callbacks[callbackID]
	if !ok {
		return 0
	}
	var req []byte
	if reqLen > 0 {
		req, _ = m.mod.Memory().Read(uint32(reqPtr), uint32(reqLen))
		// Copy to avoid referencing wasm memory after return
		buf := make([]byte, len(req))
		copy(buf, req)
		req = buf
	}
	resp, err := handler.HandleCallback(methodID, req)
	if err != nil {
		// Encode error response
		resp = pbAppendString(nil, 15, err.Error())
	}
	if len(resp) == 0 {
		return 0
	}
	// Write response to wasm memory
	ctx := context.Background()
	results, callErr := m.allocFn.Call(ctx, uint64(len(resp)))
	if callErr != nil || len(results) == 0 {
		return 0
	}
	ptr := results[0]
	m.mod.Memory().Write(uint32(ptr), resp)
	return int64(ptr)<<32 | int64(len(resp))
}

// envStub returns a GoModuleFunction for an unresolved env import.
// Most functions return zeros, but some require specific behavior
// for the wasm module to initialize correctly. The patterns below
// handle standard C++ runtime functions (__cxa_*) and common thread
// primitives that any C++ project compiled to wasm may import.
func envStub(m *Module, name string, params, results []api.ValueType) api.GoModuleFunction {
	switch {
	case strings.Contains(name, "SemWait") || strings.Contains(name, "sem_wait"):
		// Thread semaphore wait — return success (1) since wasm is single-threaded.
		return semWaitStub{}
	case strings.Contains(name, "allocate_exception"):
		// C++ exception allocation (__cxa_allocate_exception) — allocate from wasm memory.
		return &cxaAllocExceptionStub{module: m}
	case strings.Contains(name, "__cxa_throw") || strings.HasSuffix(name, "_throw"):
		// C++ throw — abort since we can't unwind in wasm.
		return cxaThrowStub{}
	default:
		return defaultStub{results: results}
	}
}

type defaultStub struct{ results []api.ValueType }

func (s defaultStub) Call(_ context.Context, _ api.Module, stack []uint64) {
	for i := range stack {
		if i < len(s.results) {
			stack[i] = 0
		}
	}
}

type semWaitStub struct{}

func (semWaitStub) Call(_ context.Context, _ api.Module, stack []uint64) {
	// Return true (success) — wasm is single-threaded, no wait needed.
	if len(stack) > 0 {
		stack[0] = 1
	}
}

type cxaAllocExceptionStub struct{ module *Module }

func (s *cxaAllocExceptionStub) Call(ctx context.Context, mod api.Module, stack []uint64) {
	// Allocate exception object in wasm memory.
	size := stack[0]
	if size == 0 {
		size = 64
	}
	if s.module.allocFn != nil {
		results, err := s.module.allocFn.Call(ctx, size)
		if err == nil && len(results) > 0 {
			stack[0] = results[0]
			return
		}
	}
	stack[0] = 0
}

type cxaThrowStub struct{}

func (cxaThrowStub) Call(_ context.Context, _ api.Module, _ []uint64) {
	panic("C++ exception thrown in wasm")
}

// Init initializes the global module from the embedded wasm binary.
// Must be called before any API use. Safe to call multiple times
// (uses sync.Once).
func Init(opts ...Option) error {
	initOnce.Do(func() {
		o := &options{}
		for _, opt := range opts {
			opt(o)
		}
		initErr = initModule(embeddedWasm, o)
	})
	return initErr
}

// Close shuts down the global module. Optional — for clean shutdown.
// Releases the wazero runtime and, if WithCompilationCache was used,
// flushes and closes the underlying on-disk cache so any in-flight
// writes land before the process exits. The first error encountered
// is returned; both teardown steps run regardless.
func Close() error {
	if globalModule == nil {
		return nil
	}
	ctx := context.Background()
	rtErr := globalModule.runtime.Close(ctx)
	var cacheErr error
	if globalModule.compilationCache != nil {
		cacheErr = globalModule.compilationCache.Close(ctx)
		globalModule.compilationCache = nil
	}
	if rtErr != nil {
		return rtErr
	}
	return cacheErr
}

// module returns the initialized global module. Panics if Init was not called.
func module() *Module {
	if globalModule == nil {
		panic("wasmify: Init() must be called before using any API")
	}
	return globalModule
}

func initModule(wasmBytes []byte, o *options) error {
	ctx := context.Background()
	var cfg wazero.RuntimeConfig
	switch o.compilationMode {
	case CompilationModeCompiler:
		cfg = wazero.NewRuntimeConfigCompiler()
	default:
		cfg = wazero.NewRuntimeConfigInterpreter()
	}
	// CompilationCache is a Compiler-only feature; the interpreter
	// never produces machine code and would not consult the cache,
	// so silently dropping the option there avoids wazero panicking
	// on a config it can't honor.
	var compilationCache wazero.CompilationCache
	if o.compilationCacheDir != "" && o.compilationMode == CompilationModeCompiler {
		c, err := wazero.NewCompilationCacheWithDir(o.compilationCacheDir)
		if err != nil {
			return fmt.Errorf("compilation cache %q: %w", o.compilationCacheDir, err)
		}
		compilationCache = c
		cfg = cfg.WithCompilationCache(c)
	}
	r := wazero.NewRuntimeWithConfig(ctx, cfg)
	wasi_snapshot_preview1.MustInstantiate(ctx, r)
	m := &Module{runtime: r, compilationCache: compilationCache}
	// Register the "wasmify" host module with callback_invoke.
	wasmifyMod := r.NewHostModuleBuilder("wasmify")
	wasmifyMod.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, callbackID, methodID int32, reqPtr, reqLen int32) int64 {
			return m.handleCallback(callbackID, methodID, reqPtr, reqLen)
		}).
		Export("callback_invoke")
	if _, err := wasmifyMod.Instantiate(ctx); err != nil {
		r.Close(ctx)
		return fmt.Errorf("wasmify host module: %w", err)
	}
	// Register stub "env" module for unresolved symbols.
	compiled, err := r.CompileModule(ctx, wasmBytes)
	if err != nil {
		r.Close(ctx)
		return fmt.Errorf("wasm compile: %w", err)
	}
	envBuilder := r.NewHostModuleBuilder("env")
	for _, imp := range compiled.ImportedFunctions() {
		modName, fnName, _ := imp.Import()
		if modName != "env" {
			continue
		}
		params := imp.ParamTypes()
		results := imp.ResultTypes()
		envBuilder.NewFunctionBuilder().
			WithGoModuleFunction(envStub(m, fnName, params, results), params, results).
			Export(fnName)
	}
	if _, err := envBuilder.Instantiate(ctx); err != nil {
		r.Close(ctx)
		return fmt.Errorf("env stub module: %w", err)
	}
	// Instantiate as reactor (don't call _start) since the wasm is a library.
	// Mount the root filesystem so wasm can access timezone data, etc.
	modConfig := wazero.NewModuleConfig().
		WithStartFunctions().
		WithFSConfig(wazero.NewFSConfig().WithDirMount("/", "/"))
	mod, err := r.InstantiateWithConfig(ctx, wasmBytes, modConfig)
	if err != nil {
		r.Close(ctx)
		return fmt.Errorf("wasm instantiate: %w", err)
	}
	allocFn := mod.ExportedFunction("wasm_alloc")
	freeFn := mod.ExportedFunction("wasm_free")
	if allocFn == nil || freeFn == nil {
		r.Close(ctx)
		return fmt.Errorf("wasm module missing required exports (wasm_alloc, wasm_free)")
	}
	// Call _initialize (wasi reactor init) to run C++ global constructors.
	if initializeFn := mod.ExportedFunction("_initialize"); initializeFn != nil {
		if _, err := initializeFn.Call(ctx); err != nil {
			r.Close(ctx)
			return fmt.Errorf("_initialize: %w", err)
		}
	}
	if initFn := mod.ExportedFunction("wasm_init"); initFn != nil {
		if _, err := initFn.Call(ctx); err != nil {
			r.Close(ctx)
			return fmt.Errorf("wasm_init: %w", err)
		}
	}
	m.mod = mod
	m.allocFn = allocFn
	m.freeFn = freeFn
	// wasmify_get_type_name is the runtime-typeid helper invoked when
	// abstract returns must be downcast to a concrete Go type. Cache it
	// at startup so dispatch sites don't repeat the lookup.
	m.typeNameFn = mod.ExportedFunction("wasmify_get_type_name")
	m.exportFns = make(map[uint64]api.Function)
	globalModule = m
	return nil
}

// invoke calls the wasm export for (serviceID, methodID), serializing
// req into wasm memory and unpacking the (ptr<<32 | len) response.
// Each (svc, mid) pair has its own export named ` + "`w_<svc>_<mid>`" + ` and the
// looked-up api.Function is cached after the first call.
func (m *Module) invoke(serviceID, methodID int32, req []byte) ([]byte, error) {
	fn, err := m.exportFor(serviceID, methodID)
	if err != nil {
		return nil, err
	}
	return m.callExport(fn, req)
}

// exportFor returns the cached api.Function for (serviceID, methodID),
// performing a one-time mod.ExportedFunction lookup if absent.
func (m *Module) exportFor(serviceID, methodID int32) (api.Function, error) {
	key := uint64(uint32(serviceID))<<32 | uint64(uint32(methodID))
	m.mu.Lock()
	if fn, ok := m.exportFns[key]; ok {
		m.mu.Unlock()
		return fn, nil
	}
	m.mu.Unlock()
	name := fmt.Sprintf("w_%d_%d", serviceID, methodID)
	fn := m.mod.ExportedFunction(name)
	if fn == nil {
		return nil, fmt.Errorf("wasm export %q not found", name)
	}
	m.mu.Lock()
	m.exportFns[key] = fn
	m.mu.Unlock()
	return fn, nil
}

// callExport copies req into wasm memory, calls fn, and reads back the
// packed response. Holds the runtime lock for the duration of the wasm
// call to keep the underlying memory writes atomic w.r.t. the
// callback invoke path.
func (m *Module) callExport(fn api.Function, req []byte) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx := context.Background()
	var reqPtr, reqLen uint64
	if len(req) > 0 {
		results, err := m.allocFn.Call(ctx, uint64(len(req)))
		if err != nil {
			return nil, fmt.Errorf("wasm_alloc: %w", err)
		}
		reqPtr = results[0]
		reqLen = uint64(len(req))
		if !m.mod.Memory().Write(uint32(reqPtr), req) {
			return nil, fmt.Errorf("failed to write request to wasm memory")
		}
	}
	results, err := fn.Call(ctx, reqPtr, reqLen)
	if err != nil {
		return nil, fmt.Errorf("wasm export call: %w", err)
	}
	packed := results[0]
	respPtr := uint32(packed >> 32)
	respLen := uint32(packed & 0xFFFFFFFF)
	if respLen == 0 {
		return nil, nil
	}
	resp, ok := m.mod.Memory().Read(respPtr, respLen)
	if !ok {
		return nil, fmt.Errorf("failed to read response from wasm memory")
	}
	out := make([]byte, len(resp))
	copy(out, resp)
	m.freeFn.Call(ctx, uint64(respPtr))
	if reqPtr != 0 {
		m.freeFn.Call(ctx, reqPtr)
	}
	return out, nil
}

// resolveTypeName calls the C++ bridge to get the runtime type name
// of an object pointed to by ptr. Returns a fully qualified C++ class name
// like "ns::ASTQueryStatement".
func (m *Module) resolveTypeName(ptr uint64) (string, error) {
	if m.typeNameFn == nil {
		return "", fmt.Errorf("wasm export %q not found", "wasmify_get_type_name")
	}
	buf := pbAppendUint64(nil, 1, ptr)
	resp, err := m.callExport(m.typeNameFn, buf)
	if err != nil {
		return "", err
	}
	if e := pbExtractError(resp); e != nil {
		return "", e
	}
	r := &pbReader{data: resp}
	for f, w, ok := r.next(); ok; f, w, ok = r.next() {
		if f == 1 {
			return r.readString(), nil
		}
		r.skip(w)
	}
	return "", nil
}

var _ = fmt.Errorf
var _ = strings.HasPrefix
`

// -----------------------------------------------------------------------------
// Interfaces + cppTypeToGoType (port of gengo.go generateInterfaces)
// -----------------------------------------------------------------------------

// formatInterfaceMethodSignature renders a svcMethodInfo as a Go
// interface-method line (no body). Returns "" for methods that don't
// belong in the interface surface — constructors, the Free RPC,
// disambiguated overloads (the generator renamed one of them to end
// in a digit, and exposing both variants on the interface forces
// every concrete descendant to have matching overloads which they
// generally don't), and anything that would leak a handle-removing
// operation that doesn't make sense on an abstract base.
func formatInterfaceMethodSignature(m svcMethodInfo, handleName string, skipNames map[string]bool) string {
	switch m.methodType {
	case "constructor", "free", "static_factory", "callback_factory", "downcast":
		return ""
	}
	// Skip overloads: if the method name ends with a digit the
	// generator appended a disambiguation suffix (SupportsOrdering2
	// alongside SupportsOrdering). Concrete descendants usually only
	// override the primary signature, so including the disambiguated
	// variant in the interface would force subclasses to implement a
	// signature they don't actually have.
	if n := len(m.name); n > 0 && m.name[n-1] >= '0' && m.name[n-1] <= '9' {
		return ""
	}
	if skipNames[m.name] {
		return ""
	}
	var params []fieldInfo
	for _, f := range m.inputFields {
		if f.fieldNum == 1 && f.isHandle && f.handleName == handleName {
			continue
		}
		params = append(params, f)
	}
	var result fieldInfo
	hasResult := false
	for _, f := range m.outputFields {
		if f.isHandle && f.handleName == "Status" {
			continue
		}
		result = f
		hasResult = true
		break
	}
	paramStr := buildParamStr(params)
	methodName := m.goName
	if methodName == "" {
		methodName = m.name
	}
	if hasResult {
		return fmt.Sprintf("%s(%s) (%s, error)", methodName, paramStr, result.goType)
	}
	return fmt.Sprintf("%s(%s) error", methodName, paramStr)
}

// collectOverloadedNames returns the set of method names in svc that
// also appear with a disambiguation digit suffix. Both the primary
// and disambiguated form are excluded from the interface because the
// presence of an overload here implies subclasses only define one
// variant locally; forcing the interface to carry the primary
// signature would reject those subclasses.
func collectOverloadedNames(svc svcInfo) map[string]bool {
	names := map[string]bool{}
	for _, m := range svc.methods {
		names[m.name] = true
	}
	out := map[string]bool{}
	for name := range names {
		if n := len(name); n > 0 && name[n-1] >= '0' && name[n-1] <= '9' {
			// strip a single trailing digit
			base := name[:n-1]
			if names[base] {
				out[base] = true
			}
		}
	}
	return out
}

func generateInterfaces(pkg string, messages map[string]msgInfo, cppNamespace string, servicesByMsg map[string]svcInfo) string {
	var b strings.Builder
	b.WriteString("// Code generated by protoc-gen-wasmify-go. DO NOT EDIT.\n\n")
	fmt.Fprintf(&b, "package %s\n\n", pkg)
	b.WriteString("import (\n\t\"fmt\"\n\t\"sort\"\n)\n\nvar _ = fmt.Errorf\nvar _ = sort.Search\n\n")

	var abstractNames []string
	for name, info := range messages {
		if info.isAbstract {
			abstractNames = append(abstractNames, name)
		}
	}
	sort.Strings(abstractNames)

	for _, name := range abstractNames {
		info := messages[name]
		if doc := formatGoDocComment(info.comment); doc != "" {
			b.WriteString(doc)
		}
		fmt.Fprintf(&b, "type %s interface {\n", nodeIfaceName(name))
		if info.parent != "" {
			if parentInfo, ok := messages[info.parent]; ok && parentInfo.isAbstract {
				fmt.Fprintf(&b, "\t%s\n", nodeIfaceName(info.parent))
			}
		}
		b.WriteString("\trawPtr() uint64\n")
		fmt.Fprintf(&b, "\tis%s()\n", name)
		// Include method signatures of the abstract class's own
		// service. With the embedded-base pattern, concrete derived
		// structs inherit these methods via Go method promotion, so
		// listing them here lets interface-typed variables call the
		// inherited accessors directly without a type assertion.
		if svc, ok := servicesByMsg[name]; ok {
			skip := collectOverloadedNames(svc)
			for _, m := range svc.methods {
				sig := formatInterfaceMethodSignature(m, name, skip)
				if sig == "" {
					continue
				}
				fmt.Fprintf(&b, "\t%s\n", sig)
			}
		}
		b.WriteString("}\n\n")
	}

	// Concrete handles do NOT get a companion `<X>Node = *<X>` alias.
	// The alias would be pure noise: it adds no methods, and the
	// idiomatic Go spelling for a concrete handle is just `*X` (which
	// is what every accessor / constructor / field already returns).
	// Only abstract bases need an interface — those are emitted above
	// as `type <Class> interface { ... }` (with `Node` suffix
	// elided when the class itself ends in `Node`).

	// cppType{Names,Factories}: parallel sorted slices that replace the
	// old `map[string]func(uint64) interface{}` literal. The slices are
	// keyed in the same order, so a binary search on cppTypeNames yields
	// the matching factory index — produces a much smaller Go file than
	// the per-class map-literal closure (~1,400 distinct closures, all
	// structurally identical, collapsed to one curried `factoryFor`
	// helper plus two slices).
	//
	// NoFinalizer: abstract handles resolved here are almost always
	// views into a parent-owned tree (e.g. accessor returns). If Go GC
	// ran a finalizer on them, the Free RPC would delete memory the
	// parent's destructor will later delete again = UB. Freshly
	// constructed instances take the NewX path which installs its own
	// finalizer.
	var concreteNames []string
	for name, info := range messages {
		if !info.isAbstract {
			concreteNames = append(concreteNames, name)
		}
	}
	sort.Strings(concreteNames)
	type cppEntry struct {
		cpp string
		fac string
	}
	entries := make([]cppEntry, 0, len(concreteNames))
	for _, goName := range concreteNames {
		entries = append(entries, cppEntry{
			cpp: goNameToCppName(goName, cppNamespace),
			fac: "new" + goName + "NoFinalizer",
		})
	}
	// Sort by cpp name so binary search works (concreteNames is already
	// sorted by Go name; the C++ name order may differ — sort once here).
	sort.Slice(entries, func(i, j int) bool { return entries[i].cpp < entries[j].cpp })
	b.WriteString("// cppTypeNames / cppTypeFactories: parallel sorted slices replacing\n")
	b.WriteString("// the old cppTypeToGoType map literal. Looked up via sort.Search.\n")
	b.WriteString("var cppTypeNames = []string{\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "\t%q,\n", e.cpp)
	}
	b.WriteString("}\n\n")
	b.WriteString("var cppTypeFactories = []func(uint64) interface{}{\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "\tfactoryFor(%s),\n", e.fac)
	}
	b.WriteString("}\n\n")

	b.WriteString(`// resolveAbstractHandle queries the C++ runtime for the actual type of the
// object at ptr and returns the appropriate concrete Go handle type.
func resolveAbstractHandle(ptr uint64) (interface{}, error) {
	typeName, err := module().resolveTypeName(ptr)
	if err != nil {
		return nil, fmt.Errorf("resolveTypeName: %w", err)
	}
	i := sort.SearchStrings(cppTypeNames, typeName)
	if i < len(cppTypeNames) && cppTypeNames[i] == typeName {
		return cppTypeFactories[i](ptr), nil
	}
	return nil, fmt.Errorf("unknown C++ type %q for ptr 0x%x", typeName, ptr)
}
`)
	return b.String()
}

// goNameToCppName mirrors gengo.go: split on "_", lowercase the first char of
// all but the last segment, then join with "::". The leading namespace (derived
// from the proto package) is prepended.
func goNameToCppName(goName, ns string) string {
	parts := strings.Split(goName, "_")
	if len(parts) == 1 {
		if ns != "" {
			return ns + "::" + goName
		}
		return goName
	}
	for i := 0; i < len(parts)-1; i++ {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToLower(parts[i][:1]) + parts[i][1:]
		}
	}
	cppName := strings.Join(parts, "::")
	if ns != "" {
		return ns + "::" + cppName
	}
	return cppName
}

// -----------------------------------------------------------------------------
// Enums
// -----------------------------------------------------------------------------

func generateEnums(pkg string, enums []enumInfo) string {
	var b strings.Builder
	b.WriteString("// Code generated by protoc-gen-wasmify-go. DO NOT EDIT.\n\n")
	fmt.Fprintf(&b, "package %s\n\n", pkg)
	for _, e := range enums {
		if doc := formatGoDocComment(e.comment); doc != "" {
			b.WriteString(doc)
		}
		fmt.Fprintf(&b, "type %s int32\n\n", e.goName)
		b.WriteString("const (\n")
		prefix := toScreamingSnakeSimple(e.goName) + "_"
		// Keep track of the first-seen constant for each numeric value so
		// the String() implementation can print a stable canonical name
		// when multiple spellings share a value (common with UNSPECIFIED
		// aliases that collapse to 1 in proto3).
		type entry struct {
			name string
			val  int32
		}
		var emitted []entry
		seen := map[int32]string{}
		for _, v := range e.values {
			constName := v.name
			constName = strings.TrimPrefix(constName, prefix)
			if constName == "UNSPECIFIED" {
				continue
			}
			goConstName := e.goName + screamingSnakeToUpperCamel(constName)
			if doc := formatGoDocComment(v.comment); doc != "" {
				// Indent each comment line so it nests with the const block.
				for _, line := range strings.Split(strings.TrimRight(doc, "\n"), "\n") {
					b.WriteString("\t" + line + "\n")
				}
			}
			fmt.Fprintf(&b, "\t%s %s = %d\n", goConstName, e.goName, v.value)
			if _, ok := seen[v.value]; !ok {
				seen[v.value] = goConstName
				emitted = append(emitted, entry{goConstName, v.value})
			}
		}
		b.WriteString(")\n\n")

		// Stringer: every go-zetasql enum-backed type implements
		// fmt.Stringer; match that so code printing enum values with
		// "%v" or %s sees a readable name instead of the raw int. The
		// per-enum table is parallel int32+string slices; the shared
		// enumString helper does a linear scan and falls back to
		// strconv.Itoa for unknown values (forward compatibility when
		// new values are introduced without regenerating). This shape
		// is ~5x smaller than the per-enum switch it replaced.
		fmt.Fprintf(&b, "var _vals_%s = []int32{", e.goName)
		for i, en := range emitted {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%d", en.val)
		}
		b.WriteString("}\n")
		fmt.Fprintf(&b, "var _names_%s = []string{", e.goName)
		for i, en := range emitted {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%q", en.name)
		}
		b.WriteString("}\n")
		fmt.Fprintf(&b, "func (e %s) String() string { return enumString(int32(e), _vals_%s, _names_%s) }\n\n",
			e.goName, e.goName, e.goName)
	}
	return b.String()
}

// -----------------------------------------------------------------------------
// Orphan handles
// -----------------------------------------------------------------------------

func generateOrphanHandles(pkg string, names []string, messages map[string]msgInfo) string {
	var b strings.Builder
	b.WriteString("// Code generated by protoc-gen-wasmify-go. DO NOT EDIT.\n\n")
	fmt.Fprintf(&b, "package %s\n\n", pkg)
	for _, name := range names {
		parent := messages[name].parent
		secondary := messages[name].parents
		isAbstract := messages[name].isAbstract
		structName := handleStructName(name, isAbstract)
		// parentStruct/secondaryStructs translate parent class names to
		// their (possibly renamed) struct names so a child of an abstract
		// `<X>Node` parent embeds `*<X>NodeBase` rather than the
		// non-existent `*<X>Node`.
		parentStruct := handleStructName(parent, messages[parent].isAbstract)
		secondaryStructs := make([]string, len(secondary))
		for i, sp := range secondary {
			secondaryStructs[i] = handleStructName(sp, messages[sp].isAbstract)
		}
		if doc := formatGoDocComment(messages[name].comment); doc != "" {
			b.WriteString(doc)
		}
		if parent == "" {
			// Root orphan: carry keepAlive for parent-child lifetime
			// tracking (see notes in generateHandleServices).
			fmt.Fprintf(&b, "type %s struct {\n\tptr uint64\n\tkeepAlive []any\n}\n\n", structName)
			fmt.Fprintf(&b, "func (h *%s) rawPtr() uint64 { return h.ptr }\n\n", structName)
			fmt.Fprintf(&b, "func (h *%s) setKeepAlive(v any) { if h != nil && v != nil { h.keepAlive = append(h.keepAlive, v) } }\n\n", structName)
		} else {
			// Embed primary + secondary parents. Method promotion
			// lifts rawPtr and every inherited accessor through the
			// chain without explicit upcasts.
			fmt.Fprintf(&b, "type %s struct {\n\t*%s\n", structName, parentStruct)
			for _, sp := range secondaryStructs {
				fmt.Fprintf(&b, "\t*%s\n", sp)
			}
			b.WriteString("}\n\n")
		}
		// Self marker for abstract types so the companion XNode
		// interface is satisfiable by embedding descendants. The
		// marker name still uses the bare class name (matching the
		// interface's `is<Class>()` slot), but the receiver is the
		// possibly-renamed struct.
		if isAbstract {
			fmt.Fprintf(&b, "func (*%s) is%s() {}\n\n", structName, name)
		}
		// Emit both new<X>(ptr) and new<X>NoFinalizer(ptr). Orphan
		// handles don't own a Free RPC, so neither installs a
		// finalizer, but the NoFinalizer variant exists so the chain
		// of embedded parents in a derived handle can delegate to the
		// orphan without cycles.
		emitOrphanCtor := func(helper string) {
			fmt.Fprintf(&b, "func %s(ptr uint64) *%s {\n", helper, structName)
			if parent == "" {
				fmt.Fprintf(&b, "\treturn &%s{ptr: ptr}\n", structName)
			} else {
				fmt.Fprintf(&b, "\treturn &%s{\n", structName)
				fmt.Fprintf(&b, "\t\t%s: new%sNoFinalizer(ptr),\n", parentStruct, parentStruct)
				for _, sp := range secondaryStructs {
					fmt.Fprintf(&b, "\t\t%s: new%sNoFinalizer(ptr),\n", sp, sp)
				}
				b.WriteString("\t}\n")
			}
			b.WriteString("}\n\n")
		}
		emitOrphanCtor("new" + structName)
		emitOrphanCtor("new" + structName + "NoFinalizer")
	}
	return b.String()
}

// -----------------------------------------------------------------------------
// Callback adapters (C++ -> Go virtual dispatch)
// -----------------------------------------------------------------------------

// lowerFirst lowercases the first rune of s. Used to build unexported
// identifiers from exported type names (e.g., Logger -> logger).
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// generateCallbackAdapters emits, for each wasm_callback service, a
// public Go interface (the API the user implements) and an unexported
// adapter type that routes runtime CallbackHandler dispatch through
// the user's methods. Each callback service pairs 1:1 with a handle
// service whose `callback_factory` RPC is used by NewXFromImpl to
// allocate a C++ trampoline bound to the registered adapter.
//
// Only signatures with primitive / string / void shapes are fully
// wired; other parameter or return types are captured as opaque proto
// bytes so unsupported methods still compile — they are recognisable
// in user code by the `[]byte` fallback on the interface.
func generateCallbackAdapters(pkg string, callbackServices []svcInfo, handleByMsg map[string]svcInfo, messages map[string]msgInfo) string {
	var b strings.Builder
	b.WriteString("// Code generated by protoc-gen-wasmify-go. DO NOT EDIT.\n\n")
	fmt.Fprintf(&b, "package %s\n\n", pkg)
	b.WriteString("import (\n\t\"fmt\"\n\t\"runtime\"\n)\n\nvar _ = fmt.Errorf\nvar _ = runtime.SetFinalizer\n\n")

	sort.SliceStable(callbackServices, func(i, j int) bool {
		return callbackServices[i].msgName < callbackServices[j].msgName
	})

	for _, svc := range callbackServices {
		writeCallbackAdapter(&b, svc, messages)
	}
	return b.String()
}

func writeCallbackAdapter(b *strings.Builder, svc svcInfo, messages map[string]msgInfo) {
	handleName := svc.msgName
	if handleName == "" {
		// Service name didn't match the "XCallbackService" convention; skip.
		return
	}
	interfaceName := handleName + "Callback"
	adapterName := lowerFirst(handleName) + "CallbackAdapter"

	// Interface
	fmt.Fprintf(b, "type %s interface {\n", interfaceName)
	for _, m := range svc.methods {
		fmt.Fprintf(b, "\t%s(%s) %s\n", m.name, callbackParamList(m.inputFields), callbackReturnType(m.outputFields))
	}
	b.WriteString("}\n\n")

	// Default embed: a zero-value struct that provides a stub
	// implementation for every interface method (returning zero
	// values and a nil error). Users embed this and override only
	// the methods they care about, matching how Catalog subclassing
	// works in C++ where the base provides non-trivial default impls
	// for most virtuals.
	//
	// Naming: <Handle>CallbackDefaults — suffixed rather than
	// prefixed so it doesn't collide with classes named `DefaultX`
	// that happen to also be callback candidates (e.g., googlesql's
	// `DefaultParseTreeVisitor`, which would otherwise produce both
	// a `DefaultParseTreeVisitorCallback` interface from itself and
	// a `DefaultParseTreeVisitorCallback` struct from
	// `ParseTreeVisitor`).
	defaultName := interfaceName + "Defaults"
	fmt.Fprintf(b, "type %s struct{}\n\n", defaultName)
	for _, m := range svc.methods {
		params := callbackParamList(m.inputFields)
		retType := callbackReturnType(m.outputFields)
		fmt.Fprintf(b, "func (%s) %s(%s) %s { %s }\n",
			defaultName, m.name, params, retType, callbackDefaultBody(m.outputFields))
	}
	b.WriteString("\n")

	// Adapter struct
	fmt.Fprintf(b, "type %s struct {\n\timpl %s\n}\n\n", adapterName, interfaceName)

	// HandleCallback dispatch
	fmt.Fprintf(b, "func (a *%s) HandleCallback(methodID int32, req []byte) ([]byte, error) {\n", adapterName)
	b.WriteString("\tswitch methodID {\n")
	for _, m := range svc.methods {
		fmt.Fprintf(b, "\tcase %d: // %s\n", m.methodID, m.name)
		writeCallbackCaseBody(b, handleName, m)
	}
	b.WriteString("\t}\n")
	b.WriteString("\treturn nil, fmt.Errorf(\"unknown callback method %d\", methodID)\n")
	b.WriteString("}\n\n")
}

// callbackParamList builds the interface's parameter list for a callback
// method, mapping primitive/string proto fields to their Go equivalents.
// Unsupported kinds become []byte (opaque pass-through).
func callbackParamList(fields []fieldInfo) string {
	if len(fields) == 0 {
		return ""
	}
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		name := f.fieldName
		if name == "" {
			name = fmt.Sprintf("arg%d", f.fieldNum)
		}
		parts = append(parts, fmt.Sprintf("%s %s", goIdent(name), callbackGoType(f)))
	}
	return strings.Join(parts, ", ")
}

// callbackReturnType builds the Go return type expression for a callback
// method. Void maps to `error`; a single supported output maps to
// `(<type>, error)`; anything else falls back to `([]byte, error)`.
func callbackReturnType(fields []fieldInfo) string {
	if len(fields) == 0 {
		return "error"
	}
	if len(fields) == 1 {
		return fmt.Sprintf("(%s, error)", callbackGoType(fields[0]))
	}
	return "([]byte, error)"
}

// callbackDefaultBody returns the body expression for a Default<Xxx>
// method — an inline "return zero values + nil error" that matches
// the return signature from callbackReturnType. Kept on one line so
// the Default struct stays compact (users grep it to find what they
// can override).
func callbackDefaultBody(fields []fieldInfo) string {
	if len(fields) == 0 {
		return "return nil"
	}
	if len(fields) == 1 {
		return fmt.Sprintf("return %s, nil", zeroValueLiteral(callbackGoType(fields[0])))
	}
	return "return nil, nil"
}

// zeroValueLiteral returns a Go literal that produces the zero value
// for t, suitable for inline use in a return statement.
func zeroValueLiteral(t string) string {
	switch t {
	case "string":
		return `""`
	case "bool":
		return "false"
	case "int32", "int64", "uint32", "uint64", "float32", "float64":
		return "0"
	case "[]byte", "[]string":
		return "nil"
	}
	// Pointers, interfaces, named handles: nil is always valid.
	return "nil"
}

func callbackGoType(f fieldInfo) string {
	if f.isRepeated {
		// analyzeField already prepends "[]" to the element type, so
		// `repeated string` arrives here as goType=="[]string". Only
		// `[]string` is fully wired into the callback adapter so far;
		// any other list shape falls back to opaque `[]byte`.
		if f.goType == "[]string" {
			return "[]string"
		}
		return "[]byte"
	}
	if f.isHandle {
		// Handle params/returns surface as their typed Go handle.
		// Abstract types use the XNode interface form so a caller can
		// satisfy the contract with any concrete subclass.
		return f.goType
	}
	switch f.goType {
	case "string", "bool", "float32", "float64",
		"int32", "int64", "uint32", "uint64":
		return f.goType
	}
	return "[]byte"
}

// writeCallbackCaseBody emits the per-method dispatch body: decode the
// input proto into the typed params, call the user's interface method,
// then encode the return value back into a proto response.
func writeCallbackCaseBody(b *strings.Builder, handleName string, m svcMethodInfo) {
	// Check if any input field is unsupported — in that case we can't
	// safely decode the request; surface a runtime error instead of
	// emitting a call to impl with undefined locals.
	supportedInputs := true
	for _, f := range m.inputFields {
		if callbackGoType(f) == "[]byte" && f.goType != "bytes" {
			supportedInputs = false
			break
		}
	}
	if !supportedInputs {
		fmt.Fprintf(b, "\t\treturn nil, fmt.Errorf(%q)\n", "callback method "+m.name+" has unsupported input types")
		return
	}
	if len(m.inputFields) == 0 {
		// No inputs: skip decoding loop.
	} else {
		// Declare + initialize local vars for each input field.
		for _, f := range m.inputFields {
			fmt.Fprintf(b, "\t\tvar %s %s\n", goIdent(f.fieldName), callbackGoType(f))
		}
		b.WriteString("\t\t_r := &pbReader{data: req}\n")
		b.WriteString("\t\tfor _f, _w, _ok := _r.next(); _ok; _f, _w, _ok = _r.next() {\n")
		b.WriteString("\t\t\tswitch _f {\n")
		for _, f := range m.inputFields {
			switch {
			case f.isRepeated && f.goType == "[]string":
				// Repeated string: append each occurrence to the slice.
				fmt.Fprintf(b, "\t\t\tcase %d: %s = append(%s, _r.readString())\n",
					f.fieldNum, goIdent(f.fieldName), goIdent(f.fieldName))
			case f.isHandle:
				// Submessage { uint64 ptr = 1 }; wrap as typed handle.
				fmt.Fprintf(b, "\t\t\tcase %d:\n", f.fieldNum)
				b.WriteString("\t\t\t\t_sub := _r.readSubmessage()\n")
				b.WriteString("\t\t\t\tvar _ptr uint64\n")
				b.WriteString("\t\t\t\tfor _sf, _sw, _sok := _sub.next(); _sok; _sf, _sw, _sok = _sub.next() {\n")
				b.WriteString("\t\t\t\t\tif _sf == 1 { _ptr = _sub.readUint64() } else { _sub.skip(_sw) }\n")
				b.WriteString("\t\t\t\t}\n")
				if f.isAbstract {
					fmt.Fprintf(b, "\t\t\t\tif _ptr != 0 { if _v, _err := resolveAbstractHandle(_ptr); _err == nil { if _h, _ok := _v.(%s); _ok { %s = _h } } }\n", callbackGoType(f), goIdent(f.fieldName))
				} else {
					fmt.Fprintf(b, "\t\t\t\tif _ptr != 0 { %s = new%s(_ptr) }\n", goIdent(f.fieldName), f.handleName)
				}
			default:
				fmt.Fprintf(b, "\t\t\tcase %d: %s = _r.%s()\n", f.fieldNum, goIdent(f.fieldName), callbackReader(f))
			}
		}
		b.WriteString("\t\t\tdefault: _r.skip(_w)\n")
		b.WriteString("\t\t\t}\n")
		b.WriteString("\t\t}\n")
	}

	// Call + response
	args := make([]string, 0, len(m.inputFields))
	for _, f := range m.inputFields {
		args = append(args, goIdent(f.fieldName))
	}
	argList := strings.Join(args, ", ")

	switch len(m.outputFields) {
	case 0:
		fmt.Fprintf(b, "\t\terr := a.impl.%s(%s)\n", m.name, argList)
		b.WriteString("\t\tif err != nil { return nil, err }\n")
		b.WriteString("\t\treturn nil, nil\n")
	case 1:
		f := m.outputFields[0]
		fmt.Fprintf(b, "\t\t_v, err := a.impl.%s(%s)\n", m.name, argList)
		b.WriteString("\t\tif err != nil { return nil, err }\n")
		b.WriteString("\t\tvar _out []byte\n")
		if f.isHandle {
			// Encode the handle's ptr into a submessage on field N.
			b.WriteString("\t\tif _v == nil {\n")
			b.WriteString("\t\t\treturn _out, nil\n")
			b.WriteString("\t\t}\n")
			fmt.Fprintf(b, "\t\t_out = pbAppendHandle(_out, %d, _v.rawPtr())\n", f.fieldNum)
		} else {
			fmt.Fprintf(b, "\t\t_out = %s\n", callbackWriter(f, "_out", "_v"))
		}
		b.WriteString("\t\treturn _out, nil\n")
	default:
		// Multi-return not yet wired: treat as opaque.
		fmt.Fprintf(b, "\t\t_out, err := a.impl.%s(%s)\n", m.name, argList)
		b.WriteString("\t\tif err != nil { return nil, err }\n")
		b.WriteString("\t\treturn _out, nil\n")
	}
}

// callbackReader returns the pbReader method name for decoding a field
// of the given shape.
func callbackReader(f fieldInfo) string {
	switch f.goType {
	case "string":
		return "readString"
	case "bool":
		return "readBool"
	case "float32":
		return "readFloat"
	case "float64":
		return "readDouble"
	case "int32":
		return "readInt32"
	case "int64":
		return "readInt64"
	case "uint32":
		return "readUint32"
	case "uint64":
		return "readUint64"
	}
	return "readBytes"
}

// callbackWriter returns the pbAppend* expression that serializes the
// named local into the running output buffer.
func callbackWriter(f fieldInfo, bufName, valueName string) string {
	switch f.goType {
	case "string":
		return fmt.Sprintf("pbAppendString(%s, %d, %s)", bufName, f.fieldNum, valueName)
	case "bool":
		return fmt.Sprintf("pbAppendBool(%s, %d, %s)", bufName, f.fieldNum, valueName)
	case "float32":
		return fmt.Sprintf("pbAppendFloat(%s, %d, %s)", bufName, f.fieldNum, valueName)
	case "float64":
		return fmt.Sprintf("pbAppendDouble(%s, %d, %s)", bufName, f.fieldNum, valueName)
	case "int32":
		return fmt.Sprintf("pbAppendInt32(%s, %d, %s)", bufName, f.fieldNum, valueName)
	case "int64":
		return fmt.Sprintf("pbAppendInt64(%s, %d, %s)", bufName, f.fieldNum, valueName)
	case "uint32":
		return fmt.Sprintf("pbAppendUint64(%s, %d, uint64(%s))", bufName, f.fieldNum, valueName)
	case "uint64":
		return fmt.Sprintf("pbAppendUint64(%s, %d, %s)", bufName, f.fieldNum, valueName)
	}
	return fmt.Sprintf("pbAppendBytes(%s, %d, %s)", bufName, f.fieldNum, valueName)
}

// goIdent sanitizes a proto field name into a valid Go identifier,
// escaping Go keywords and lowercasing the first letter.
func goIdent(name string) string {
	if name == "" {
		return "arg"
	}
	// Simple lowerCamel from snake_case.
	parts := strings.Split(name, "_")
	for i, p := range parts {
		if i == 0 {
			continue
		}
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	result := strings.Join(parts, "")
	switch result {
	case "type", "range", "func", "var", "const", "map", "chan", "go", "select",
		"case", "default", "package", "import", "interface", "struct",
		"switch", "if", "else", "for", "break", "continue", "return",
		"defer", "goto", "fallthrough":
		result = result + "_"
	}
	return result
}

// -----------------------------------------------------------------------------
// Value-type messages
// -----------------------------------------------------------------------------

// generateValueTypes emits a Go struct, a marshal() method, and an
// unmarshalX function for every non-handle message that is referenced as a
// field in some RPC input/output (or transitively referenced by another
// value-type message). Callers populate these structs field-by-field and
// hand them to methods that expect the corresponding typed parameter; the
// encoding/decoding is identical to the handle-case wire format except the
// ptr-as-uint64 becomes a fully structured submessage.
func generateValueTypes(pkg string, valueMsgs map[string]*protogen.Message, messages map[string]msgInfo) string {
	var b strings.Builder
	b.WriteString("// Code generated by protoc-gen-wasmify-go. DO NOT EDIT.\n\n")
	fmt.Fprintf(&b, "package %s\n\n", pkg)

	names := make([]string, 0, len(valueMsgs))
	for n := range valueMsgs {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		msg := valueMsgs[name]
		var fields []fieldInfo
		for _, f := range msg.Fields {
			fields = append(fields, analyzeField(f, messages))
		}

		// Struct declaration. Struct fields are exported (upper-camel) so
		// callers can populate them directly; the fieldInfo.goName stays
		// lower-camel because it is also used as a method-parameter name
		// elsewhere, so we uppercase per-emission here.
		fmt.Fprintf(&b, "type %s struct {\n", name)
		for _, f := range fields {
			fmt.Fprintf(&b, "\t%s %s\n", upperFirst(f.goName), f.goType)
		}
		b.WriteString("}\n\n")

		// marshal(): reuse writeEncode by aliasing the field's goName to the
		// struct-accessor form ("v.Foo") so its emitted code references the
		// struct fields rather than bare locals.
		fmt.Fprintf(&b, "func (v *%s) marshal() []byte {\n", name)
		b.WriteString("\tif v == nil { return nil }\n")
		b.WriteString("\tvar buf []byte\n")
		for _, f := range fields {
			alias := f
			alias.goName = "v." + upperFirst(f.goName)
			writeEncode(&b, alias, "buf")
		}
		b.WriteString("\treturn buf\n}\n\n")

		// unmarshalX(): decode field-by-field into a fresh struct. Empty or
		// length-zero data yields a zero-valued struct.
		fmt.Fprintf(&b, "func unmarshal%s(data []byte) *%s {\n", name, name)
		fmt.Fprintf(&b, "\tv := &%s{}\n", name)
		b.WriteString("\tpr := &pbReader{data: data}\n")
		b.WriteString("\tfor f, w, ok := pr.next(); ok; f, w, ok = pr.next() {\n")
		b.WriteString("\t\tswitch f {\n")
		for _, f := range fields {
			fmt.Fprintf(&b, "\t\tcase %d:\n", f.fieldNum)
			writeValueFieldDecode(&b, f, "v."+upperFirst(f.goName), "\t\t\t")
		}
		b.WriteString("\t\tdefault: pr.skip(w)\n")
		b.WriteString("\t\t}\n")
		b.WriteString("\t}\n")
		b.WriteString("\treturn v\n}\n\n")
	}
	return b.String()
}

// writeValueFieldDecode emits the per-field decoder body for one case of the
// switch inside an unmarshalX function. It mirrors the RPC-return decoders
// but writes into the target struct field rather than a local named result.
func writeValueFieldDecode(b *strings.Builder, f fieldInfo, target, indent string) {
	if f.isHandle {
		if f.isRepeated {
			fmt.Fprintf(b, "%ssub := pr.readSubmessage()\n", indent)
			fmt.Fprintf(b, "%sp := readHandlePtr(sub.data)\n", indent)
			if f.isAbstract {
				fmt.Fprintf(b, "%sif p != 0 { if r, _ := resolveAbstractHandle(p); r != nil { if vv, ok := r.(%s); ok { %s = append(%s, vv) } } }\n",
					indent, strings.TrimPrefix(f.goType, "[]"), target, target)
			} else {
				elem := strings.TrimPrefix(strings.TrimPrefix(f.goType, "[]"), "*")
				fmt.Fprintf(b, "%sif p != 0 { %s = append(%s, new%s(p)) }\n", indent, target, target, elem)
			}
			return
		}
		fmt.Fprintf(b, "%ssub := pr.readSubmessage()\n", indent)
		fmt.Fprintf(b, "%sp := readHandlePtr(sub.data)\n", indent)
		if f.isAbstract {
			fmt.Fprintf(b, "%sif p != 0 { if r, _ := resolveAbstractHandle(p); r != nil { if vv, ok := r.(%s); ok { %s = vv } } }\n",
				indent, f.goType, target)
		} else {
			elem := strings.TrimPrefix(f.goType, "*")
			fmt.Fprintf(b, "%sif p != 0 { %s = new%s(p) }\n", indent, target, elem)
		}
		return
	}
	if f.isValueMsg {
		if f.isRepeated {
			fmt.Fprintf(b, "%ssub := pr.readSubmessage()\n", indent)
			fmt.Fprintf(b, "%s%s = append(%s, unmarshal%s(sub.data))\n", indent, target, target, f.valueTypeName)
			return
		}
		fmt.Fprintf(b, "%ssub := pr.readSubmessage()\n", indent)
		fmt.Fprintf(b, "%s%s = unmarshal%s(sub.data)\n", indent, target, f.valueTypeName)
		return
	}
	// Scalar / repeated scalar.
	read := readMethodForElemType("", f.kind)
	if f.isRepeated {
		// Accept both packed and unpacked encodings.
		packed := isPackedEligible(f.kind)
		if packed {
			fmt.Fprintf(b, "%sif w == 2 {\n", indent)
			fmt.Fprintf(b, "%s\tsub := pr.readSubmessage()\n", indent)
			fmt.Fprintf(b, "%s\tfor sub.hasData() { %s = append(%s, %s) }\n", indent, target, target, castRead(f.kind, f.goType, "sub."+read+"()"))
			fmt.Fprintf(b, "%s} else {\n", indent)
			fmt.Fprintf(b, "%s\t%s = append(%s, %s)\n", indent, target, target, castRead(f.kind, f.goType, "pr."+read+"()"))
			fmt.Fprintf(b, "%s}\n", indent)
		} else {
			fmt.Fprintf(b, "%s%s = append(%s, %s)\n", indent, target, target, castRead(f.kind, f.goType, "pr."+read+"()"))
		}
		return
	}
	if f.kind == protoreflect.EnumKind {
		fmt.Fprintf(b, "%s%s = %s(pr.readInt32())\n", indent, target, f.goType)
		return
	}
	fmt.Fprintf(b, "%s%s = pr.%s()\n", indent, target, readMethodName(f))
}

func isPackedEligible(k protoreflect.Kind) bool {
	switch k {
	case protoreflect.BoolKind,
		protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind,
		protoreflect.FloatKind, protoreflect.DoubleKind,
		protoreflect.EnumKind:
		return true
	}
	return false
}

// castRead wraps the bare pr.readX() call in the appropriate Go type
// conversion when the slice element type needs an explicit cast (enum ints
// vs plain int32; most other cases are a direct assignment).
func castRead(k protoreflect.Kind, goType, expr string) string {
	if k == protoreflect.EnumKind {
		elem := strings.TrimPrefix(goType, "[]")
		return elem + "(" + expr + ")"
	}
	return expr
}

// -----------------------------------------------------------------------------
// Concrete handles
// -----------------------------------------------------------------------------

func generateHandleBatch(pkg string, batch []svcInfo, messages map[string]msgInfo) string {
	var b strings.Builder
	b.WriteString("// Code generated by protoc-gen-wasmify-go. DO NOT EDIT.\n\n")
	fmt.Fprintf(&b, "package %s\n\n", pkg)
	b.WriteString("import (\n\t\"errors\"\n\t\"fmt\"\n\t\"runtime\"\n)\n\nvar _ = errors.New\nvar _ = fmt.Errorf\nvar _ = runtime.SetFinalizer\n\n")

	byMsgName := make(map[string]svcInfo, len(batch))
	for _, svc := range batch {
		if svc.msgName != "" {
			byMsgName[svc.msgName] = svc
		}
	}

	for _, svc := range batch {
		name := svc.msgName
		isAbstract := messages[name].isAbstract
		structName := handleStructName(name, isAbstract)

		hasFree := false
		for _, m := range svc.methods {
			if m.methodType == "free" {
				hasFree = true
				break
			}
		}

		// Emit struct using the embedded-base pattern. A class with no
		// parent (the ultimate base) gets `{ ptr uint64 }`; every
		// derived class embeds its immediate parent by pointer and
		// inherits rawPtr/RawPtr, markers, and base accessors via Go
		// method promotion. This mirrors the pattern go-zetasql's
		// ast.BaseNode / resolved_ast.BaseNode use, and lets callers
		// invoke inherited methods naturally (e.g.
		// `resolvedLiteral.NodeKind()` resolves to
		// `ResolvedNode.NodeKind()`).
		parent := messages[name].parent
		secondary := messages[name].parents
		// parentStruct/secondaryStructs translate parent class names
		// to their (possibly renamed) struct names so a child of an
		// abstract `<X>Node` parent embeds `*<X>NodeBase` rather than
		// the non-existent `*<X>Node`.
		parentStruct := handleStructName(parent, messages[parent].isAbstract)
		secondaryStructs := make([]string, len(secondary))
		for i, sp := range secondary {
			secondaryStructs[i] = handleStructName(sp, messages[sp].isAbstract)
		}
		if doc := formatGoDocComment(messages[name].comment); doc != "" {
			b.WriteString(doc)
		}
		if parent == "" {
			// The root carries an optional keepAlive reference back to
			// the handle that created this one (e.g. the parent that
			// returned this child handle from a method). Go's GC must
			// not free the parent while a pointer into its C++-owned
			// tree is still reachable through us, so accessor call sites
			// store the receiver on the returned child via setKeepAlive.
			fmt.Fprintf(&b, "type %s struct {\n\tptr uint64\n\tkeepAlive []any\n}\n\n", structName)
			fmt.Fprintf(&b, "func (h *%s) rawPtr() uint64 { return h.ptr }\n\n", structName)
			fmt.Fprintf(&b, "func (h *%s) setKeepAlive(v any) { if h != nil && v != nil { h.keepAlive = append(h.keepAlive, v) } }\n\n", structName)
		} else if len(secondary) == 0 {
			// Single inheritance: embed the primary parent. Method
			// promotion unambiguously lifts rawPtr and every
			// inherited accessor through the chain.
			fmt.Fprintf(&b, "type %s struct {\n\t*%s\n}\n\n", structName, parentStruct)
		} else {
			// Multiple inheritance: embedding both parents makes
			// their methods promote in, but any name they share
			// (notably `ptr` and `rawPtr()`) becomes ambiguous at
			// compile time. Give the derived struct its own `ptr`
			// field plus an explicit rawPtr() to shadow the
			// ambiguous promotions. The own ptr is used by this
			// class's own accessors; the embedded parents keep
			// their own copies of the same wasm pointer so their
			// inherited accessors still work via promotion.
			fmt.Fprintf(&b, "type %s struct {\n\tptr uint64\n\tkeepAlive []any\n\t*%s\n", structName, parentStruct)
			for _, sp := range secondaryStructs {
				fmt.Fprintf(&b, "\t*%s\n", sp)
			}
			b.WriteString("}\n\n")
			fmt.Fprintf(&b, "func (h *%s) rawPtr() uint64 { return h.ptr }\n\n", structName)
			fmt.Fprintf(&b, "func (h *%s) setKeepAlive(v any) { if h != nil && v != nil { h.keepAlive = append(h.keepAlive, v) } }\n\n", structName)
		}

		// Self marker: abstract classes provide the "is<Name>()" tag
		// so concrete descendants can satisfy the companion interface.
		// The marker selector still uses the bare class name (matching
		// the interface's `is<Class>()` slot); only the receiver is
		// the renamed struct.
		if isAbstract {
			fmt.Fprintf(&b, "func (*%s) is%s() {}\n\n", structName, name)
		}

		// Every handle type that has a constructor needs a new<X>(ptr)
		// helper. For the ultimate base we allocate directly; otherwise
		// delegate to new<Parent>(ptr) so the embedded chain is wired
		// correctly.
		//
		// The finalizer attaches ONLY on the outermost struct (the
		// type that `new<X>` is constructing). Parent structs in the
		// embedded chain hold the same wasm pointer — if they also
		// freed on GC we'd double-delete and corrupt the module. Since
		// Go GC collects parents as the outer becomes unreachable, a
		// single free at the leaf level is sufficient.
		emitCtorBody := func(installFinalizer bool) {
			if parent == "" {
				fmt.Fprintf(&b, "\th := &%s{ptr: ptr}\n", structName)
			} else {
				fmt.Fprintf(&b, "\th := &%s{\n", structName)
				if len(secondary) > 0 {
					// Multi-parent class owns its own ptr field to
					// resolve the `h.ptr` / rawPtr ambiguity that
					// arises from two embeds sharing the field name.
					fmt.Fprintf(&b, "\t\tptr: ptr,\n")
				}
				fmt.Fprintf(&b, "\t\t%s: new%sNoFinalizer(ptr),\n", parentStruct, parentStruct)
				for _, sp := range secondaryStructs {
					fmt.Fprintf(&b, "\t\t%s: new%sNoFinalizer(ptr),\n", sp, sp)
				}
				b.WriteString("\t}\n")
			}
			if installFinalizer && hasFree {
				fmt.Fprintf(&b, "\truntime.SetFinalizer(h, (*%s).free)\n", structName)
			}
			b.WriteString("\treturn h\n")
		}
		fmt.Fprintf(&b, "func new%s(ptr uint64) *%s {\n", structName, structName)
		emitCtorBody(true)
		b.WriteString("}\n\n")

		// Sibling helper without the finalizer — every other new<X> in
		// the chain invokes this so embedded parents share the same
		// ptr but don't race with the leaf's free on GC.
		fmt.Fprintf(&b, "func new%sNoFinalizer(ptr uint64) *%s {\n", structName, structName)
		emitCtorBody(false)
		b.WriteString("}\n\n")

		for _, m := range svc.methods {
			writeMethod(&b, svc, m, name, messages)
		}
	}
	_ = byMsgName
	return b.String()
}

func writeMethod(b *strings.Builder, svc svcInfo, m svcMethodInfo, handleName string, messages map[string]msgInfo) {
	switch m.methodType {
	case "constructor", "static_factory":
		writeConstructor(b, svc, m, handleName, messages)
	case "callback_factory":
		writeCallbackFactory(b, svc, m, handleName, messages)
	case "free":
		writeFree(b, svc, m, handleName, messages)
	case "getter":
		writeGetter(b, svc, m, handleName, messages)
	case "downcast":
		// Defensive: the generator no longer emits "downcast" RPCs
		// (Go type assertion replaces them), but legacy protos may
		// still carry the marker. Skip entirely.
	default:
		writeRegularMethod(b, svc, m, handleName, messages)
	}
}

// writeCallbackFactory emits `NewXFromImpl(impl XCallback) (*X, error)`
// which registers a Go adapter with the runtime callback registry and
// invokes the C++ FromCallback RPC to construct a trampoline handle
// backed by that callback ID. The handle's finalizer is re-bound so
// the callback is unregistered once the handle is freed — otherwise
// the adapter (and the user's closure it captures) would outlive the
// C++ object it stands in for.
func writeCallbackFactory(b *strings.Builder, svc svcInfo, m svcMethodInfo, handleName string, messages map[string]msgInfo) {
	// handleName is the original (un-renamed) class name and drives
	// all user-facing identifiers (`New<X>FromImpl`, `<X>Callback`
	// interface, adapter struct). The Go struct name may differ
	// (a `Base` suffix is appended when the abstract class itself
	// ends in `Node`); that suffix only surfaces on the receiver /
	// return type, never on the public symbol the user types.
	structName := handleStructName(handleName, messages[handleName].isAbstract)
	interfaceName := handleName + "Callback"
	adapterName := lowerFirst(handleName) + "CallbackAdapter"
	if doc := formatGoDocComment(m.comment); doc != "" {
		b.WriteString(doc)
	}
	fmt.Fprintf(b, "func New%sFromImpl(impl %s) (*%s, error) {\n", handleName, interfaceName, structName)
	fmt.Fprintf(b, "\tadapter := &%s{impl: impl}\n", adapterName)
	b.WriteString("\tid := RegisterCallback(adapter)\n")
	b.WriteString("\tvar buf []byte\n")
	b.WriteString("\tbuf = pbAppendInt32(buf, 1, id)\n")
	fmt.Fprintf(b, "\tresp, err := invokeMethod(%d, %d, buf)\n", svc.serviceID, m.methodID)
	b.WriteString("\tif err != nil { UnregisterCallback(id); return nil, err }\n")
	fmt.Fprintf(b, "\th := new%s(readScalarAtField(resp, 1, (*pbReader).readUint64))\n", structName)
	// new<X>(ptr) already installed a finalizer that just calls free.
	// Replace it with one that also unregisters the Go callback on
	// GC; runtime.SetFinalizer panics if the existing finalizer isn't
	// first cleared, so nil-reset before setting the new one.
	fmt.Fprintf(b, "\truntime.SetFinalizer(h, nil)\n")
	fmt.Fprintf(b, "\truntime.SetFinalizer(h, func(h *%s) { h.free(); UnregisterCallback(id) })\n", structName)
	b.WriteString("\treturn h, nil\n")
	b.WriteString("}\n\n")
}

func writeConstructor(b *strings.Builder, svc svcInfo, m svcMethodInfo, handleName string, messages map[string]msgInfo) {
	structName := handleStructName(handleName, messages[handleName].isAbstract)
	var params []fieldInfo
	for _, f := range m.inputFields {
		if f.isHandle && f.handleName == handleName {
			continue
		}
		params = append(params, f)
	}
	ctorSuffix := ""
	if m.name != "New" {
		ctorSuffix = strings.TrimPrefix(m.name, "New")
	}
	paramStr := buildParamStr(params)
	if doc := formatGoDocComment(m.comment); doc != "" {
		b.WriteString(doc)
	}
	fmt.Fprintf(b, "func New%s%s(%s) (*%s, error) {\n", handleName, ctorSuffix, paramStr, structName)
	b.WriteString("\tvar buf []byte\n")
	for _, p := range params {
		writeEncode(b, p, "buf")
	}
	fmt.Fprintf(b, "\tresp, err := invokeMethod(%d, %d, buf)\n", svc.serviceID, m.methodID)
	b.WriteString("\tif err != nil { return nil, err }\n")
	fmt.Fprintf(b, "\treturn new%s(readScalarAtField(resp, 1, (*pbReader).readUint64)), nil\n", structName)
	b.WriteString("}\n\n")
}

func writeFree(b *strings.Builder, svc svcInfo, m svcMethodInfo, handleName string, messages map[string]msgInfo) {
	structName := handleStructName(handleName, messages[handleName].isAbstract)
	fmt.Fprintf(b, "func (h *%s) free() {\n", structName)
	b.WriteString("\tif h.ptr != 0 {\n")
	b.WriteString("\t\tbuf := pbAppendHandle(nil, 1, h.ptr)\n")
	fmt.Fprintf(b, "\t\tmodule().invoke(%d, %d, buf)\n", svc.serviceID, m.methodID)
	b.WriteString("\t\th.ptr = 0\n")
	b.WriteString("\t}\n")
	b.WriteString("}\n\n")
	// No public Close()/release method is emitted: the wasm-side C++
	// object's lifetime is owned by the Go GC. Each handle has a
	// finalizer attached at construction time (see new<X>) which calls
	// free() once the handle becomes unreachable. Hot paths that need
	// to release more eagerly should drop their references and let the
	// runtime collect — exposing manual release would invite
	// use-after-free bugs without buying meaningful control over wasm
	// memory pressure.
}

func writeGetter(b *strings.Builder, svc svcInfo, m svcMethodInfo, handleName string, messages map[string]msgInfo) {
	structName := handleStructName(handleName, messages[handleName].isAbstract)
	methodName := m.goName
	if methodName == "" {
		methodName = m.name
	}
	if doc := formatGoDocComment(m.comment); doc != "" {
		b.WriteString(doc)
	}
	if len(m.outputFields) == 0 {
		fmt.Fprintf(b, "func (h *%s) %s() error {\n", structName, methodName)
		b.WriteString("\tbuf := pbAppendHandle(nil, 1, h.ptr)\n")
		fmt.Fprintf(b, "\t_, err := invokeMethod(%d, %d, buf)\n", svc.serviceID, m.methodID)
		b.WriteString("\treturn err\n")
		b.WriteString("}\n\n")
		return
	}
	var result fieldInfo
	for _, f := range m.outputFields {
		if f.isHandle && f.handleName == "Status" {
			continue
		}
		result = f
		break
	}
	if result.goType == "" {
		fmt.Fprintf(b, "func (h *%s) %s() error {\n", structName, methodName)
		b.WriteString("\tbuf := pbAppendHandle(nil, 1, h.ptr)\n")
		fmt.Fprintf(b, "\t_, err := invokeMethod(%d, %d, buf)\n", svc.serviceID, m.methodID)
		b.WriteString("\treturn err\n")
		b.WriteString("}\n\n")
		return
	}
	fmt.Fprintf(b, "func (h *%s) %s() (%s, error) {\n", structName, methodName, result.goType)
	b.WriteString("\tbuf := pbAppendHandle(nil, 1, h.ptr)\n")
	fmt.Fprintf(b, "\tresp, err := invokeMethod(%d, %d, buf)\n", svc.serviceID, m.methodID)
	fmt.Fprintf(b, "\tif err != nil { return %s, err }\n", zeroValue(result))
	writeDecode(b, result, structName)
	b.WriteString("}\n\n")
}

func writeRegularMethod(b *strings.Builder, svc svcInfo, m svcMethodInfo, handleName string, messages map[string]msgInfo) {
	structName := handleStructName(handleName, messages[handleName].isAbstract)
	var params []fieldInfo
	for _, f := range m.inputFields {
		if f.fieldNum == 1 && f.isHandle && f.handleName == handleName {
			continue
		}
		params = append(params, f)
	}
	var result fieldInfo
	hasResult := false
	for _, f := range m.outputFields {
		if f.isHandle && f.handleName == "Status" {
			continue
		}
		result = f
		hasResult = true
		break
	}
	paramStr := buildParamStr(params)
	methodName := m.goName
	if methodName == "" {
		methodName = m.name
	}
	if doc := formatGoDocComment(m.comment); doc != "" {
		b.WriteString(doc)
	}
	if hasResult {
		fmt.Fprintf(b, "func (h *%s) %s(%s) (%s, error) {\n", structName, methodName, paramStr, result.goType)
	} else {
		fmt.Fprintf(b, "func (h *%s) %s(%s) error {\n", structName, methodName, paramStr)
	}
	b.WriteString("\tbuf := pbAppendHandle(nil, 1, h.ptr)\n")
	for _, p := range params {
		writeEncode(b, p, "buf")
	}
	// Pin handle-typed arguments on the receiver ONLY for methods whose
	// name suggests the parent stores a reference to the child (AddX,
	// SetX, RegisterX, InsertX, AttachX, PushX). For pure setters that
	// copy the argument by value (most Get/Is/Has/Compute/Find methods
	// don't take handle args, and the few that do rarely retain), we
	// skip the pin to avoid piling up unnecessary references and bloating
	// Go heap. Call sites that know better can still keep their own Go
	// references.
	if methodRetainsHandleArgs(m.name) {
		for _, p := range params {
			if !p.isHandle {
				continue
			}
			if p.isRepeated {
				fmt.Fprintf(b, "\tfor _, _ka := range %s { h.setKeepAlive(_ka) }\n", p.goName)
			} else {
				fmt.Fprintf(b, "\th.setKeepAlive(%s)\n", p.goName)
			}
		}
	}
	if hasResult {
		fmt.Fprintf(b, "\tresp, err := invokeMethod(%d, %d, buf)\n", svc.serviceID, m.methodID)
		fmt.Fprintf(b, "\tif err != nil { return %s, err }\n", zeroValue(result))
		writeDecode(b, result, structName)
	} else {
		fmt.Fprintf(b, "\t_, err := invokeMethod(%d, %d, buf)\n", svc.serviceID, m.methodID)
		b.WriteString("\treturn err\n")
	}
	b.WriteString("}\n\n")
}

// -----------------------------------------------------------------------------
// Free-function client
// -----------------------------------------------------------------------------

func generateClient(pkg string, freeServices []svcInfo, messages map[string]msgInfo) string {
	var b strings.Builder
	b.WriteString("// Code generated by protoc-gen-wasmify-go. DO NOT EDIT.\n\n")
	fmt.Fprintf(&b, "package %s\n\n", pkg)
	b.WriteString("import (\n\t\"errors\"\n\t\"fmt\"\n\t\"runtime\"\n)\n\nvar _ = errors.New\nvar _ = fmt.Errorf\nvar _ = runtime.SetFinalizer\n\n")

	for _, svc := range freeServices {
		for _, m := range svc.methods {
			var params []fieldInfo
			params = append(params, m.inputFields...)
			var result fieldInfo
			hasResult := false
			for _, f := range m.outputFields {
				if f.isHandle && f.handleName == "Status" {
					continue
				}
				result = f
				hasResult = true
				break
			}
			paramStr := buildParamStr(params)
			methodName := m.goName
			if methodName == "" {
				methodName = m.name
			}
			if doc := formatGoDocComment(m.comment); doc != "" {
				b.WriteString(doc)
			}
			if hasResult {
				fmt.Fprintf(&b, "func %s(%s) (%s, error) {\n", methodName, paramStr, result.goType)
			} else {
				fmt.Fprintf(&b, "func %s(%s) error {\n", methodName, paramStr)
			}
			b.WriteString("\tvar buf []byte\n")
			for _, p := range params {
				writeEncode(&b, p, "buf")
			}
			if hasResult {
				fmt.Fprintf(&b, "\tresp, err := invokeMethod(%d, %d, buf)\n", svc.serviceID, m.methodID)
				fmt.Fprintf(&b, "\tif err != nil { return %s, err }\n", zeroValue(result))
				writeDecodeForModule(&b, result, params)
			} else {
				fmt.Fprintf(&b, "\t_, err := invokeMethod(%d, %d, buf)\n", svc.serviceID, m.methodID)
				b.WriteString("\treturn err\n")
			}
			b.WriteString("}\n\n")
		}
	}
	return b.String()
}

// -----------------------------------------------------------------------------
// Encode / decode helpers — literal port of gengo.go
// -----------------------------------------------------------------------------

func buildParamStr(params []fieldInfo) string {
	var parts []string
	for _, p := range params {
		parts = append(parts, p.goName+" "+p.goType)
	}
	return strings.Join(parts, ", ")
}

func writeEncode(b *strings.Builder, f fieldInfo, bufVar string) {
	fn := uint32(f.fieldNum)
	v := f.goName
	if f.isHandle {
		if f.isRepeated {
			fmt.Fprintf(b, "\tfor _, item := range %s {\n", v)
			fmt.Fprintf(b, "\t\tif item != nil { %s = pbAppendHandle(%s, %d, item.rawPtr()) }\n", bufVar, bufVar, fn)
			b.WriteString("\t}\n")
		} else {
			fmt.Fprintf(b, "\tif %s != nil { %s = pbAppendHandle(%s, %d, %s.rawPtr()) }\n", v, bufVar, bufVar, fn, v)
		}
		return
	}
	if f.isValueMsg {
		// Value-type submessage: delegate to the type's own marshal() helper
		// and wrap its bytes as a length-delimited sub-message field.
		if f.isRepeated {
			fmt.Fprintf(b, "\tfor _, item := range %s {\n", v)
			fmt.Fprintf(b, "\t\tif item != nil { %s = pbAppendSubmessage(%s, %d, item.marshal()) }\n", bufVar, bufVar, fn)
			b.WriteString("\t}\n")
		} else {
			fmt.Fprintf(b, "\tif %s != nil { %s = pbAppendSubmessage(%s, %d, %s.marshal()) }\n", v, bufVar, bufVar, fn, v)
		}
		return
	}
	if f.isRepeated {
		fmt.Fprintf(b, "\tfor _, item := range %s {\n", v)
		writeEncodeScalar(b, f.kind, fn, "item", bufVar, "\t\t")
		b.WriteString("\t}\n")
		return
	}
	writeEncodeScalar(b, f.kind, fn, v, bufVar, "\t")
}

func writeEncodeScalar(b *strings.Builder, kind protoreflect.Kind, fn uint32, v, bufVar, indent string) {
	switch kind {
	case protoreflect.StringKind:
		fmt.Fprintf(b, "%s%s = pbAppendString(%s, %d, %s)\n", indent, bufVar, bufVar, fn, v)
	case protoreflect.BoolKind:
		fmt.Fprintf(b, "%s%s = pbAppendBool(%s, %d, %s)\n", indent, bufVar, bufVar, fn, v)
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		fmt.Fprintf(b, "%s%s = pbAppendInt32(%s, %d, %s)\n", indent, bufVar, bufVar, fn, v)
	case protoreflect.EnumKind:
		fmt.Fprintf(b, "%s%s = pbAppendInt32(%s, %d, int32(%s))\n", indent, bufVar, bufVar, fn, v)
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		fmt.Fprintf(b, "%s%s = pbAppendInt64(%s, %d, %s)\n", indent, bufVar, bufVar, fn, v)
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		fmt.Fprintf(b, "%s%s = pbAppendUint64(%s, %d, uint64(%s))\n", indent, bufVar, bufVar, fn, v)
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		fmt.Fprintf(b, "%s%s = pbAppendUint64(%s, %d, %s)\n", indent, bufVar, bufVar, fn, v)
	case protoreflect.FloatKind:
		fmt.Fprintf(b, "%s%s = pbAppendFloat(%s, %d, %s)\n", indent, bufVar, bufVar, fn, v)
	case protoreflect.DoubleKind:
		fmt.Fprintf(b, "%s%s = pbAppendDouble(%s, %d, %s)\n", indent, bufVar, bufVar, fn, v)
	case protoreflect.BytesKind, protoreflect.MessageKind:
		// Non-handle message fields are represented as opaque []byte at the
		// Go layer (see analyzeField), so they encode the same way as bytes.
		fmt.Fprintf(b, "%s%s = pbAppendBytes(%s, %d, %s)\n", indent, bufVar, bufVar, fn, v)
	default:
		fmt.Fprintf(b, "%s// TODO: encode field %d (%s)\n", indent, fn, v)
	}
}

func writeDecode(b *strings.Builder, result fieldInfo, ownerHandleName string) {
	if result.isHandle && !result.isRepeated {
		// Single-handle return: delegate to the runtime helpers.
		// decodeAbstractAs walks the type-id resolver and asserts to
		// the requested interface; decodeChildHandle picks the
		// NoFinalizer constructor (accessor returns are views into
		// the parent's tree, not owned resources).
		if result.isAbstract {
			fmt.Fprintf(b, "\treturn decodeAbstractAs[%s](resp, %d, h, %q)\n",
				result.goType, result.fieldNum, result.goType)
		} else {
			fmt.Fprintf(b, "\treturn decodeChildHandle(resp, %d, h, new%sNoFinalizer), nil\n",
				result.fieldNum, result.handleName)
		}
		return
	}
	if result.isValueMsg && !result.isRepeated {
		// Value-type return: locate the submessage bytes on the given field
		// number, then delegate to the type's unmarshal helper. Empty / missing
		// field returns nil so callers can distinguish absence from zero value.
		b.WriteString("\tpr := &pbReader{data: resp}\n")
		b.WriteString("\tfor f, w, ok := pr.next(); ok; f, w, ok = pr.next() {\n")
		fmt.Fprintf(b, "\t\tif f == %d {\n", result.fieldNum)
		b.WriteString("\t\t\tsub := pr.readSubmessage()\n")
		fmt.Fprintf(b, "\t\t\treturn unmarshal%s(sub.data), nil\n", result.valueTypeName)
		b.WriteString("\t\t} else { pr.skip(w) }\n")
		b.WriteString("\t}\n")
		b.WriteString("\treturn nil, nil\n")
		return
	}
	if result.isValueMsg && result.isRepeated {
		elemType := strings.TrimPrefix(result.goType, "[]")
		fmt.Fprintf(b, "\tvar items %s\n", result.goType)
		b.WriteString("\tpr := &pbReader{data: resp}\n")
		b.WriteString("\tfor f, w, ok := pr.next(); ok; f, w, ok = pr.next() {\n")
		fmt.Fprintf(b, "\t\tif f == %d {\n", result.fieldNum)
		b.WriteString("\t\t\tsub := pr.readSubmessage()\n")
		fmt.Fprintf(b, "\t\t\titems = append(items, unmarshal%s(sub.data))\n", result.valueTypeName)
		b.WriteString("\t\t} else { pr.skip(w) }\n")
		b.WriteString("\t}\n")
		b.WriteString("\treturn items, nil\n")
		_ = elemType
		return
	}
	if result.isHandle && result.isRepeated {
		if result.isAbstract {
			elemType := result.goType
			fmt.Fprintf(b, "\tvar items %s\n", elemType)
			b.WriteString("\tpr := &pbReader{data: resp}\n")
			b.WriteString("\tfor f, w, ok := pr.next(); ok; f, w, ok = pr.next() {\n")
			fmt.Fprintf(b, "\t\tif f == %d {\n", result.fieldNum)
			b.WriteString("\t\t\tsub := pr.readSubmessage()\n")
			b.WriteString("\t\t\tp := readHandlePtr(sub.data)\n")
			b.WriteString("\t\t\tif p != 0 {\n")
			b.WriteString("\t\t\t\tresolved, _ := resolveAbstractHandle(p)\n")
			ifaceType := strings.TrimPrefix(elemType, "[]")
			fmt.Fprintf(b, "\t\t\t\tif v, ok := resolved.(%s); ok {\n", ifaceType)
			b.WriteString("\t\t\t\t\tif ka, okKA := v.(interface{ setKeepAlive(any) }); okKA { ka.setKeepAlive(h) }\n")
			b.WriteString("\t\t\t\t\titems = append(items, v)\n")
			b.WriteString("\t\t\t\t}\n")
			b.WriteString("\t\t\t}\n")
			b.WriteString("\t\t} else { pr.skip(w) }\n")
			b.WriteString("\t}\n")
			b.WriteString("\treturn items, nil\n")
		} else {
			elemType := strings.TrimPrefix(result.goType, "[]")
			fmt.Fprintf(b, "\tvar items %s\n", result.goType)
			b.WriteString("\tpr := &pbReader{data: resp}\n")
			b.WriteString("\tfor f, w, ok := pr.next(); ok; f, w, ok = pr.next() {\n")
			fmt.Fprintf(b, "\t\tif f == %d {\n", result.fieldNum)
			b.WriteString("\t\t\tsub := pr.readSubmessage()\n")
			b.WriteString("\t\t\tp := readHandlePtr(sub.data)\n")
			fmt.Fprintf(b, "\t\t\tif p != 0 { child := new%sNoFinalizer(p); child.setKeepAlive(h); items = append(items, child) }\n", strings.TrimPrefix(elemType, "*"))
			b.WriteString("\t\t} else { pr.skip(w) }\n")
			b.WriteString("\t}\n")
			b.WriteString("\treturn items, nil\n")
		}
		return
	}
	if result.isRepeated && !result.isHandle {
		elemType := strings.TrimPrefix(result.goType, "[]")
		fmt.Fprintf(b, "\tvar items %s\n", result.goType)
		b.WriteString("\tpr := &pbReader{data: resp}\n")
		writeRepeatedScalarLoop(b, result, elemType)
		b.WriteString("\treturn items, nil\n")
		return
	}
	if result.kind == protoreflect.EnumKind {
		fmt.Fprintf(b, "\treturn %s(readScalarAtField(resp, %d, (*pbReader).readInt32)), nil\n",
			result.goType, result.fieldNum)
	} else {
		fmt.Fprintf(b, "\treturn readScalarAtField(resp, %d, (*pbReader).%s), nil\n",
			result.fieldNum, readMethodName(result))
	}
}

func writeDecodeForModule(b *strings.Builder, result fieldInfo, params []fieldInfo) {
	// emitKeepAliveFromParams pins handle-typed arguments on the freshly
	// constructed handle `child` so the arguments outlive the module-level
	// call. C++ parent objects returned by these free functions frequently
	// retain raw pointers to their inputs (e.g. ParseNextScriptStatement's
	// ParserOutput retains the ParseResumeLocation and ParserOptions);
	// without this the Go finalizers for the arguments run after the call
	// returns and the wasm-side parent is left pointing at freed memory.
	emitKeepAliveFromParams := func(childVar string) {
		for _, p := range params {
			if !p.isHandle {
				continue
			}
			if p.isRepeated {
				fmt.Fprintf(b, "\tfor _, _ka := range %s { %s.setKeepAlive(_ka) }\n", p.goName, childVar)
			} else {
				fmt.Fprintf(b, "\t%s.setKeepAlive(%s)\n", childVar, p.goName)
			}
		}
	}
	if result.isHandle && !result.isRepeated {
		fmt.Fprintf(b, "\tptr := readPtrAtField(resp, %d)\n", result.fieldNum)
		b.WriteString("\tif ptr == 0 { return nil, nil }\n")
		if result.isAbstract {
			b.WriteString("\tresolved, err := resolveAbstractHandle(ptr)\n")
			b.WriteString("\tif err != nil { return nil, err }\n")
			fmt.Fprintf(b, "\tif v, ok := resolved.(%s); ok {\n", result.goType)
			b.WriteString("\t\tif ka, okKA := v.(interface{ setKeepAlive(any) }); okKA {\n")
			for _, p := range params {
				if !p.isHandle {
					continue
				}
				if p.isRepeated {
					fmt.Fprintf(b, "\t\t\tfor _, _ka := range %s { ka.setKeepAlive(_ka) }\n", p.goName)
				} else {
					fmt.Fprintf(b, "\t\t\tka.setKeepAlive(%s)\n", p.goName)
				}
			}
			b.WriteString("\t\t}\n")
			b.WriteString("\t\treturn v, nil\n")
			b.WriteString("\t}\n")
			fmt.Fprintf(b, "\treturn nil, fmt.Errorf(\"resolved type %%T does not implement %s\", resolved)\n", result.goType)
		} else {
			fmt.Fprintf(b, "\tchild := new%s(ptr)\n", result.handleName)
			emitKeepAliveFromParams("child")
			b.WriteString("\treturn child, nil\n")
		}
		return
	}
	if result.isValueMsg && !result.isRepeated {
		b.WriteString("\tpr := &pbReader{data: resp}\n")
		b.WriteString("\tfor f, w, ok := pr.next(); ok; f, w, ok = pr.next() {\n")
		fmt.Fprintf(b, "\t\tif f == %d {\n", result.fieldNum)
		b.WriteString("\t\t\tsub := pr.readSubmessage()\n")
		fmt.Fprintf(b, "\t\t\treturn unmarshal%s(sub.data), nil\n", result.valueTypeName)
		b.WriteString("\t\t} else { pr.skip(w) }\n")
		b.WriteString("\t}\n")
		b.WriteString("\treturn nil, nil\n")
		return
	}
	if result.isRepeated {
		writeDecodeRepeatedForModule(b, result, params)
		return
	}
	if result.kind == protoreflect.EnumKind {
		fmt.Fprintf(b, "\treturn %s(readScalarAtField(resp, %d, (*pbReader).readInt32)), nil\n",
			result.goType, result.fieldNum)
	} else {
		fmt.Fprintf(b, "\treturn readScalarAtField(resp, %d, (*pbReader).%s), nil\n",
			result.fieldNum, readMethodName(result))
	}
}

func writeDecodeRepeatedForModule(b *strings.Builder, result fieldInfo, params []fieldInfo) {
	fmt.Fprintf(b, "\tvar items %s\n", result.goType)
	b.WriteString("\tpr := &pbReader{data: resp}\n")
	if result.isValueMsg {
		b.WriteString("\tfor f, w, ok := pr.next(); ok; f, w, ok = pr.next() {\n")
		fmt.Fprintf(b, "\t\tif f == %d {\n", result.fieldNum)
		b.WriteString("\t\t\tsub := pr.readSubmessage()\n")
		fmt.Fprintf(b, "\t\t\titems = append(items, unmarshal%s(sub.data))\n", result.valueTypeName)
		b.WriteString("\t\t} else { pr.skip(w) }\n")
		b.WriteString("\t}\n")
		b.WriteString("\treturn items, nil\n")
		return
	}
	b.WriteString("\tfor f, w, ok := pr.next(); ok; f, w, ok = pr.next() {\n")
	if result.isHandle {
		elemType := strings.TrimPrefix(strings.TrimPrefix(result.goType, "[]"), "*")
		fmt.Fprintf(b, "\t\tif f == %d {\n", result.fieldNum)
		b.WriteString("\t\t\tsub := pr.readSubmessage()\n")
		b.WriteString("\t\t\tp := readHandlePtr(sub.data)\n")
		fmt.Fprintf(b, "\t\t\tif p != 0 { _child := new%s(p);", elemType)
		for _, p := range params {
			if !p.isHandle {
				continue
			}
			if p.isRepeated {
				fmt.Fprintf(b, " for _, _ka := range %s { _child.setKeepAlive(_ka) };", p.goName)
			} else {
				fmt.Fprintf(b, " _child.setKeepAlive(%s);", p.goName)
			}
		}
		b.WriteString(" items = append(items, _child) }\n")
		b.WriteString("\t\t} else { pr.skip(w) }\n")
	} else {
		writeRepeatedScalarLoop(b, result, strings.TrimPrefix(result.goType, "[]"))
		b.WriteString("\treturn items, nil\n")
		return
	}
	b.WriteString("\t}\n")
	b.WriteString("\treturn items, nil\n")
}

// writeRepeatedScalarLoop emits a decoder for a repeated scalar field that
// tolerates both the packed (wire type 2) and unpacked (native) encodings —
// protoc-gen-go packs numeric repeateds by default, but some bridges emit the
// unpacked form and the Go reader has to handle either shape. For bytes and
// string the native wire type is already 2, so the submessage branch is
// redundant but harmless.
func writeRepeatedScalarLoop(b *strings.Builder, result fieldInfo, elemType string) {
	read := readMethodForElemType(elemType, result.kind)
	// Numeric kinds with wire types 0/1/5 can also arrive packed — the peer
	// encodes the whole vector into a length-delimited submessage. Detect w==2
	// and decode the submessage element-by-element in that case.
	packedEligible := false
	switch result.kind {
	case protoreflect.BoolKind,
		protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind,
		protoreflect.FloatKind, protoreflect.DoubleKind,
		protoreflect.EnumKind:
		packedEligible = true
	}
	b.WriteString("\tfor f, w, ok := pr.next(); ok; f, w, ok = pr.next() {\n")
	fmt.Fprintf(b, "\t\tif f != %d { pr.skip(w); continue }\n", result.fieldNum)
	if packedEligible {
		b.WriteString("\t\tif w == 2 {\n")
		b.WriteString("\t\t\tsub := pr.readSubmessage()\n")
		b.WriteString("\t\t\tfor sub.hasData() {\n")
		if result.kind == protoreflect.EnumKind {
			fmt.Fprintf(b, "\t\t\t\titems = append(items, %s(sub.%s()))\n", elemType, read)
		} else {
			fmt.Fprintf(b, "\t\t\t\titems = append(items, sub.%s())\n", read)
		}
		b.WriteString("\t\t\t}\n")
		b.WriteString("\t\t\tcontinue\n")
		b.WriteString("\t\t}\n")
	}
	if result.kind == protoreflect.EnumKind {
		fmt.Fprintf(b, "\t\titems = append(items, %s(pr.%s()))\n", elemType, read)
	} else {
		fmt.Fprintf(b, "\t\titems = append(items, pr.%s())\n", read)
	}
	b.WriteString("\t}\n")
}

func readMethodName(f fieldInfo) string {
	switch f.kind {
	case protoreflect.StringKind:
		return "readString"
	case protoreflect.BoolKind:
		return "readBool"
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind, protoreflect.EnumKind:
		return "readInt32"
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return "readInt64"
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return "readUint32"
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return "readUint64"
	case protoreflect.FloatKind:
		return "readFloat"
	case protoreflect.DoubleKind:
		return "readDouble"
	case protoreflect.BytesKind:
		return "readBytes"
	default:
		return "readBytes"
	}
}

func readMethodForElemType(_ string, kind protoreflect.Kind) string {
	switch kind {
	case protoreflect.StringKind:
		return "readString"
	case protoreflect.BoolKind:
		return "readBool"
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind, protoreflect.EnumKind:
		return "readInt32"
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return "readInt64"
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return "readUint32"
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return "readUint64"
	case protoreflect.FloatKind:
		return "readFloat"
	case protoreflect.DoubleKind:
		return "readDouble"
	default:
		return "readBytes"
	}
}

func zeroValue(f fieldInfo) string {
	if f.isHandle || f.isRepeated {
		return "nil"
	}
	if strings.HasPrefix(f.goType, "[]") || strings.HasPrefix(f.goType, "*") {
		return "nil"
	}
	switch f.kind {
	case protoreflect.StringKind:
		return `""`
	case protoreflect.BoolKind:
		return "false"
	case protoreflect.BytesKind:
		return "nil"
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return "0"
	default:
		return "0"
	}
}

// -----------------------------------------------------------------------------
// Name conversion helpers (direct port of gengo.go)
// -----------------------------------------------------------------------------

var goReservedWords = map[string]bool{
	"break": true, "case": true, "chan": true, "const": true, "continue": true,
	"default": true, "defer": true, "else": true, "fallthrough": true, "for": true,
	"func": true, "go": true, "goto": true, "if": true, "import": true,
	"interface": true, "map": true, "package": true, "range": true, "return": true,
	"select": true, "struct": true, "switch": true, "type": true, "var": true,
}

// upperFirst capitalizes the first letter of s (Unicode-safe). Used to turn
// a lower-camel parameter name into an exported struct-field identifier.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func snakeToCamel(s string) string {
	parts := strings.Split(s, "_")
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	result := strings.Join(parts, "")
	if goReservedWords[result] {
		result += "_"
	}
	return result
}

func protoToGoType(name string) string {
	if name == "" {
		return ""
	}
	parts := strings.Split(name, "_")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "_")
}

// nodeIfaceName returns the Go interface (or `<X>Node = *X` alias)
// name for a handle of class `class`. Idempotent over the trailing
// `Node` suffix: when the class name already ends in `Node`, the
// suffix is dropped so `ASTNode` resolves to `ASTNode` rather than
// `ASTNodeNode`. The naming convention everywhere else in the
// generator is "<class>Node"; this helper centralises the fold so a
// future refinement (e.g. excluding non-base classes) only needs to
// change one place.
func nodeIfaceName(class string) string {
	if strings.HasSuffix(class, "Node") {
		return class
	}
	return class + "Node"
}

// handleStructName returns the Go struct name for an emitted handle.
// For most classes this is the bare class name; for abstract classes
// whose name already ends in "Node" the struct is renamed to
// "<Class>Base" so the companion interface (= nodeIfaceName, which
// folds the "Node" suffix) can keep the natural class spelling
// without colliding with the struct.
func handleStructName(class string, isAbstract bool) string {
	if isAbstract && strings.HasSuffix(class, "Node") {
		return class + "Base"
	}
	return class
}

func toScreamingSnakeSimple(s string) string {
	// Mirror the main proto-side toSnakeCase rules so a Go type name
	// like `ResolvedJoinScanEnums_JoinType` screams to
	// RESOLVED_JOIN_SCAN_ENUMS_JOIN_TYPE (single underscore at each
	// word boundary — never double up against an existing `_` or
	// split an all-caps acronym into individual letters).
	runes := []rune(s)
	var result strings.Builder
	for i, r := range runes {
		isUpper := r >= 'A' && r <= 'Z'
		if isUpper && i > 0 {
			prev := runes[i-1]
			// Skip the underscore if the previous rune already IS an
			// underscore, so `Foo_Bar` stays FOO_BAR (not FOO__BAR).
			if prev != '_' {
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
		}
		result.WriteRune(r)
	}
	return strings.ToUpper(result.String())
}

func screamingSnakeToUpperCamel(s string) string {
	parts := strings.Split(strings.ToLower(s), "_")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

// -----------------------------------------------------------------------------
// Option accessors (preserved from the previous implementation)
// -----------------------------------------------------------------------------

func isHandleMessage(msg *protogen.Message) bool {
	opts := msg.Desc.Options().(*descriptorpb.MessageOptions)
	if opts == nil {
		return false
	}
	b, _ := proto.GetExtension(opts, wasmifyopts.E_WasmHandle).(bool)
	return b
}

func isAbstractMessage(msg *protogen.Message) bool {
	opts := msg.Desc.Options().(*descriptorpb.MessageOptions)
	if opts == nil {
		return false
	}
	b, _ := proto.GetExtension(opts, wasmifyopts.E_WasmAbstract).(bool)
	return b
}

func getWasmParent(msg *protogen.Message) string {
	opts := msg.Desc.Options().(*descriptorpb.MessageOptions)
	if opts == nil {
		return ""
	}
	s, _ := proto.GetExtension(opts, wasmifyopts.E_WasmParent).(string)
	return s
}

// getWasmParents returns the secondary (C++ multiple-inheritance)
// parents emitted as repeated wasm_parents options. Empty if the
// class has at most one parent.
func getWasmParents(msg *protogen.Message) []string {
	opts := msg.Desc.Options().(*descriptorpb.MessageOptions)
	if opts == nil {
		return nil
	}
	out, _ := proto.GetExtension(opts, wasmifyopts.E_WasmParents).([]string)
	return out
}

// methodRetainsHandleArgs reports whether a C++ method name suggests the
// receiver will store a reference to any handle-typed argument. Setter
// prefixes that commonly do so (Add, Set, Register, Insert, Attach, Push)
// trigger the keepAlive emission; anything else is assumed to consume the
// argument by value. Keeping this conservative avoids piling every
// transient handle onto long-lived parents like analyzer options, which
// otherwise defeats finalizer-driven release of wasm-side memory.
func methodRetainsHandleArgs(name string) bool {
	for _, prefix := range []string{"Add", "Set", "Register", "Insert", "Attach", "Push"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// getWasmCppNamespace returns the real C++ namespace encoded in the
// proto's file options (e.g. "googlesql"). This is what
// resolveAbstractHandle needs to match the runtime typeid string
// against the factory table — the proto package name alone is not
// sufficient because it is arbitrary (often "wasmify.api").
func getWasmCppNamespace(file *protogen.File) string {
	opts := file.Desc.Options().(*descriptorpb.FileOptions)
	if opts == nil {
		return ""
	}
	s, _ := proto.GetExtension(opts, wasmifyopts.E_WasmCppNamespace).(string)
	return s
}

// getMessageComment lifts the doc comment that the generator
// attached to a handle/value message via wasm_message_comment.
func getMessageComment(msg *protogen.Message) string {
	opts := msg.Desc.Options().(*descriptorpb.MessageOptions)
	if opts == nil {
		return ""
	}
	s, _ := proto.GetExtension(opts, wasmifyopts.E_WasmMessageComment).(string)
	return s
}

// getMethodComment lifts the doc comment for an RPC.
func getMethodComment(m *protogen.Method) string {
	opts := m.Desc.Options().(*descriptorpb.MethodOptions)
	if opts == nil {
		return ""
	}
	s, _ := proto.GetExtension(opts, wasmifyopts.E_WasmMethodComment).(string)
	return s
}

// getOriginalName returns the C++ name preserved on an RPC when the
// proto generator renamed it to avoid a service-scoped collision.
// Empty when no rename happened.
func getOriginalName(m *protogen.Method) string {
	opts := m.Desc.Options().(*descriptorpb.MethodOptions)
	if opts == nil {
		return ""
	}
	s, _ := proto.GetExtension(opts, wasmifyopts.E_WasmOriginalName).(string)
	return s
}

// getEnumComment lifts the doc comment attached to an enum.
func getEnumComment(e *protogen.Enum) string {
	opts := e.Desc.Options().(*descriptorpb.EnumOptions)
	if opts == nil {
		return ""
	}
	s, _ := proto.GetExtension(opts, wasmifyopts.E_WasmEnumComment).(string)
	return s
}

// getEnumValueComment lifts the doc comment attached to an enum
// value.
func getEnumValueComment(v *protogen.EnumValue) string {
	opts := v.Desc.Options().(*descriptorpb.EnumValueOptions)
	if opts == nil {
		return ""
	}
	s, _ := proto.GetExtension(opts, wasmifyopts.E_WasmEnumValueComment).(string)
	return s
}

// resolveGoMethodNames walks methods and assigns each entry's goName.
// For methods whose proto-side rpcName was rewritten to dodge a
// service-scoped lookup collision (proto generator stamped the
// original C++ name into wasm_original_name), it tries to recover
// the natural upper-camel form and uses that on the Go side. Two
// situations force a fallback to the proto-renamed name:
//
//   - The recovered name matches the receiver's embedded parent
//     struct field. Go forbids declaring a method with the same
//     name as an embedded field on the same struct.
//   - The recovered name has already been claimed by another
//     method that emits onto the same Go receiver. Constructors,
//     static factories, and callback factories all become
//     top-level functions (`func New<X>(...)`), so they do NOT
//     occupy the receiver's name space and are excluded from the
//     conflict set.
//
// Free-function services pass an empty handleName: there is no
// receiver to clash with, so the recovery is unconditional.
func resolveGoMethodNames(methods []svcMethodInfo, handleName string, messages map[string]msgInfo) {
	// emitsAsReceiverMethod reports whether a method type produces
	// a `func (h *X) Foo()` declaration whose name competes for the
	// receiver's identifier slot. Top-level functions
	// (constructors, factories) live in the package namespace and
	// don't collide with receiver methods.
	emitsAsReceiverMethod := func(t string) bool {
		switch t {
		case "constructor", "static_factory", "callback_factory":
			return false
		}
		return true
	}
	taken := make(map[string]bool, len(methods))
	// Seed with the (already-unique) proto rpcNames of methods that
	// won't change. Two methods that both want the same recovered
	// name shouldn't both get it.
	for i := range methods {
		if !emitsAsReceiverMethod(methods[i].methodType) {
			continue
		}
		if methods[i].originalName == "" {
			taken[methods[i].name] = true
		}
	}
	parentNames := map[string]bool{}
	if handleName != "" {
		info := messages[handleName]
		if info.parent != "" {
			parentNames[info.parent] = true
		}
		for _, p := range info.parents {
			if p != "" {
				parentNames[p] = true
			}
		}
	}
	for i := range methods {
		m := &methods[i]
		if m.originalName == "" {
			m.goName = m.name
			continue
		}
		desired := upperFirst(snakeToCamel(m.originalName))
		if desired == "" || desired == m.name {
			m.goName = m.name
			continue
		}
		// Receiver-scoped collision check applies only to methods
		// that share the receiver's identifier slot.
		if emitsAsReceiverMethod(m.methodType) {
			if parentNames[desired] || taken[desired] {
				m.goName = m.name
				taken[m.name] = true
				continue
			}
			taken[desired] = true
		}
		m.goName = desired
	}
}

// formatGoDocComment renders s as a sequence of Go doc-comment lines
// (each prefixed with `// `). Returns the empty string when s is empty
// so callers can unconditionally write the result without producing a
// leading blank line.
func formatGoDocComment(s string) string {
	if s = strings.TrimSpace(s); s == "" {
		return ""
	}
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			b.WriteString("//\n")
			continue
		}
		// Trim a single leading space if present (clang's TextComment
		// text values usually open with one space carried over from
		// `// foo`).
		if line[0] == ' ' {
			line = line[1:]
		}
		b.WriteString("// ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func getWasmServiceId(svc *protogen.Service) int {
	opts := svc.Desc.Options().(*descriptorpb.ServiceOptions)
	if opts == nil {
		return -1
	}
	n, _ := proto.GetExtension(opts, wasmifyopts.E_WasmServiceId).(int32)
	return int(n)
}

func getWasmMethodId(method *protogen.Method) int {
	opts := method.Desc.Options().(*descriptorpb.MethodOptions)
	if opts == nil {
		return -1
	}
	n, _ := proto.GetExtension(opts, wasmifyopts.E_WasmMethodId).(int32)
	return int(n)
}

func getWasmMethodType(method *protogen.Method) string {
	opts := method.Desc.Options().(*descriptorpb.MethodOptions)
	if opts == nil {
		return ""
	}
	s, _ := proto.GetExtension(opts, wasmifyopts.E_WasmMethodType).(string)
	return s
}

func isCallbackService(svc *protogen.Service) bool {
	opts := svc.Desc.Options().(*descriptorpb.ServiceOptions)
	if opts == nil {
		return false
	}
	b, _ := proto.GetExtension(opts, wasmifyopts.E_WasmCallback).(bool)
	return b
}

