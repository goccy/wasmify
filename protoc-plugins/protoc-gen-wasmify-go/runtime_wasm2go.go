package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"sort"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
)

// wasm2goImportPath is the Go import path of the wasm2go-transpiled
// package emitted alongside the bridge. It is set by generateFile
// before any of the wasm2go codegen helpers run, and is substituted
// into the templates' __WASM2GO_IMPORT__ placeholder.
var wasm2goImportPath string

// unifiedImportsWasm2go is the import block of the consolidated bridge
// file in runtime=wasm2go mode. It drops the wazero imports of the
// default block and pulls in the transpiled wasm2go package instead.
const unifiedImportsWasm2go = `import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"__WASM2GO_IMPORT__"
	"__WASM2GO_IMPORT__/base"
)
`

// unifiedImportAnchorsWasm2go references every package in
// unifiedImportsWasm2go so a trivial proto still compiles.
const unifiedImportAnchorsWasm2go = `var (
	_ = binary.LittleEndian
	_ = errors.New
	_ = fmt.Errorf
	_ = math.Float32bits
	_ = os.Stderr
	_ = runtime.SetFinalizer
	_ = sort.Search
	_ = strconv.Itoa
	_ = strings.TrimSpace
	_ = sync.Once{}
	_ = wasm2go.Memory
	_ *base.Module
)
`

// moduleBodyWasm2go is the runtime adapter for runtime=wasm2go. It is
// the wasm2go-backed counterpart of moduleBody: the wasm calls go
// through the AOT-compiled wasm2go package instead of wazero.
//
// The __WASM2GO_IMPORT__ placeholder is replaced with the wasm2go
// package import path by generateModule. envStubs' interface methods
// are emitted separately by generateEnvStubs (they are derived from
// the wasm2go base.EnvImports interface, so they cannot be a const).
const moduleBodyWasm2go = `import (
	"fmt"
	"sync"

	"__WASM2GO_IMPORT__"
	"__WASM2GO_IMPORT__/base"
)

// Module is the bridge handle. It wraps the wasm2go-transpiled module
// (*base.Module) and serialises every entry on m.mu.
//
// Reentrant callbacks — a host-import callback whose handler needs
// to make further calls back into the transpiled module — are
// supported by releasing m.mu around the user handler in
// handleCallback.
//
// Safety rests on a single structural property: every path that
// touches transpiled module state goes through m.invoke, and
// m.invoke holds m.mu for the entire duration of its call. The
// release window in handleCallback simply lets another m.invoke
// grab the mutex and run as a fully nested top-level call — same
// goroutine via the user handler, different goroutine via an
// unrelated caller, it doesn't matter. Either way the nested call
// enters, balances its own state changes, and exits before the
// outer call resumes, so the outer call never observes a mid-flight
// inner call.
type Module struct {
	// mu serialises every entry into the transpiled module (invoke +
	// nested re-entry from inside a callback handler).
	mu sync.Mutex
	g  *base.Module
	// cbMu guards callbacks and nextCBID. RWMutex because lookup in
	// handleCallback is hot (every host-import dispatch) while
	// Register/Unregister are rare (typically once per lifetime of a
	// registered Go-side handler). Held only for the brief map op;
	// never held while running user code or the transpiled module,
	// so it never participates in the m.mu -> transpiled-module ->
	// handleCallback chain and cannot deadlock against it.
	cbMu      sync.RWMutex
	callbacks map[int32]CallbackHandler
	nextCBID  int32
}

var (
	globalModule *Module
	initOnce     sync.Once
	initErr      error
)

// CallbackHandler is implemented by Go types that need to be called
// from C++. The C++ bridge calls callback_invoke(callbackID, methodID,
// reqPtr, reqLen), which dispatches to the registered handler.
type CallbackHandler interface {
	HandleCallback(methodID int32, req []byte) ([]byte, error)
}

// RegisterCallback registers a Go callback handler and returns its ID.
// Safe to call concurrently with other Register/Unregister/callback
// dispatch calls.
func RegisterCallback(handler CallbackHandler) int32 {
	m := module()
	m.cbMu.Lock()
	defer m.cbMu.Unlock()
	if m.callbacks == nil {
		m.callbacks = make(map[int32]CallbackHandler)
	}
	m.nextCBID++
	id := m.nextCBID
	m.callbacks[id] = handler
	return id
}

// UnregisterCallback removes a previously registered callback handler.
// Safe to call concurrently with other Register/Unregister/callback
// dispatch calls.
func UnregisterCallback(id int32) {
	m := module()
	m.cbMu.Lock()
	delete(m.callbacks, id)
	m.cbMu.Unlock()
}

func (m *Module) handleCallback(callbackID, methodID, reqPtr, reqLen int32) int64 {
	m.cbMu.RLock()
	handler, ok := m.callbacks[callbackID]
	m.cbMu.RUnlock()
	if !ok {
		return 0
	}
	mem := wasm2go.Memory(m.g)
	var req []byte
	if reqLen > 0 {
		buf := make([]byte, reqLen)
		copy(buf, mem[reqPtr:reqPtr+reqLen])
		req = buf
	}
	// Release m.mu around the user handler so that nested calls
	// back into the transpiled module can re-acquire it without
	// deadlocking. The IIFE + defer guarantees Lock is retaken even
	// if the handler panics, so the outer invoke's defer
	// m.mu.Unlock() always sees a held mutex.
	var resp []byte
	var err error
	func() {
		m.mu.Unlock()
		defer m.mu.Lock()
		resp, err = handler.HandleCallback(methodID, req)
	}()
	if err != nil {
		resp = pbAppendString(nil, 15, err.Error())
	}
	if len(resp) == 0 {
		return 0
	}
	ptr := wasm2go.WasmAlloc(m.g, int32(len(resp)))
	copy(wasm2go.Memory(m.g)[ptr:], resp)
	return int64(ptr)<<32 | int64(len(resp))
}

// Init initializes the global module. Must be called before any API
// use. Safe to call multiple times (uses sync.Once).
func Init() error {
	initOnce.Do(func() {
		initErr = initModule()
	})
	return initErr
}

// module returns the initialized global module. Panics if Init was not
// called.
func module() *Module {
	if globalModule == nil {
		panic("wasmify: Init() must be called before using any API")
	}
	return globalModule
}

func initModule() (retErr error) {
	m := &Module{}
	env := envStubs{m: m}
	wm := wasmifyStubs{m: m}
	m.g = wasm2go.New(env, wm)
	// Set globalModule eagerly so the rest of the API can run even if
	// _initialize panics partway through C++ static-initializer code.
	globalModule = m
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("init panicked: %v", r)
		}
	}()
	wasm2go.Initialize(m.g)
	_ = wasm2go.WasmInit(m.g)
	return nil
}

// invoke serializes req into wasm memory, runs the per-export caller
// (wasm2go.Inv_<svc>_<mt>), and unpacks the (ptr<<32 | len) response.
// call is the trap-safe per-export entry point: it snapshots and
// restores the mutable wasm globals so a mid-call panic does not leak
// an abandoned C++ activation frame.
func (m *Module) invoke(serviceID, methodID int32, req []byte, call func(*base.Module, int32, int32) (int64, error)) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var reqPtr, reqLen int32
	if len(req) > 0 {
		reqPtr = wasm2go.WasmAlloc(m.g, int32(len(req)))
		reqLen = int32(len(req))
		copy(wasm2go.Memory(m.g)[reqPtr:], req)
	}
	packed, err := call(m.g, reqPtr, reqLen)
	if err != nil {
		return nil, err
	}
	respPtr := uint32(packed >> 32)
	respLen := uint32(packed & 0xFFFFFFFF)
	if respLen == 0 {
		if reqPtr != 0 {
			wasm2go.WasmFree(m.g, reqPtr)
		}
		return nil, nil
	}
	mem := wasm2go.Memory(m.g)
	out := make([]byte, respLen)
	copy(out, mem[respPtr:respPtr+respLen])
	wasm2go.WasmFree(m.g, int32(respPtr))
	if reqPtr != 0 {
		wasm2go.WasmFree(m.g, reqPtr)
	}
	return out, nil
}

// resolveTypeName calls the C++ bridge to get the runtime type name of
// the object at ptr. Returns a fully qualified C++ class name.
func (m *Module) resolveTypeName(ptr uint64) (string, error) {
	buf := pbAppendUint64(nil, 1, ptr)
	m.mu.Lock()
	defer m.mu.Unlock()
	reqPtr := wasm2go.WasmAlloc(m.g, int32(len(buf)))
	copy(wasm2go.Memory(m.g)[reqPtr:], buf)
	packed := wasm2go.WasmifyGetTypeName(m.g, reqPtr, int32(len(buf)))
	respPtr := uint32(packed >> 32)
	respLen := uint32(packed & 0xFFFFFFFF)
	defer wasm2go.WasmFree(m.g, reqPtr)
	if respLen == 0 {
		return "", nil
	}
	resp := make([]byte, respLen)
	copy(resp, wasm2go.Memory(m.g)[respPtr:respPtr+respLen])
	defer wasm2go.WasmFree(m.g, int32(respPtr))
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

// invokeMethod fans the wasm call out into the runtime, then folds in
// the (very common) pbExtractError check on the response. call is the
// per-export wasm2go.Inv_<svc>_<mt> entry point.
func invokeMethod(svc, mid int32, req []byte, call func(*base.Module, int32, int32) (int64, error)) ([]byte, error) {
	resp, err := module().invoke(svc, mid, req, call)
	if err != nil {
		return nil, err
	}
	if e := pbExtractError(resp); e != nil {
		return nil, e
	}
	return resp, nil
}

// envStubs implements wasm2go/base.EnvImports — the host side of the
// wasm "env" imports. The per-method definitions are emitted by the
// generator (generateEnvStubs) since the import set is module-specific.
type envStubs struct{ m *Module }

// wasmifyStubs implements wasm2go/base.WasmifyImports: the single
// callback_invoke entry point that the C++ bridge calls back into.
type wasmifyStubs struct{ m *Module }

func (h wasmifyStubs) Callback_invoke(_ *base.Module, callbackID, methodID, reqPtr, reqLen int32) int64 {
	return h.m.handleCallback(callbackID, methodID, reqPtr, reqLen)
}
`

// emitWasm2go transpiles cfg.wasm into the wasm2go-generated Go package
// and emits every file under internal/wasm2go/ next to the bridge. It
// returns the Go source that declares the EnvImports interface, so the
// caller can derive the env stub method set from it.
//
// wasm2go auto-selects single-package or multi-package layout based on
// the wasm function-body byte size. The bridge template needs a uniform
// surface (free functions wasm2go.Foo(m) + *base.Module), so when the
// single-package layout is in effect we synthesise a compat shim and a
// base/base.go alias file alongside the main output.
func emitWasm2go(plugin *protogen.Plugin, importPath protogen.GoImportPath) ([]byte, error) {
	wasmBin, err := os.ReadFile(cfg.wasm)
	if err != nil {
		return nil, fmt.Errorf("read wasm %q: %w", cfg.wasm, err)
	}
	files, singlePkg, err := transpileGenwasm(wasmBin, "wasm2go", wasm2goImportPath)
	if err != nil {
		return nil, err
	}
	var envImportsSrc []byte
	if singlePkg {
		envImportsSrc = files["wasm2go.go"]
		files["wasm2go_compat.go"] = []byte(wasm2goSinglePkgCompatGo)
		files["base/base.go"] = []byte(fmt.Sprintf(wasm2goSinglePkgBaseAliasGoFmt, wasm2goImportPath))
	} else {
		envImportsSrc = files["base/base.go"]
	}
	if envImportsSrc == nil {
		return nil, fmt.Errorf("transpileGenwasm: cannot locate EnvImports source")
	}
	gwImport := protogen.GoImportPath(wasm2goImportPath)
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	// NewGeneratedFile takes an import-path-rooted name: buf's module=
	// option strips the module prefix to derive the on-disk path.
	for _, rel := range rels {
		name := string(importPath) + "/internal/wasm2go/" + rel
		g := plugin.NewGeneratedFile(name, gwImport)
		if _, err := g.Write(files[rel]); err != nil {
			return nil, fmt.Errorf("write wasm2go/%s: %w", rel, err)
		}
	}
	return envImportsSrc, nil
}

// wasm2goSinglePkgCompatGo is the file emitted alongside wasm2go's
// single-package output. The runtime API surface wasm2go exposes in
// single-package mode is *Module methods; the bridge needs free
// functions wasm2go.Foo(m) — these shims provide them by routing to the
// underlying methods.
const wasm2goSinglePkgCompatGo = `package wasm2go

func Memory(m *Module) []byte                                   { return m.Memory() }
func WasmAlloc(m *Module, n int32) int32                        { return m.WasmAlloc(n) }
func WasmFree(m *Module, ptr int32)                             { m.WasmFree(ptr) }
func WasmInit(m *Module) int32                                  { return m.WasmInit() }
func WasmifyGetTypeName(m *Module, ptr, length int32) int64     { return m.WasmifyGetTypeName(ptr, length) }
func Initialize(m *Module)                                      { m.Initialize() }
`

// wasm2goSinglePkgBaseAliasGoFmt is the file emitted at <pkg>/base/base.go
// in single-package mode. It re-exports wasm2go's Module/EnvImports/
// WasmifyImports as aliases so the bridge can refer to base.Module
// uniformly. The %q is replaced with the wasm2go package import path.
const wasm2goSinglePkgBaseAliasGoFmt = `package base

import wasm2go %q

type Module = wasm2go.Module
type EnvImports = wasm2go.EnvImports
type WasmifyImports = wasm2go.WasmifyImports
`

// invokeArgs returns the trailing argument that a generated
// invokeMethod / module().invoke call site needs. In wazero mode the
// 3-arg form is used (no trailing arg); in wasm2go mode each call site
// passes its per-export caller wasm2go.Inv_<svc>_<mt>.
func invokeArgs(svc, mt int32) string {
	if cfg.wasm2goRuntime() {
		return fmt.Sprintf(", wasm2go.Inv_%d_%d", svc, mt)
	}
	return ""
}

// generateEnvStubs emits the envStubs interface-method definitions for
// runtime=wasm2go. The method set is read from the wasm2go base.go's
// EnvImports interface so the generated stubs satisfy it exactly. Most
// imports are unresolved symbols stubbed to a zero return; the C++
// runtime entry points (__cxa_*) get real bodies.
func generateEnvStubs(baseGoSrc []byte) (string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "base.go", baseGoSrc, 0)
	if err != nil {
		return "", fmt.Errorf("parse wasm2go base.go: %w", err)
	}
	var iface *ast.InterfaceType
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "EnvImports" {
				continue
			}
			it, ok := ts.Type.(*ast.InterfaceType)
			if ok {
				iface = it
			}
		}
	}
	if iface == nil {
		return "", fmt.Errorf("wasm2go base.go: EnvImports interface not found")
	}

	var methods []*ast.Field
	for _, m := range iface.Methods.List {
		if len(m.Names) == 1 {
			if _, ok := m.Type.(*ast.FuncType); ok {
				methods = append(methods, m)
			}
		}
	}
	sort.SliceStable(methods, func(i, j int) bool {
		return methods[i].Names[0].Name < methods[j].Names[0].Name
	})

	var b strings.Builder
	b.WriteString("\n")
	for _, m := range methods {
		name := m.Names[0].Name
		ft := m.Type.(*ast.FuncType)
		params, results, paramNames := renderEnvSig(fset, ft)
		fmt.Fprintf(&b, "func (h envStubs) %s(%s)%s { %s }\n",
			name, params, results, envStubBody(name, results, paramNames))
	}
	return b.String(), nil
}

// renderEnvSig renders the parameter list, result, and ordered
// parameter names of an EnvImports method. The base.go interface
// declares parameters as *Module (package-local); the bridge needs
// the qualified *base.Module form.
func renderEnvSig(fset *token.FileSet, ft *ast.FuncType) (params, results string, paramNames []string) {
	render := func(n ast.Node) string {
		var sb strings.Builder
		_ = printer.Fprint(&sb, fset, n)
		return strings.ReplaceAll(sb.String(), "*Module", "*base.Module")
	}
	var ps []string
	if ft.Params != nil {
		for _, p := range ft.Params.List {
			typ := render(p.Type)
			for _, nm := range p.Names {
				ps = append(ps, nm.Name+" "+typ)
				paramNames = append(paramNames, nm.Name)
			}
		}
	}
	params = strings.Join(ps, ", ")
	if ft.Results != nil && len(ft.Results.List) == 1 {
		results = " " + render(ft.Results.List[0].Type)
	}
	return params, results, paramNames
}

// envStubBody returns the body of an env stub. __cxa_allocate_exception
// allocates the exception object from wasm memory; __cxa_throw aborts
// (wasm cannot unwind); thread semaphore waits succeed immediately;
// every other unresolved symbol returns its zero value.
func envStubBody(name, results string, paramNames []string) string {
	switch {
	case strings.Contains(name, "allocate_exception"):
		sizeArg := "64"
		if len(paramNames) >= 2 {
			sizeArg = paramNames[1]
		}
		modArg := "m"
		if len(paramNames) >= 1 {
			modArg = paramNames[0]
		}
		return fmt.Sprintf("if %s == 0 { %s = 64 }; return wasm2go.WasmAlloc(%s, %s)",
			sizeArg, sizeArg, modArg, sizeArg)
	case strings.Contains(name, "__cxa_throw") || strings.HasSuffix(name, "_throw"):
		return `panic("wasm: C++ exception thrown")`
	case strings.Contains(name, "SemWait") || strings.Contains(name, "sem_wait"):
		if results != "" {
			return "return 1"
		}
		return ""
	default:
		if results != "" {
			return "return 0"
		}
		return ""
	}
}
