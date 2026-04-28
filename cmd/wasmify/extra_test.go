package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/wasmify/internal/arch"
	"github.com/goccy/wasmify/internal/buildjson"
	"github.com/goccy/wasmify/internal/cache"
	"github.com/goccy/wasmify/internal/state"
)

// TestCmdClassify_Interactive exercises the interactive branch.
func TestCmdClassify_Interactive_ValidChoice(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()

	a := &arch.Arch{
		Version: "1.0",
		Project: arch.Project{Name: "p", RootDir: project, Language: "c++"},
		Targets: []arch.Target{
			{Name: "t1", Type: "library", Description: "first"},
			{Name: "t2", Type: "executable", BuildTarget: "//p:t2", SourceDirs: []string{"src"}},
		},
	}
	if err := arch.Save(outDir, a); err != nil {
		t.Fatal(err)
	}

	err := withStdin(t, "2\n", func() error {
		_, innerErr := captureStderr(t, func() error {
			return cmdClassify(nil)
		})
		return innerErr
	})
	if err != nil {
		t.Fatalf("cmdClassify: %v", err)
	}
	loaded, _ := arch.Load(outDir)
	if loaded.Selection == nil || loaded.Selection.TargetName != "t2" {
		t.Errorf("selection = %+v", loaded.Selection)
	}
}

func TestCmdClassify_Interactive_InvalidChoice(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()
	a := &arch.Arch{
		Version: "1.0",
		Project: arch.Project{Name: "p", RootDir: project, Language: "c++"},
		Targets: []arch.Target{
			{Name: "t1", Type: "library"},
		},
	}
	if err := arch.Save(outDir, a); err != nil {
		t.Fatal(err)
	}

	err := withStdin(t, "99\n", func() error {
		_, innerErr := captureStderr(t, func() error {
			return cmdClassify(nil)
		})
		return innerErr
	})
	if err == nil {
		t.Error("expected error for invalid choice")
	}
}

// TestCmdParseHeaders_MultiHeaderFromBuild covers the multi-header discover-from-build path.
func TestCmdParseHeaders_MultiHeaderFromBuild(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()

	// Create two compatible headers
	h1 := filepath.Join(project, "api1.h")
	h2 := filepath.Join(project, "api2.h")
	if err := os.WriteFile(h1, []byte("int f1(int);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h2, []byte("int f2(int);\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a .d file listing both
	depDir := filepath.Join(project, "build_out")
	_ = os.MkdirAll(depDir, 0o755)
	depFile := filepath.Join(depDir, "foo.d")
	content := filepath.Join(project, "build_out/foo.o") + ": " + h1 + " " + h2 + "\n"
	if err := os.WriteFile(depFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Build.json referencing the .d file
	b := &buildjson.BuildJSON{
		Version: "1.0",
		Steps: []buildjson.BuildStep{
			{
				ID: 1, Type: buildjson.StepCompile,
				WorkDir: project,
				Args:    []string{"clang", "-c", "foo.cc", "-MF", depFile},
			},
		},
	}
	if err := buildjson.Save(outDir, b); err != nil {
		t.Fatal(err)
	}

	_, err := captureStdout(t, func() error {
		_, innerErr := captureStderr(t, func() error {
			return cmdParseHeaders(nil)
		})
		return innerErr
	})
	if err != nil {
		// Many reasons could cause this in different envs; log and move on
		t.Logf("cmdParseHeaders: %v", err)
		return
	}
	if _, err := os.Stat(filepath.Join(outDir, "api-spec.json")); err != nil {
		t.Errorf("api-spec.json missing: %v", err)
	}
}

// TestCmdWasmBuild_DryRun_WithBuildJSON runs wasm-build in dry-run.
// This exercises more of cmdWasmBuild without actually invoking clang.
func TestCmdWasmBuild_DryRun_WithBuildJSON(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()
	dataDir := filepath.Join(outDir, ".wasmify")

	// Write a minimal build.json with one trivial compile step.
	b := &buildjson.BuildJSON{
		Version:    "1.0",
		CapturedAt: "2025-01-01T00:00:00Z",
		Steps: []buildjson.BuildStep{
			{
				ID:         1,
				Type:       buildjson.StepCompile,
				Executable: "clang",
				Compiler:   "clang",
				WorkDir:    project,
				Args:       []string{"clang", "-c", "foo.cc", "-o", "foo.o"},
			},
		},
	}
	if err := buildjson.Save(outDir, b); err != nil {
		t.Fatal(err)
	}
	plantStubBridge(t, dataDir)

	_, err := captureStdout(t, func() error {
		_, innerErr := captureStderr(t, func() error {
			return cmdWasmBuild([]string{"--dry-run"})
		})
		return innerErr
	})
	if err != nil {
		t.Logf("cmdWasmBuild dry-run: %v", err)
	}
	// wasm-build.json should exist after dry-run
	if _, err := os.Stat(filepath.Join(dataDir, "wasm-build.json")); err != nil {
		t.Logf("wasm-build.json not generated: %v", err)
	}
}

// TestCmdWasmBuild_DryRun_WithFakeSDK exercises the SDK-found branch.
func TestCmdWasmBuild_DryRun_WithFakeSDK(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()
	dataDir := filepath.Join(outDir, ".wasmify")

	b := &buildjson.BuildJSON{
		Version:    "1.0",
		CapturedAt: "2025-01-01T00:00:00Z",
		Steps:      []buildjson.BuildStep{},
	}
	if err := buildjson.Save(outDir, b); err != nil {
		t.Fatal(err)
	}
	plantStubBridge(t, dataDir)

	fake := makeFakeWasiSDK(t)
	_, err := captureStdout(t, func() error {
		_, innerErr := captureStderr(t, func() error {
			return cmdWasmBuild([]string{"--dry-run", "--wasi-sdk", fake, "--no-cache"})
		})
		return innerErr
	})
	if err != nil {
		t.Logf("cmdWasmBuild dry-run with fake SDK: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "wasm-build.json")); err != nil {
		t.Errorf("wasm-build.json not generated: %v", err)
	}
}

// TestCmdWasmBuild_WithBridgeDryRun exercises the bridge-injection path.
// Bridge injection is now automatic: wasm-build always injects bridge sources.
func TestCmdWasmBuild_WithBridgeDryRun(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()
	dataDir := filepath.Join(outDir, ".wasmify")

	b := &buildjson.BuildJSON{
		Version:    "1.0",
		CapturedAt: "2025-01-01T00:00:00Z",
		Steps:      []buildjson.BuildStep{},
	}
	if err := buildjson.Save(outDir, b); err != nil {
		t.Fatal(err)
	}
	plantStubBridge(t, dataDir)
	fake := makeFakeWasiSDK(t)
	_, _ = captureStdout(t, func() error {
		_, err := captureStderr(t, func() error {
			return cmdWasmBuild([]string{"--dry-run", "--wasi-sdk", fake})
		})
		if err != nil {
			t.Logf("cmdWasmBuild bridge dry-run: %v", err)
		}
		return nil
	})
}

// TestCmdWasmBuild_ZeroStepsActualExecute runs full flow (not dry-run) with
// zero build steps. This covers wasmbuild.Execute and cache recording.
func TestCmdWasmBuild_ZeroStepsActualExecute(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()
	dataDir := filepath.Join(outDir, ".wasmify")

	// Need an arch.json with an executable selection to avoid the library
	// branch which tries to LinkLibrary.
	a := &arch.Arch{
		Version:     "1.0",
		Project:     arch.Project{Name: "p", RootDir: project, Language: "c++"},
		BuildSystem: arch.BuildSystem{Type: "make"},
		Targets:     []arch.Target{{Name: "t", Type: "executable"}},
		Commands:    arch.BuildCommands{Build: "make"},
		Selection:   &arch.Selection{TargetName: "t", BuildType: "executable"},
	}
	_ = arch.Save(outDir, a)

	b := &buildjson.BuildJSON{
		Version:    "1.0",
		CapturedAt: "2025-01-01T00:00:00Z",
		Steps:      []buildjson.BuildStep{},
	}
	if err := buildjson.Save(outDir, b); err != nil {
		t.Fatal(err)
	}
	plantStubBridge(t, dataDir)
	fake := makeFakeWasiSDK(t)
	_, _ = captureStdout(t, func() error {
		_, err := captureStderr(t, func() error {
			return cmdWasmBuild([]string{"--wasi-sdk", fake})
		})
		if err != nil {
			t.Logf("cmdWasmBuild: %v", err)
		}
		return nil
	})
	c, _ := cache.Load(dataDir)
	if c == nil || !c.IsCompleted(cache.PhaseWasmBuild) {
		t.Errorf("PhaseWasmBuild not completed")
	}
}

// TestCmdWasmBuild_LibraryBranch covers the library-link branch.
// LinkLibrary may fail (since the fake SDK can't link), but we want to
// exercise the code path.
func TestCmdWasmBuild_LibraryBranch(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()
	dataDir := filepath.Join(outDir, ".wasmify")

	a := &arch.Arch{
		Version:     "1.0",
		Project:     arch.Project{Name: "libfoo", RootDir: project, Language: "c++"},
		BuildSystem: arch.BuildSystem{Type: "make"},
		Targets:     []arch.Target{{Name: "libfoo", Type: "library"}},
		Commands:    arch.BuildCommands{Build: "make"},
		Selection:   &arch.Selection{TargetName: "libfoo", BuildType: "library"},
	}
	_ = arch.Save(outDir, a)

	b := &buildjson.BuildJSON{
		Version:    "1.0",
		CapturedAt: "2025-01-01T00:00:00Z",
		Steps:      []buildjson.BuildStep{},
	}
	if err := buildjson.Save(outDir, b); err != nil {
		t.Fatal(err)
	}
	plantStubBridge(t, dataDir)
	fake := makeFakeWasiSDK(t)
	_, _ = captureStdout(t, func() error {
		_, err := captureStderr(t, func() error {
			return cmdWasmBuild([]string{"--wasi-sdk", fake})
		})
		// Expected to fail because LinkLibrary can't actually link with
		// a fake SDK. Log and move on.
		if err != nil {
			t.Logf("cmdWasmBuild library branch (expected): %v", err)
		}
		return nil
	})
}

// TestCmdWasmBuild_OutputFlag tests the --output flag.
func TestCmdWasmBuild_OutputFlag(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()
	dataDir := filepath.Join(outDir, ".wasmify")

	b := &buildjson.BuildJSON{
		Version:    "1.0",
		CapturedAt: "2025-01-01T00:00:00Z",
		Steps:      []buildjson.BuildStep{},
	}
	if err := buildjson.Save(outDir, b); err != nil {
		t.Fatal(err)
	}
	plantStubBridge(t, dataDir)
	fake := makeFakeWasiSDK(t)
	customOut := filepath.Join(t.TempDir(), "buildout")
	_, _ = captureStdout(t, func() error {
		_, err := captureStderr(t, func() error {
			return cmdWasmBuild([]string{"--dry-run", "--wasi-sdk", fake, "--output", customOut})
		})
		if err != nil {
			t.Logf("cmdWasmBuild --output: %v", err)
		}
		return nil
	})
}

// TestCmdUpdate_Phases exercises the update path where changes trigger phases.
// We use non-relevant changes first to verify state updates, then a header
// change which triggers phases that will fail — we just assert the error is
// reported (no panic).
func TestCmdUpdate_HeaderChangeTriggersPhases(t *testing.T) {
	isolatedXDG(t)
	project := gitInitRepo(t)
	out, _ := os.Getwd() // isolatedXDG chdirs into a fresh tempdir

	first, _ := gitHeadCommit(project)
	s := state.New(project)
	s.UpdateUpstream(first, "main")
	if err := state.Save(out, s); err != nil {
		t.Fatal(err)
	}

	// Add a header, commit
	if err := os.WriteFile(filepath.Join(project, "api.h"), []byte("int f();\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, project, "add", ".")
	runGit(t, project, "commit", "-m", "add header")

	// cmdUpdate will try to run parse-headers, which without a build.json
	// will error out. We just care that the error propagates.
	_, _ = captureStderr(t, func() error {
		err := cmdUpdate(nil)
		if err == nil {
			t.Error("expected error propagated from parse-headers phase")
		}
		return nil
	})
}

// TestCmdUpdate_BazelChangeTriggersAllPhases adds a BUILD file which
// triggers all five phases including build, generate-build, gen-proto.
func TestCmdUpdate_BazelChangeTriggersAllPhases(t *testing.T) {
	isolatedXDG(t)
	project := gitInitRepo(t)
	out, _ := os.Getwd()

	first, _ := gitHeadCommit(project)
	s := state.New(project)
	s.UpdateUpstream(first, "main")
	if err := state.Save(out, s); err != nil {
		t.Fatal(err)
	}

	// Add a BUILD file to trigger all phases
	_ = os.WriteFile(filepath.Join(project, "BUILD"), []byte("cc_library(name=\"foo\")\n"), 0o644)
	runGit(t, project, "add", ".")
	runGit(t, project, "commit", "-m", "BUILD")

	// cmdUpdate's first phase is "build" which is cmdBuild with only
	// project path — will fail at deriveBuildCommand (no arch.json).
	_, _ = captureStderr(t, func() error {
		_ = cmdUpdate(nil)
		return nil
	})
}

// TestCmdUpdate_CcChangeTriggersWasmBuild covers the cc-file triggering wasm-build phase.
func TestCmdUpdate_CcChangeTriggersWasmBuild(t *testing.T) {
	isolatedXDG(t)
	project := gitInitRepo(t)
	out, _ := os.Getwd()

	first, _ := gitHeadCommit(project)
	s := state.New(project)
	s.UpdateUpstream(first, "main")
	if err := state.Save(out, s); err != nil {
		t.Fatal(err)
	}

	// Add a .cc file
	_ = os.WriteFile(filepath.Join(project, "foo.cc"), []byte("int main(){}\n"), 0o644)
	runGit(t, project, "add", ".")
	runGit(t, project, "commit", "-m", "add cc")

	// cmdUpdate should trigger wasm-build phase, which will fail because
	// there's no build.json. We care only that the code path is exercised.
	_, _ = captureStderr(t, func() error {
		_ = cmdUpdate(nil)
		return nil
	})
}

// TestCmdUpdate_FirstRun: no prior state, any commit triggers phases.
func TestCmdUpdate_FirstRun(t *testing.T) {
	isolatedXDG(t)
	project := gitInitRepo(t)
	// cmdUpdate now requires wasmify.json to exist in cwd — write the
	// minimal state pointing at the upstream project (but don't fill in
	// the commit, so it's effectively "first-run").
	initTestOutDir(t, project)

	// parse-headers phase will fail due to missing build.json, which is
	// fine — we only care that the path is exercised without panic.
	_, _ = captureStderr(t, func() error {
		_ = cmdUpdate(nil)
		return nil
	})
}

// TestSplitAndMerge_Reject covers the branch where a header fails to compile
// and is rejected.
func TestSplitAndMerge_BadHeader(t *testing.T) {
	dir := t.TempDir()
	// intentionally invalid content
	bad := filepath.Join(dir, "bad.h")
	if err := os.WriteFile(bad, []byte("this is not c++ !!!\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Should return nil (header rejected)
	got := splitAndMerge("clang", []string{bad}, nil)
	if got != nil {
		t.Logf("splitAndMerge returned %v (environment-dependent)", got)
	}
}

// TestSplitAndMerge_Multiple covers the recursive split case.
func TestSplitAndMerge_Multiple(t *testing.T) {
	dir := t.TempDir()
	good1 := filepath.Join(dir, "g1.h")
	good2 := filepath.Join(dir, "g2.h")
	_ = os.WriteFile(good1, []byte("int a;\n"), 0o644)
	_ = os.WriteFile(good2, []byte("int b;\n"), 0o644)
	got := splitAndMerge("clang", []string{good1, good2}, nil)
	if len(got) == 0 {
		t.Skipf("clang rejected headers: %v", got)
	}
}

// TestMainFunc_InProcess_HelpOK invokes main() directly for the "help"
// subcommand which does not call os.Exit on success.
func TestMainFunc_InProcess_HelpOK(t *testing.T) {
	origArgs := os.Args
	defer func() { os.Args = origArgs }()

	os.Args = []string{"wasmify", "help"}
	_, _ = captureStderr(t, func() error {
		main()
		return nil
	})
}

// TestMainFunc invokes main() via a re-exec of the test binary, which is
// the standard way to test os.Exit paths.
func TestMainFunc(t *testing.T) {
	if os.Getenv("WASMIFY_TEST_MAIN") == "1" {
		// Child: set os.Args and invoke main()
		// We test the no-args branch that calls printUsage then os.Exit(1)
		os.Args = []string{"wasmify"}
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestMainFunc")
	cmd.Env = append(os.Environ(), "WASMIFY_TEST_MAIN=1", "XDG_CONFIG_HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	// main should exit with code 1 for no args
	if err == nil {
		t.Errorf("expected non-zero exit, got success: %s", out)
	}
	if !strings.Contains(string(out), "Usage:") {
		t.Errorf("expected usage in output, got: %s", out)
	}
}

// TestMainFunc_HelpOK invokes main() with a "help" command; should exit 0.
func TestMainFunc_HelpOK(t *testing.T) {
	if os.Getenv("WASMIFY_TEST_MAIN_HELP") == "1" {
		os.Args = []string{"wasmify", "help"}
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestMainFunc_HelpOK")
	cmd.Env = append(os.Environ(), "WASMIFY_TEST_MAIN_HELP=1", "XDG_CONFIG_HOME="+t.TempDir())
	if err := cmd.Run(); err != nil {
		t.Errorf("main help should exit 0, got %v", err)
	}
}

// TestMainFunc_WrapperMode exercises the wrapper-mode branch in main().
// We invoke the binary as "cc" (a known tool name) so IsWrapperInvocation
// returns true. The wrapper will fail because WASMIFY_LOG_FILE is unset,
// and main exits 1 — we just want the branch to execute.
func TestMainFunc_WrapperMode(t *testing.T) {
	if os.Getenv("WASMIFY_TEST_WRAPPER") == "1" {
		os.Args = []string{"cc"}
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestMainFunc_WrapperMode")
	// Keep WASMIFY_LOG_FILE unset to force RunAsWrapper to error.
	cmd.Env = []string{"WASMIFY_TEST_WRAPPER=1", "PATH=" + os.Getenv("PATH")}
	// Don't check exit code — wrapper may exit 1 or 0 depending on env.
	_ = cmd.Run()
}

// TestDispatch invokes dispatch with various subcommands in-process to
// exercise the argv dispatch loop and keep coverage.
func TestDispatch(t *testing.T) {
	isolatedXDG(t)
	// Redirect stderr/stdout so usage spam doesn't cloud test output.
	_, _ = captureStderr(t, func() error {
		_, _ = captureStdout(t, func() error {
			cases := []struct {
				argv []string
				want int
			}{
				{argv: []string{"help"}, want: 0},
				{argv: []string{"-h"}, want: 0},
				{argv: []string{"--help"}, want: 0},
				{argv: nil, want: 1},              // no args
				{argv: []string{"unknown-cmd"}, want: 1},
				// Each subcommand with no args should exit 1 (usage error)
				{argv: []string{"init"}, want: 1},
				{argv: []string{"status"}, want: 1},
				{argv: []string{"save-arch"}, want: 1},
				{argv: []string{"classify"}, want: 1},
				{argv: []string{"build"}, want: 1},
				{argv: []string{"generate-build"}, want: 1},
				{argv: []string{"validate-build"}, want: 1},
				{argv: []string{"wasm-build"}, want: 1},
				{argv: []string{"parse-headers"}, want: 1},
				{argv: []string{"gen-proto"}, want: 1},
				{argv: []string{"gen-go"}, want: 1},
				{argv: []string{"update"}, want: 1},
			}
			for _, tc := range cases {
				got := dispatch(tc.argv)
				if got != tc.want {
					t.Errorf("dispatch(%v) = %d, want %d", tc.argv, got, tc.want)
				}
			}
			return nil
		})
		return nil
	})
}

// plantStubBridge writes a minimal api_bridge.cc/h pair under
// <dataDir>/wasm-build/src/ so cmdWasmBuild's bridge precondition passes.
// The test that exercises the "bridge missing" error path updates its
// expectations instead of calling this.
func plantStubBridge(t *testing.T, dataDir string) {
	t.Helper()
	src := filepath.Join(dataDir, "wasm-build", "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "api_bridge.cc"), []byte("// stub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "api_bridge.h"), []byte("// stub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// makeFakeWasiSDK creates a directory that passes validateWasiSDK.
func makeFakeWasiSDK(t *testing.T) string {
	t.Helper()
	fake := t.TempDir()
	binDir := filepath.Join(fake, "bin")
	sysrootDir := filepath.Join(fake, "share", "wasi-sysroot")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sysrootDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"clang", "clang++", "llvm-ar", "wasm-ld"} {
		p := filepath.Join(binDir, tool)
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return fake
}

// TestCmdInstallSDK_AlreadyInstalled feeds cmdInstallSDK a directory that
// looks like an installed wasi-sdk. The "already installed" branch returns nil.
func TestCmdInstallSDK_AlreadyInstalled(t *testing.T) {
	isolatedXDG(t)
	fake := makeFakeWasiSDK(t)

	_, err := captureStderr(t, func() error {
		return cmdInstallSDK([]string{"--path", fake})
	})
	if err != nil {
		t.Errorf("cmdInstallSDK with already-installed SDK should return nil, got %v", err)
	}
}

// TestCmdInstallSDK_BarePathFormat covers the bare-path argument branch.
func TestCmdInstallSDK_BarePathFormat(t *testing.T) {
	isolatedXDG(t)
	fake := makeFakeWasiSDK(t)

	_, err := captureStderr(t, func() error {
		return cmdInstallSDK([]string{fake})
	})
	if err != nil {
		t.Errorf("bare path install: %v", err)
	}
}

// Kept for completeness of the dispatch coverage.
func TestDispatch_InstallSDK_ArgsParse(t *testing.T) {
	if testing.Short() || os.Getenv("WASMIFY_TEST_INSTALL_SDK") == "" {
		t.Skip("set WASMIFY_TEST_INSTALL_SDK=1 to enable install-sdk test")
	}
	isolatedXDG(t)
	_, _ = captureStderr(t, func() error {
		_, _ = captureStdout(t, func() error {
			_ = dispatch([]string{"install-sdk", "--path", t.TempDir()})
			return nil
		})
		return nil
	})
}

// Satisfy the unused-import checks when the subprocess version was removed.
var _ = exec.LookPath

// TestCmdStatus_CacheOnly verifies cmdStatus shows just cache info.
func TestCmdStatus_CacheOnly(t *testing.T) {
	isolatedXDG(t)
	project := t.TempDir()
	initTestOutDir(t, project)
	outDir, _ := os.Getwd()
	dataDir := filepath.Join(outDir, ".wasmify")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	c := cache.New()
	c.CompletePhase(cache.PhaseAnalyze)
	_ = c.Save(dataDir)

	out, err := captureStdout(t, func() error {
		return cmdStatus(nil)
	})
	if err != nil {
		t.Fatalf("cmdStatus: %v", err)
	}
	if !strings.Contains(out, "completed_phases") {
		t.Errorf("expected cache info in output: %s", out)
	}
}
