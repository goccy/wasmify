package wasmify_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/wasmify/internal/apispec"
)

// TestIntegration_ParseHeadersAndGenProto runs the full pipeline:
// init → save-arch → parse-headers → gen-proto on testdata/simplelib.
func TestIntegration_ParseHeadersAndGenProto(t *testing.T) {
	// Check clang is available
	if _, err := exec.LookPath("clang"); err != nil {
		t.Skip("clang not found, skipping integration test")
	}

	// Build wasmify binary
	wasmifyBin := filepath.Join(t.TempDir(), "wasmify")
	buildCmd := exec.Command("go", "build", "-o", wasmifyBin, "./cmd/wasmify/")
	buildCmd.Dir = projectRoot(t)
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build wasmify: %v\n%s", err, out)
	}

	projectPath := filepath.Join(projectRoot(t), "testdata", "simplelib")

	// Use a temp dir for config to avoid polluting real config
	configDir := t.TempDir()
	env := append(os.Environ(), "XDG_CONFIG_HOME="+configDir)

	// Dedicated output dir shared across every step (init/save-arch/parse-
	// headers/gen-proto). Without this, each runWasmify call would use a
	// fresh cmd.Dir, and artifacts like arch.json / api-spec.json would be
	// scattered across throwaway locations.
	outDir := t.TempDir()

	// Step 1: init
	t.Run("init", func(t *testing.T) {
		out := runWasmify(t, wasmifyBin, env, "init", projectPath, "--output-dir", outDir)
		var result map[string]string
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("init output is not valid JSON: %v\noutput: %s", err, out)
		}
		if result["project_path"] != projectPath {
			t.Errorf("project_path = %q, want %q", result["project_path"], projectPath)
		}
		if result["data_dir"] == "" {
			t.Error("data_dir is empty")
		}
	})

	// Step 2: save-arch. Paths in arch.json must be relative (the file is
	// committed to git), so we point root_dir and source_dirs at "." —
	// runWasmify sets cmd.Dir = projectPath, making that equivalent to the
	// test project directory.
	t.Run("save-arch", func(t *testing.T) {
		archJSON := `{
			"project": {
				"name": "simplelib",
				"root_dir": ".",
				"language": "c++",
				"language_standard": "c++17"
			},
			"build_system": {
				"type": "make",
				"files": ["Makefile"]
			},
			"targets": [
				{
					"name": "libcalculator",
					"type": "library",
					"source_dirs": ["."]
				}
			],
			"build_commands": {
				"configure": null,
				"build": "make"
			}
		}`

		cmd := exec.Command(wasmifyBin, "save-arch")
		cmd.Stdin = strings.NewReader(archJSON)
		cmd.Env = env
		cmd.Dir = outDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("save-arch failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "saved") {
			t.Errorf("unexpected output: %s", out)
		}
	})

	// Step 3: parse-headers
	t.Run("parse-headers", func(t *testing.T) {
		headerPath := filepath.Join(projectPath, "calculator.h")
		out := runWasmifyIn(t, wasmifyBin, env, outDir, "parse-headers", "--header", headerPath)

		if !strings.Contains(out, "api-spec.json generated") {
			t.Fatalf("parse-headers did not generate api-spec.json: %s", out)
		}

		// api-spec.json is saved to the output dir (committed alongside the
		// project's arch.json / build.json), not the ephemeral .wasmify dir.
		specPath := filepath.Join(outDir, "api-spec.json")
		specData, err := os.ReadFile(specPath)
		if err != nil {
			t.Fatalf("failed to read api-spec.json: %v", err)
		}

		var spec apispec.APISpec
		if err := json.Unmarshal(specData, &spec); err != nil {
			t.Fatalf("failed to parse api-spec.json: %v", err)
		}

		// Verify expected content
		t.Logf("API spec: %d functions, %d classes, %d enums", len(spec.Functions), len(spec.Classes), len(spec.Enums))

		// Should have free functions: add, format_result, version
		if len(spec.Functions) == 0 {
			t.Error("expected at least one function")
		}
		funcNames := make(map[string]bool)
		for _, fn := range spec.Functions {
			funcNames[fn.Name] = true
			t.Logf("  function: %s (%s)", fn.Name, fn.QualName)
		}
		for _, expected := range []string{"add", "format_result", "version", "aggregate_results"} {
			if !funcNames[expected] {
				t.Errorf("missing expected function: %s", expected)
			}
		}

		// aggregate_results returns ResultList, a namespace-scope typedef
		// for `std::vector<Result>`. The underlying spelling clang records
		// has the inner identifier unqualified ("Result"), so the parser
		// must rewrite it to "calc::Result" via postProcessTypedefAliases
		// — otherwise downstream consumers (proto gen, bridge gen) cannot
		// resolve the inner type back to its class. Regression guard for
		// the typedef inner-identifier qualification fix.
		for _, fn := range spec.Functions {
			if fn.Name != "aggregate_results" {
				continue
			}
			rt := fn.ReturnType
			if rt.Inner == nil {
				t.Errorf("aggregate_results: expected vector return with Inner set, got %+v", rt)
				break
			}
			if got := rt.Inner.Name; got != "calc::Result" {
				t.Errorf("aggregate_results inner type = %q, want %q (typedef inner identifiers must be FQ-qualified)", got, "calc::Result")
			}
		}

		// Should have classes: Calculator, ScientificCalculator, Result
		if len(spec.Classes) == 0 {
			t.Error("expected at least one class")
		}
		classMap := make(map[string]*apispec.Class)
		for i, c := range spec.Classes {
			classMap[c.Name] = &spec.Classes[i]
			t.Logf("  class: %s (handle=%v, parent=%q, methods=%d, fields=%d)",
				c.Name, c.IsHandle, c.Parent, len(c.Methods), len(c.Fields))
		}

		// Calculator should be a handle (has virtual destructor)
		if calc, ok := classMap["Calculator"]; ok {
			if !calc.IsHandle {
				t.Error("Calculator should be a handle type (has virtual destructor)")
			}
			if len(calc.Methods) == 0 {
				t.Error("Calculator should have methods")
			}
		} else {
			t.Error("missing Calculator class")
		}

		// ScientificCalculator should have Calculator as parent
		if sci, ok := classMap["ScientificCalculator"]; ok {
			if !sci.IsHandle {
				t.Error("ScientificCalculator should be a handle type")
			}
			if !strings.Contains(sci.Parent, "Calculator") {
				t.Errorf("ScientificCalculator parent = %q, want containing 'Calculator'", sci.Parent)
			}
		} else {
			t.Error("missing ScientificCalculator class")
		}

		// Should have Operation enum
		if len(spec.Enums) == 0 {
			t.Error("expected at least one enum")
		}
		enumFound := false
		for _, e := range spec.Enums {
			t.Logf("  enum: %s (scoped=%v, values=%d)", e.Name, e.IsScoped, len(e.Values))
			if e.Name == "Operation" {
				enumFound = true
				if !e.IsScoped {
					t.Error("Operation should be a scoped enum (enum class)")
				}
				if len(e.Values) != 4 {
					t.Errorf("Operation should have 4 values, got %d", len(e.Values))
				}
			}
		}
		if !enumFound {
			t.Error("missing Operation enum")
		}
	})

	// Step 4: gen-proto
	t.Run("gen-proto", func(t *testing.T) {
		out := runWasmifyIn(t, wasmifyBin, env, outDir, "gen-proto", "--package", "calculator")

		if !strings.Contains(out, "Proto bridge generated") {
			t.Fatalf("gen-proto did not complete: %s", out)
		}

		// Verify .proto file (lives in outDir/proto/, not the ephemeral data dir).
		protoPath := filepath.Join(outDir, "proto", "calculator.proto")
		protoData, err := os.ReadFile(protoPath)
		if err != nil {
			t.Fatalf("failed to read calculator.proto: %v", err)
		}
		proto := string(protoData)

		t.Logf("Proto file length: %d bytes", len(proto))

		checks := []struct {
			name string
			want string
		}{
			{"syntax", `syntax = "proto3"`},
			{"package", "package wasmify.calculator"},
			{"options import", `import "wasmify/options.proto"`},
			{"enum", "enum Operation {"},
			{"handle msg", "message Calculator {"},
			{"wasm_handle opt", "wasm_handle"},
			{"ptr field", "uint64 ptr = 1"},
			{"sci msg", "message ScientificCalculator {"},
			{"wasm_parent opt", "wasm_parent"},
			{"value msg", "message Result {"},
			{"service", "CalculatorService"},
			{"free rpc", "rpc Free("},
		}
		for _, c := range checks {
			if !strings.Contains(proto, c.want) {
				t.Errorf("[%s] proto does not contain %q", c.name, c.want)
			}
		}

		// Result should NOT have ptr field (it's a value type)
		// Find the Result message block and check
		if idx := strings.Index(proto, "message Result {"); idx >= 0 {
			endIdx := strings.Index(proto[idx:], "}")
			resultBlock := proto[idx : idx+endIdx]
			if strings.Contains(resultBlock, "uint64 ptr") {
				t.Error("Result (value type) should not have ptr field")
			}
		}

		// Verify options.proto (sits under outDir/proto/wasmify/)
		optionsPath := filepath.Join(outDir, "proto", "wasmify", "options.proto")
		if _, err := os.ReadFile(optionsPath); err != nil {
			t.Errorf("missing wasmify/options.proto: %v", err)
		}

		// Verify bridge files. api_bridge.cc lives under .wasmify/wasm-build/
		// (the build-cache location — fed to wasi-sdk as part of the wasm
		// compile inputs). A committed copy also exists under outDir/bridge/.
		dataDir := filepath.Join(outDir, ".wasmify")
		bridgeCCPath := filepath.Join(dataDir, "wasm-build", "src", "api_bridge.cc")
		bridgeData, err := os.ReadFile(bridgeCCPath)
		if err != nil {
			t.Fatalf("failed to read api_bridge.cc: %v", err)
		}
		bridge := string(bridgeData)

		bridgeChecks := []struct {
			name string
			want string
		}{
			{"wasm_alloc export", "wasm_alloc"},
			{"wasm_free export", "wasm_free"},
			{"wasmify_get_type_name export", "WASM_EXPORT(wasmify_get_type_name)"},
			{"callback import", "wasmify_callback_invoke"},
			// Per-method exports replace the old dispatch_<MsgName>
			// switch. Calculator is one of the handle services in
			// the simplelib fixture; check that at least its first
			// method export shows up.
			{"per-method export", "WASM_EXPORT(w_"},
		}
		for _, c := range bridgeChecks {
			if !strings.Contains(bridge, c.want) {
				t.Errorf("[%s] bridge does not contain %q", c.name, c.want)
			}
		}

		bridgeHPath := filepath.Join(dataDir, "wasm-build", "src", "api_bridge.h")
		if _, err := os.ReadFile(bridgeHPath); err != nil {
			t.Errorf("missing api_bridge.h: %v", err)
		}
	})

	// Step 5: verify cache state
	t.Run("status", func(t *testing.T) {
		out := runWasmifyIn(t, wasmifyBin, env, outDir, "status")

		var status map[string]any
		if err := json.Unmarshal([]byte(out), &status); err != nil {
			t.Fatalf("status output is not valid JSON: %v\noutput: %s", err, out)
		}

		cacheData, ok := status["cache"].(map[string]any)
		if !ok {
			t.Fatal("cache is nil or not a map")
		}
		phases, ok := cacheData["completed_phases"].([]any)
		if !ok {
			t.Fatal("completed_phases is nil or not an array")
		}

		completedSet := make(map[string]bool)
		for _, p := range phases {
			completedSet[p.(string)] = true
		}

		for _, expected := range []string{"analyze", "parse-headers", "gen-proto"} {
			if !completedSet[expected] {
				t.Errorf("phase %q not in completed_phases: %v", expected, phases)
			}
		}
	})
}

func runWasmify(t *testing.T, bin string, env []string, args ...string) string {
	t.Helper()
	return runWasmifyIn(t, bin, env, t.TempDir(), args...)
}

// runWasmifyIn runs wasmify from a specific working directory. Non-init
// subcommands require wasmify.json in cwd (go.mod-style project discovery),
// so integration tests that span multiple phases must share a single cwd —
// typically the same outDir where init was run.
func runWasmifyIn(t *testing.T, bin string, env []string, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	cmd.Dir = dir
	stderr := &strings.Builder{}
	cmd.Stderr = stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("wasmify %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, out, stderr.String())
	}
	return string(out)
}

func projectRoot(t *testing.T) string {
	t.Helper()
	// Find the project root by looking for go.mod
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find project root (go.mod)")
		}
		dir = parent
	}
}
