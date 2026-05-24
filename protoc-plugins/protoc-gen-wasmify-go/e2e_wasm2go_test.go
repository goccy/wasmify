package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// runWasm2GoE2E drives the full runtime=wasm2go pipeline on the
// committed simplelib fixture: build the plugin into a temp directory,
// run `buf generate` in testdata/simplelib_proto_wasm2go/ to produce
// the bridge + transpiled wasm2go package, then `go test ./...`
// against the generated tree. Generated files are git-ignored and
// cleaned up after the test so re-runs are hermetic.
//
// extraEnv is appended to the environment passed to `buf generate` and
// `go test`; pass WASM2GO_MULTIPACKAGE_THRESHOLD=0 to force wasm2go
// into the multi-package + linkname-split layout regardless of wasm
// size. assertLayout runs after generation against the fixture
// directory so callers can verify which layout wasm2go picked.
func runWasm2GoE2E(t *testing.T, extraEnv []string, assertLayout func(t *testing.T, fixtureDir string)) {
	t.Helper()
	if _, err := exec.LookPath("buf"); err != nil {
		t.Skipf("buf not on PATH: %v", err)
	}

	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	pluginDir := filepath.Join(repoRoot, "protoc-plugins", "protoc-gen-wasmify-go")
	fixtureDir := filepath.Join(repoRoot, "testdata", "simplelib_proto_wasm2go")

	binDir := t.TempDir()
	pluginBin := filepath.Join(binDir, "protoc-gen-wasmify-go")
	build := exec.Command("go", "build", "-o", pluginBin, ".")
	build.Dir = pluginDir
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build plugin: %v\n%s", err, out)
	}

	env := append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	env = append(env, extraEnv...)

	cleanup := func() {
		_ = os.Remove(filepath.Join(fixtureDir, "simplelib.go"))
		_ = os.RemoveAll(filepath.Join(fixtureDir, "internal"))
	}
	cleanup()
	t.Cleanup(cleanup)

	gen := exec.Command("buf", "generate")
	gen.Dir = fixtureDir
	gen.Env = env
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("buf generate: %v\n%s", err, out)
	}

	if assertLayout != nil {
		assertLayout(t, fixtureDir)
	}

	run := exec.Command("go", "test", "./...")
	run.Dir = fixtureDir
	run.Env = env
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("go test in fixture: %v\n%s", err, out)
	}
}

// TestE2EWasm2GoBridge_SinglePkg covers wasm2go's single-package layout.
// The simplelib fixture wasm (~411 KB) is well below wasm2go's 1 MiB
// function-body threshold, so wasm2go emits a single main file and the
// plugin synthesises the compat shim + base alias alongside it.
func TestE2EWasm2GoBridge_SinglePkg(t *testing.T) {
	runWasm2GoE2E(t, nil, func(t *testing.T, fixtureDir string) {
		// Single-package mode: the plugin must have emitted its compat
		// shim. Its presence confirms wasm2go chose single-file output.
		shim := filepath.Join(fixtureDir, "internal", "wasm2go", "wasm2go_compat.go")
		if _, err := os.Stat(shim); err != nil {
			t.Fatalf("single-pkg layout marker missing: %v", err)
		}
	})
}

// TestE2EWasm2GoBridge_MultiPkg covers wasm2go's multi-package +
// linkname-split layout on the same fixture by forcing wasm2go's
// threshold to 0 via WASM2GO_MULTIPACKAGE_THRESHOLD. wasm2go then
// emits a real base.Module type in base/base.go (rather than the
// plugin's alias), exercises the linkname-split chunk emission, and
// the bridge consumes the multi-package surface unchanged.
func TestE2EWasm2GoBridge_MultiPkg(t *testing.T) {
	runWasm2GoE2E(t,
		[]string{"WASM2GO_MULTIPACKAGE_THRESHOLD=0"},
		func(t *testing.T, fixtureDir string) {
			// Multi-package mode: the plugin must NOT have emitted its
			// compat shim — its absence confirms wasm2go owned the
			// surface this run.
			shim := filepath.Join(fixtureDir, "internal", "wasm2go", "wasm2go_compat.go")
			if _, err := os.Stat(shim); err == nil {
				t.Errorf("multi-pkg run also emitted compat shim %q — the plugin must skip it when wasm2go provides the multi-package surface itself", shim)
			}
			// And the per-chunk packages should exist.
			p0 := filepath.Join(fixtureDir, "internal", "wasm2go", "p0")
			if _, err := os.Stat(p0); err != nil {
				t.Errorf("multi-pkg layout marker missing — expected directory %q: %v", p0, err)
			}
		},
	)
}
