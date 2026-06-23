package wasmbuild

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/wasmify/internal/buildjson"
)

func TestInjectBridgeSteps_NoBridge(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := WasmConfig{
		WasiSDKPath: "/opt/wasi-sdk",
		Target:      "wasm32-wasi",
		BuildDir:    tmpDir,
	}

	steps := []WasmBuildStep{
		{ID: 1, Type: buildjson.StepCompile, Args: []string{"-c", "test.cc"}},
		{ID: 2, Type: buildjson.StepLink, Args: []string{"-o", "out.wasm"}},
	}

	// No api_bridge.cc → steps unchanged
	result := InjectBridgeSteps(steps, cfg, filepath.Join(tmpDir, "src"), nil)
	if len(result) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(result))
	}
}

func TestInjectBridgeSteps_WithBridge(t *testing.T) {
	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "src")
	_ = os.MkdirAll(srcDir, 0o755)
	_ = os.WriteFile(filepath.Join(srcDir, "api_bridge.cc"), []byte("// bridge"), 0o644)

	cfg := WasmConfig{
		WasiSDKPath: "/opt/wasi-sdk",
		Target:      "wasm32-wasi",
		BuildDir:    tmpDir,
	}

	steps := []WasmBuildStep{
		{ID: 1, Type: buildjson.StepCompile, Args: []string{"-c", "-I/usr/include", "test.cc"}, OutputFile: filepath.Join(tmpDir, "obj", "test.o")},
		{ID: 2, Type: buildjson.StepLink, Args: []string{"-o", filepath.Join(tmpDir, "output", "out.wasm")}, OutputFile: filepath.Join(tmpDir, "output", "out.wasm")},
	}

	result := InjectBridgeSteps(steps, cfg, srcDir, nil)

	// Should have 3 steps: original compile + bridge compile + link
	if len(result) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(result))
	}

	// Find bridge compile step
	var bridgeStep *WasmBuildStep
	for i := range result {
		if result[i].OutputFile == filepath.Join(tmpDir, "obj", "api_bridge.o") {
			bridgeStep = &result[i]
			break
		}
	}
	if bridgeStep == nil {
		t.Fatal("bridge compile step not found")
	}
	if bridgeStep.Type != buildjson.StepCompile {
		t.Fatalf("expected compile step, got %s", bridgeStep.Type)
	}

	// Link step should include bridge object
	var linkStep *WasmBuildStep
	for i := range result {
		if result[i].Type == buildjson.StepLink {
			linkStep = &result[i]
			break
		}
	}
	if linkStep == nil {
		t.Fatal("link step not found")
	}
	found := false
	for _, arg := range linkStep.Args {
		if arg == filepath.Join(tmpDir, "obj", "api_bridge.o") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("bridge object not added to link step args")
	}
}

func TestInjectBridgeSteps_HostShims(t *testing.T) {
	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "src")
	_ = os.MkdirAll(srcDir, 0o755)
	_ = os.WriteFile(filepath.Join(srcDir, "api_bridge.cc"), []byte("// bridge"), 0o644)

	cfg := WasmConfig{
		WasiSDKPath: "/opt/wasi-sdk",
		Target:      "wasm32-wasi",
		BuildDir:    tmpDir,
	}

	baseSteps := func() []WasmBuildStep {
		return []WasmBuildStep{
			{ID: 1, Type: buildjson.StepCompile, Args: []string{"-c", "test.cc"}, OutputFile: filepath.Join(tmpDir, "obj", "test.o")},
			{ID: 2, Type: buildjson.StepLink, Args: []string{"-o", filepath.Join(tmpDir, "output", "out.wasm")}, OutputFile: filepath.Join(tmpDir, "output", "out.wasm")},
		}
	}

	socketsObj := filepath.Join(tmpDir, "obj", "host_sockets.o")
	subprocessObj := filepath.Join(tmpDir, "obj", "host_subprocess.o")

	linkHasObj := func(steps []WasmBuildStep, obj string) bool {
		for _, step := range steps {
			if step.Type != buildjson.StepLink {
				continue
			}
			for _, arg := range step.Args {
				if arg == obj {
					return true
				}
			}
		}
		return false
	}
	hasCompileFor := func(steps []WasmBuildStep, obj string) bool {
		for _, step := range steps {
			if step.Type == buildjson.StepCompile && step.OutputFile == obj {
				return true
			}
		}
		return false
	}

	// No shim sources → no shim compile/link.
	off := InjectBridgeSteps(baseSteps(), cfg, srcDir, nil)
	if hasCompileFor(off, socketsObj) || hasCompileFor(off, subprocessObj) {
		t.Fatalf("shim compile step present with no shim sources")
	}
	if linkHasObj(off, socketsObj) || linkHasObj(off, subprocessObj) {
		t.Fatalf("shim object linked with no shim sources")
	}

	// Both shim sources → both compiled and linked.
	shimSrcs := []string{
		filepath.Join(tmpDir, "host-shims", "host_sockets.cc"),
		filepath.Join(tmpDir, "host-shims", "host_subprocess.cc"),
	}
	on := InjectBridgeSteps(baseSteps(), cfg, srcDir, shimSrcs)
	if !hasCompileFor(on, socketsObj) {
		t.Fatalf("sockets shim compile step missing")
	}
	if !hasCompileFor(on, subprocessObj) {
		t.Fatalf("subprocess shim compile step missing")
	}
	if !linkHasObj(on, socketsObj) {
		t.Fatalf("sockets shim object not linked")
	}
	if !linkHasObj(on, subprocessObj) {
		t.Fatalf("subprocess shim object not linked")
	}
}

func TestInjectBridgeSteps_CustomBridgeSources(t *testing.T) {
	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "src")
	_ = os.MkdirAll(srcDir, 0o755)
	_ = os.WriteFile(filepath.Join(srcDir, "api_bridge.cc"), []byte("// bridge"), 0o644)

	projectRoot := filepath.Join(tmpDir, "project")
	embedDir := filepath.Join(projectRoot, "embed")
	_ = os.MkdirAll(embedDir, 0o755)
	customSrc := filepath.Join(embedDir, "py.c") // a .c name with C++ content
	_ = os.WriteFile(customSrc, []byte("// custom bridge"), 0o644)

	cfg := WasmConfig{
		WasiSDKPath:         "/opt/wasi-sdk",
		Target:              "wasm32-wasi",
		BuildDir:            tmpDir,
		ProjectRoot:         projectRoot,
		CustomBridgeSources: []string{customSrc},
	}

	steps := []WasmBuildStep{
		{ID: 1, Type: buildjson.StepCompile, Args: []string{"-c", "-I/usr/include", "test.cc"}, OutputFile: filepath.Join(tmpDir, "obj", "test.o")},
		{ID: 2, Type: buildjson.StepLink, Args: []string{"-o", filepath.Join(tmpDir, "output", "out.wasm")}, OutputFile: filepath.Join(tmpDir, "output", "out.wasm")},
	}

	result := InjectBridgeSteps(steps, cfg, srcDir, nil)

	customObj := filepath.Join(tmpDir, "obj", "py.o")

	// The api_bridge compile must carry the project root on its include path so
	// the generated dispatcher's project-relative includes resolve.
	var apiBridgeStep *WasmBuildStep
	for i := range result {
		if result[i].OutputFile == filepath.Join(tmpDir, "obj", "api_bridge.o") {
			apiBridgeStep = &result[i]
			break
		}
	}
	if apiBridgeStep == nil {
		t.Fatal("api_bridge compile step not found")
	}
	if !argsContainPair(apiBridgeStep.Args, "-I", projectRoot) {
		t.Fatalf("api_bridge compile missing -I %s: %v", projectRoot, apiBridgeStep.Args)
	}

	// The custom source gets its own compile step, forced to C++ (clang++ -x
	// c++), with -I of its own directory so sibling-relative includes resolve.
	var customStep *WasmBuildStep
	for i := range result {
		if result[i].OutputFile == customObj {
			customStep = &result[i]
			break
		}
	}
	if customStep == nil {
		t.Fatalf("custom bridge compile step (%s) not found", customObj)
	}
	if filepath.Base(customStep.Executable) != "clang++" {
		t.Fatalf("custom bridge must compile with clang++, got %s", customStep.Executable)
	}
	if !argsContainPair(customStep.Args, "-x", "c++") {
		t.Fatalf("custom bridge compile missing -x c++: %v", customStep.Args)
	}
	if !argsContainPair(customStep.Args, "-I", embedDir) {
		t.Fatalf("custom bridge compile missing -I %s: %v", embedDir, customStep.Args)
	}
	if len(customStep.InputFiles) != 1 || customStep.InputFiles[0] != customSrc {
		t.Fatalf("custom bridge compile InputFiles wrong: %v", customStep.InputFiles)
	}

	// The custom object must reach the link step.
	var linked bool
	for i := range result {
		if result[i].Type != buildjson.StepLink {
			continue
		}
		for _, arg := range result[i].Args {
			if arg == customObj {
				linked = true
			}
		}
	}
	if !linked {
		t.Fatalf("custom bridge object %s not added to link step", customObj)
	}
}

// argsContainPair reports whether args contains a, immediately followed by b.
func argsContainPair(args []string, a, b string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}

func TestExtractIncludeFlags(t *testing.T) {
	steps := []WasmBuildStep{
		{
			Type: buildjson.StepCompile,
			Args: []string{"-c", "-I/usr/include", "-I/usr/local/include", "-isystem", "/opt/include", "test.cc"},
		},
		{
			Type: buildjson.StepCompile,
			Args: []string{"-c", "-I/usr/include", "-I/other/include", "test2.cc"},
		},
		{
			Type:    buildjson.StepCompile,
			Args:    []string{"-c", "-I/skipped/include", "test3.cc"},
			Skipped: true,
		},
	}

	flags := extractIncludeFlags(steps)

	// Should include unique flags only, not from skipped steps
	expected := map[string]bool{
		"-I/usr/include":       true,
		"-I/usr/local/include": true,
		"-isystem":             true,
		"/opt/include":         true,
		"-I/other/include":     true,
	}

	for _, f := range flags {
		delete(expected, f)
	}
	if len(expected) > 0 {
		t.Fatalf("missing expected flags: %v", expected)
	}
}

func TestWasmCompileFlagsIncludesEmscripten(t *testing.T) {
	cfg := WasmConfig{
		WasiSDKPath: "/fake/wasi-sdk",
		Target:      "wasm32-wasip1",
	}
	flags := wasmCompileFlags(cfg)
	found := false
	for _, f := range flags {
		if f == "-D__EMSCRIPTEN__" {
			found = true
			break
		}
	}
	if !found {
		t.Error("wasmCompileFlags should include -D__EMSCRIPTEN__")
	}
}

func TestWasmLinkFlagsIncludesStackSize(t *testing.T) {
	cfg := WasmConfig{
		WasiSDKPath: "/fake/wasi-sdk",
		Target:      "wasm32-wasip1",
	}
	flags := wasmLinkFlags(cfg)
	found := false
	for _, f := range flags {
		if strings.Contains(f, "stack-size") {
			found = true
			break
		}
	}
	if !found {
		t.Error("wasmLinkFlags should include stack-size")
	}
}

func TestAppendExtraLDFlags(t *testing.T) {
	t.Setenv("WASMIFY_EXTRA_LDFLAGS", "-Wl,--wrap=socket -Wl,--wrap=connect")
	steps := []WasmBuildStep{
		{ID: 1, Type: buildjson.StepCompile, Args: []string{"-c", "a.c"}},
		{ID: 2, Type: buildjson.StepLink, Args: []string{"-o", "out.wasm", "a.o"}},
		{ID: 3, Type: buildjson.StepLink, Args: []string{"-o", "skip.wasm"}, Skipped: true},
	}
	got := appendExtraLDFlags(steps)
	// Compile step untouched.
	if len(got[0].Args) != 2 {
		t.Errorf("compile step args changed: %v", got[0].Args)
	}
	// Active link step gets both flags.
	joined := strings.Join(got[1].Args, " ")
	if !strings.Contains(joined, "-Wl,--wrap=socket") || !strings.Contains(joined, "-Wl,--wrap=connect") {
		t.Errorf("link step missing wrap flags: %v", got[1].Args)
	}
	// Skipped link step untouched.
	if len(got[2].Args) != 2 {
		t.Errorf("skipped link step changed: %v", got[2].Args)
	}
}

func TestBuildBridgeCompileArgsHostSockets(t *testing.T) {
	base := WasmConfig{WasiSDKPath: "/fake/wasi-sdk", Target: "wasm32-wasip1"}

	// Opt-in OFF (default): no -DWASMIFY_HOST_SOCKETS → portable wasm.
	off := buildBridgeCompileArgs("b.cc", "b.o", base, nil)
	for _, a := range off {
		if a == "-DWASMIFY_HOST_SOCKETS" {
			t.Fatalf("HostSockets off but -DWASMIFY_HOST_SOCKETS present: %v", off)
		}
	}
	// Opt-in ON: the macro is defined so the project's socket shim compiles.
	on := WasmConfig{WasiSDKPath: "/fake/wasi-sdk", Target: "wasm32-wasip1", HostSockets: true}
	args := buildBridgeCompileArgs("b.cc", "b.o", on, nil)
	found := false
	for _, a := range args {
		if a == "-DWASMIFY_HOST_SOCKETS" {
			found = true
		}
	}
	if !found {
		t.Fatalf("HostSockets on but -DWASMIFY_HOST_SOCKETS missing: %v", args)
	}
}

func TestBuildBridgeCompileArgsHostSubprocess(t *testing.T) {
	base := WasmConfig{WasiSDKPath: "/fake/wasi-sdk", Target: "wasm32-wasip1"}

	// Opt-in OFF (default): no -DWASMIFY_HOST_SUBPROCESS → portable wasm.
	off := buildBridgeCompileArgs("b.cc", "b.o", base, nil)
	for _, a := range off {
		if a == "-DWASMIFY_HOST_SUBPROCESS" {
			t.Fatalf("HostSubprocess off but -DWASMIFY_HOST_SUBPROCESS present: %v", off)
		}
	}
	// Opt-in ON: the macro is defined so the project's posix_spawn shim compiles.
	on := WasmConfig{WasiSDKPath: "/fake/wasi-sdk", Target: "wasm32-wasip1", HostSubprocess: true}
	args := buildBridgeCompileArgs("b.cc", "b.o", on, nil)
	found := false
	for _, a := range args {
		if a == "-DWASMIFY_HOST_SUBPROCESS" {
			found = true
		}
	}
	if !found {
		t.Fatalf("HostSubprocess on but -DWASMIFY_HOST_SUBPROCESS missing: %v", args)
	}
}

func TestExtraBridgeIncludes(t *testing.T) {
	t.Setenv("WASMIFY_BRIDGE_EXTRA_INCLUDES", "/work:/work/embed::/x")
	got := extraBridgeIncludes()
	want := []string{"/work", "/work/embed", "/x"} // empty segments dropped
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d]=%q, want %q", i, got[i], want[i])
		}
	}
	t.Setenv("WASMIFY_BRIDGE_EXTRA_INCLUDES", "")
	if got := extraBridgeIncludes(); got != nil {
		t.Fatalf("empty env should yield nil, got %v", got)
	}
}

func TestEmscriptenDefineDisabled(t *testing.T) {
	t.Setenv("WASMIFY_NO_EMSCRIPTEN_DEFINE", "1")
	if !emscriptenDefineDisabled() {
		t.Fatal("expected true when WASMIFY_NO_EMSCRIPTEN_DEFINE=1")
	}
	t.Setenv("WASMIFY_NO_EMSCRIPTEN_DEFINE", "")
	if emscriptenDefineDisabled() {
		t.Fatal("expected false when unset")
	}
}
