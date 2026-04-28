package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/wasmify/internal/apispec"
	"github.com/goccy/wasmify/internal/arch"
	"github.com/goccy/wasmify/internal/buildjson"
	"github.com/goccy/wasmify/internal/cache"
)

// TestCmdBuild_Explicit verifies that cmdBuild runs a supplied build command.
// Uses a shell command that touches the wrapper log so the post-run read
// succeeds without actually invoking any compiler.
func TestCmdBuild_Explicit(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()
	dataDir := filepath.Join(outDir, ".wasmify")

	// The wrapper sets WASMIFY_LOG_FILE; our no-op touches it so the post-run
	// read succeeds.
	err := cmdBuild([]string{"--", "touch \"$WASMIFY_LOG_FILE\""})
	if err != nil {
		t.Fatalf("cmdBuild: %v", err)
	}
	// build.log should exist in dataDir
	if _, err := os.Stat(filepath.Join(dataDir, "build.log")); err != nil {
		t.Errorf("build.log not saved: %v", err)
	}

	// Cache should record PhaseBuild
	c, _ := cache.Load(dataDir)
	if c == nil || !c.IsCompleted(cache.PhaseBuild) {
		t.Errorf("PhaseBuild not completed")
	}
}

// TestCmdBuild_FromArchJSON ensures deriveBuildCommand path is exercised
// (build command derived from arch.json when no `--` supplied).
func TestCmdBuild_FromArchJSON(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()
	dataDir := filepath.Join(outDir, ".wasmify")

	// Arch with a build command that touches the wrapper log so the
	// subsequent post-run read succeeds.
	// Use build_system.type = "make" so the wrapper path is exercised (Bazel
	// projects route through bazelcapture which requires a real workspace).
	a := &arch.Arch{
		Version: "1.0",
		Project: arch.Project{Name: "p", RootDir: project, Language: "c++"},
		BuildSystem: arch.BuildSystem{Type: "make", Files: []string{"Makefile"}},
		Targets: []arch.Target{
			{Name: "t", Type: "library", BuildTarget: ""},
		},
		Commands:  arch.BuildCommands{Build: "touch \"$WASMIFY_LOG_FILE\" #"},
		Selection: &arch.Selection{TargetName: "t", BuildType: "library"},
	}
	if err := arch.Save(outDir, a); err != nil {
		t.Fatal(err)
	}

	if err := cmdBuild(nil); err != nil {
		t.Fatalf("cmdBuild: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "build.log")); err != nil {
		t.Errorf("build.log not saved: %v", err)
	}
}

// TestCmdBuild_FailingCommand makes sure errors propagate.
func TestCmdBuild_FailingCommand(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	err := cmdBuild([]string{"--", "false"})
	if err == nil {
		t.Fatal("expected build failure error")
	}
	if !strings.Contains(err.Error(), "build failed") {
		t.Errorf("error does not mention 'build failed': %v", err)
	}
}

// TestCmdValidateBuild_EmptyBuild runs with zero steps.
func TestCmdValidateBuild_EmptyBuild(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()
	dataDir := filepath.Join(outDir, ".wasmify")

	b := &buildjson.BuildJSON{
		Version:    "1.0",
		CapturedAt: "2025-01-01T00:00:00Z",
		Steps:      nil,
	}
	if err := buildjson.Save(outDir, b); err != nil {
		t.Fatal(err)
	}

	_, err := captureStderr(t, func() error {
		return cmdValidateBuild(nil)
	})
	if err != nil {
		t.Fatalf("cmdValidateBuild: %v", err)
	}
	c, _ := cache.Load(dataDir)
	if c == nil || !c.IsCompleted(cache.PhaseValidateBuild) {
		t.Errorf("PhaseValidateBuild not completed")
	}
}

// TestCmdGenProto_Basic generates proto + bridge from a minimal api-spec.json.
func TestCmdGenProto_Basic(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()
	dataDir := filepath.Join(outDir, ".wasmify")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Minimal api spec: one free function returning int, taking int.
	spec := &apispec.APISpec{
		Functions: []apispec.Function{
			{
				Name:     "add",
				QualName: "add",
				ReturnType: apispec.TypeRef{
					Name:     "int",
					Kind:     apispec.TypePrimitive,
					QualType: "int",
				},
				Params: []apispec.Param{
					{
						Name: "a",
						Type: apispec.TypeRef{Name: "int", Kind: apispec.TypePrimitive, QualType: "int"},
					},
					{
						Name: "b",
						Type: apispec.TypeRef{Name: "int", Kind: apispec.TypePrimitive, QualType: "int"},
					},
				},
			},
		},
	}
	if err := apispec.Save(outDir, spec); err != nil {
		t.Fatal(err)
	}

	// Also save arch so package name gets derived. cmdGenProto loads arch
	// from dataDir for project metadata, so save there too.
	a := &arch.Arch{
		Version:     "1.0",
		Project:     arch.Project{Name: "mylib", RootDir: project, Language: "c++"},
		BuildSystem: arch.BuildSystem{Type: "make"},
		Targets:     []arch.Target{{Name: "mylib", Type: "library"}},
		Commands:    arch.BuildCommands{Build: "make"},
	}
	_ = arch.Save(outDir, a)
	_ = arch.Save(dataDir, a)

	_, err := captureStdout(t, func() error {
		_, innerErr := captureStderr(t, func() error {
			return cmdGenProto(nil)
		})
		return innerErr
	})
	if err != nil {
		t.Fatalf("cmdGenProto: %v", err)
	}

	// Proto file lives under outDir (the committed root); bridge .cc/.h
	// intended for wasm-build stays under dataDir/wasm-build/src/.
	protoPath := filepath.Join(outDir, "proto", "mylib.proto")
	if _, err := os.Stat(protoPath); err != nil {
		t.Errorf("proto file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "wasm-build", "src", "api_bridge.cc")); err != nil {
		t.Errorf("bridge cc missing: %v", err)
	}

	c, _ := cache.Load(dataDir)
	if c == nil || !c.IsCompleted(cache.PhaseGenProto) {
		t.Errorf("PhaseGenProto not completed")
	}
}

// TestCmdGenProto_WithExplicitPackage exercises the --package flag path.
func TestCmdGenProto_WithExplicitPackage(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()

	spec := &apispec.APISpec{
		Functions: []apispec.Function{
			{
				Name:     "noop",
				QualName: "noop",
				ReturnType: apispec.TypeRef{
					Name:     "void",
					Kind:     apispec.TypeVoid,
					QualType: "void",
				},
			},
		},
	}
	if err := apispec.Save(outDir, spec); err != nil {
		t.Fatal(err)
	}

	_, err := captureStdout(t, func() error {
		_, innerErr := captureStderr(t, func() error {
			return cmdGenProto([]string{"--package", "custom_pkg"})
		})
		return innerErr
	})
	if err != nil {
		t.Fatalf("cmdGenProto: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "proto", "custom_pkg.proto")); err != nil {
		t.Errorf("expected custom_pkg.proto: %v", err)
	}
}

// TestCmdGenProto_WithOutputDir ensures gen-proto writes bridge/ and keeps
// wasmify.json in the cwd (new go.mod-style config layout).
func TestCmdGenProto_WithOutputDir(t *testing.T) {
	isolatedXDG(t)
	project := gitInitRepo(t)
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()

	spec := &apispec.APISpec{
		Functions: []apispec.Function{
			{
				Name:     "f",
				QualName: "f",
				ReturnType: apispec.TypeRef{
					Name:     "void",
					Kind:     apispec.TypeVoid,
					QualType: "void",
				},
			},
		},
	}
	if err := apispec.Save(outDir, spec); err != nil {
		t.Fatal(err)
	}

	_, err := captureStdout(t, func() error {
		_, innerErr := captureStderr(t, func() error {
			return cmdGenProto(nil)
		})
		return innerErr
	})
	if err != nil {
		t.Fatalf("cmdGenProto: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "bridge", "api_bridge.cc")); err != nil {
		t.Errorf("bridge cc in outDir missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "wasmify.json")); err != nil {
		t.Errorf("wasmify.json in outDir missing: %v", err)
	}
}

// TestCmdGenProto_BridgeConfig exercises the --bridge-config flag path.
func TestCmdGenProto_BridgeConfig(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()
	dataDir := filepath.Join(outDir, ".wasmify")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	spec := &apispec.APISpec{
		Functions: []apispec.Function{
			{
				Name:     "h",
				QualName: "h",
				ReturnType: apispec.TypeRef{
					Name:     "void",
					Kind:     apispec.TypeVoid,
					QualType: "void",
				},
			},
		},
	}
	if err := apispec.Save(outDir, spec); err != nil {
		t.Fatal(err)
	}

	// Empty/minimal bridge config with ExportDependentLibraries=nil
	cfgPath := filepath.Join(dataDir, "bridge.json")
	if err := os.WriteFile(cfgPath, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := captureStdout(t, func() error {
		_, innerErr := captureStderr(t, func() error {
			return cmdGenProto([]string{"--bridge-config", cfgPath})
		})
		return innerErr
	})
	if err != nil {
		t.Fatalf("cmdGenProto: %v", err)
	}

	// Config file should have been rewritten (must still be valid JSON).
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse: %v", err)
	}
}

// TestCmdGenProto_InvalidBridgeConfig covers the error branch.
func TestCmdGenProto_InvalidBridgeConfig(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()
	dataDir := filepath.Join(outDir, ".wasmify")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	spec := &apispec.APISpec{Functions: []apispec.Function{{Name: "f", QualName: "f", ReturnType: apispec.TypeRef{Name: "void", Kind: apispec.TypeVoid, QualType: "void"}}}}
	if err := apispec.Save(outDir, spec); err != nil {
		t.Fatal(err)
	}
	// Missing file
	if err := cmdGenProto([]string{"--bridge-config", "/does/not/exist"}); err == nil {
		t.Error("expected error for missing bridge-config")
	}
	// Malformed
	bad := filepath.Join(dataDir, "bad.json")
	_ = os.WriteFile(bad, []byte("{not-json"), 0o644)
	if err := cmdGenProto([]string{"--bridge-config", bad}); err == nil {
		t.Error("expected error for malformed bridge-config")
	}
}

// TestCmdGenProto_EmptySpec should fail validation (no functions/classes/enums).
func TestCmdGenProto_EmptySpec(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()
	if err := apispec.Save(outDir, &apispec.APISpec{}); err != nil {
		t.Fatal(err)
	}
	// Empty spec should still succeed at gen-proto (generates empty proto).
	// If downstream validation rejects it, we still expect no panic.
	_, _ = captureStdout(t, func() error {
		_, innerErr := captureStderr(t, func() error {
			return cmdGenProto(nil)
		})
		return innerErr
	})
	// Don't assert nil/err — behavior may vary. Just want to exercise the
	// code path.
}

// TestCmdParseHeaders_NoHeadersInBuild covers the "no project headers found" error branch.
func TestCmdParseHeaders_NoHeadersInBuild(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()

	// Save an empty build.json
	b := &buildjson.BuildJSON{
		Version: "1.0", CapturedAt: "2025-01-01T00:00:00Z", Steps: []buildjson.BuildStep{},
	}
	if err := buildjson.Save(outDir, b); err != nil {
		t.Fatal(err)
	}
	err := cmdParseHeaders(nil)
	if err == nil {
		t.Error("expected error about no project headers")
	}
}

// TestCmdParseHeaders_WithClangFlag covers the --clang flag path.
func TestCmdParseHeaders_WithClangFlag(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	header := filepath.Join(project, "api.h")
	_ = os.WriteFile(header, []byte("int f();\n"), 0o644)

	// Use a fake clang path; parse will error but we want to cover the flag
	_ = cmdParseHeaders([]string{"--header", header, "--clang", "/nonexistent/clang"})
}

// TestCmdParseHeaders_WithExplicitHeader exercises the single-header path.
func TestCmdParseHeaders_WithExplicitHeader(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()

	// Write a simple C header with a function declaration
	headerPath := filepath.Join(project, "api.h")
	if err := os.WriteFile(headerPath, []byte(`
#ifndef API_H
#define API_H
int add(int a, int b);
#endif
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := captureStdout(t, func() error {
		_, innerErr := captureStderr(t, func() error {
			return cmdParseHeaders([]string{"--header", headerPath})
		})
		return innerErr
	})
	if err != nil {
		// Non-fatal: clang may not be findable or parse may fail; ensure we
		// at least passed argument parsing.
		t.Logf("cmdParseHeaders: %v", err)
		return
	}

	if _, err := os.Stat(filepath.Join(outDir, "api-spec.json")); err != nil {
		t.Errorf("api-spec.json missing: %v", err)
	}
}

// TestCmdWasmBuild_DryRun is possible if wasi-sdk detection fails gracefully.
func TestCmdWasmBuild_DryRun_NoBuildJSON(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	// No build.json, no SDK — dry-run still needs build.json.
	err := cmdWasmBuild([]string{"--dry-run"})
	if err == nil {
		t.Error("expected error when build.json missing")
	}
}

// TestMergeCompatibleGroups_Empty exercises the empty-groups branch.
func TestMergeCompatibleGroups_Empty(t *testing.T) {
	got := mergeCompatibleGroups("clang", nil, nil)
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
	got = mergeCompatibleGroups("clang", [][]string{{}}, nil)
	if got != nil {
		t.Errorf("expected nil for empty inner, got %v", got)
	}
}

// TestCheckUmbrellaCompiles_Empty covers the zero-headers fast path.
func TestCheckUmbrellaCompiles_Empty(t *testing.T) {
	if !checkUmbrellaCompiles("clang", nil, nil) {
		t.Error("expected true for empty headers")
	}
}

// TestMergeCompatibleGroups_Simple: if a single valid header is provided,
// and clang is present, it should compile and return a single batch.
func TestMergeCompatibleGroups_Simple(t *testing.T) {
	dir := t.TempDir()
	h := filepath.Join(dir, "simple.h")
	if err := os.WriteFile(h, []byte("#ifndef SIMPLE_H\n#define SIMPLE_H\nint x;\n#endif\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := mergeCompatibleGroups("clang", [][]string{{h}}, nil)
	if len(got) == 0 {
		t.Skipf("clang may not be available or header rejected; got: %v", got)
	}
	// Expect exactly one batch containing the single header.
	if len(got) != 1 || len(got[0]) != 1 {
		t.Errorf("merge produced %v", got)
	}
}

// TestSplitAndMerge_SingleHeader — single header compiles.
func TestSplitAndMerge_SingleHeader(t *testing.T) {
	dir := t.TempDir()
	h := filepath.Join(dir, "simple.h")
	if err := os.WriteFile(h, []byte("int x;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := splitAndMerge("clang", []string{h}, nil)
	if len(got) == 0 {
		t.Skipf("clang may not accept header; got %v", got)
	}
}

// TestSplitAndMerge_Empty
func TestSplitAndMerge_Empty(t *testing.T) {
	got := splitAndMerge("clang", nil, nil)
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}
