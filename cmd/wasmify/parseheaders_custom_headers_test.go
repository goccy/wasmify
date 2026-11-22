package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/wasmify/internal/apispec"
	"github.com/goccy/wasmify/internal/state"
)

// sourceFileStub is a runBatchClang stand-in that records every batch and
// returns one Function per header, declared in that header — the shape the
// real parser produces, so SourceFile handling can be asserted.
type sourceFileStub struct {
	batches [][]string
}

func (s *sourceFileStub) handler(_ string, _ string, batch []string, _ []string) (*apispec.APISpec, error) {
	s.batches = append(s.batches, append([]string(nil), batch...))
	spec := &apispec.APISpec{}
	for _, h := range batch {
		spec.Functions = append(spec.Functions, apispec.Function{
			Name:       "from_" + sanitize(filepath.Base(h)),
			QualName:   "from_" + sanitize(filepath.Base(h)),
			ReturnType: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"},
			SourceFile: h,
		})
	}
	return spec, nil
}

func (f *fixture) installSourceFileStub() *sourceFileStub {
	f.t.Helper()
	f.installStub(&clangStub{})
	stub := &sourceFileStub{}
	runBatchClang = stub.handler
	return stub
}

func (f *fixture) setCustomBridgeHeaders(headers ...string) {
	f.t.Helper()
	s, err := state.Load(f.root)
	if err != nil {
		f.t.Fatalf("load wasmify.json: %v", err)
	}
	s.Bridge = &state.BridgeConfig{CustomBridgeHeaders: headers}
	if err := state.Save(f.root, s); err != nil {
		f.t.Fatalf("save wasmify.json: %v", err)
	}
}

func (f *fixture) writeFile(rel, body string) string {
	f.t.Helper()
	path := filepath.Join(f.root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		f.t.Fatalf("write %s: %v", rel, err)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		f.t.Fatalf("eval symlinks: %v", err)
	}
	return real
}

func countInBatches(batches [][]string, header string) int {
	n := 0
	for _, b := range batches {
		for _, h := range b {
			if h == header {
				n++
			}
		}
	}
	return n
}

func TestParseHeaders_CustomBridgeHeaders_ParsedAlongsideDiscovered(t *testing.T) {
	f := newFixture(t, []string{"a.h", "b.h"}, nil, [][]int{{0, 1}})
	helper := f.writeFile("extra/helper.h", "int helper(void);\n")
	f.setCustomBridgeHeaders("extra/helper.h")
	stub := f.installSourceFileStub()

	if stderr, err := f.runParseHeaders(); err != nil {
		t.Fatalf("parse-headers: %v\nstderr:\n%s", err, stderr)
	}

	if got := countInBatches(stub.batches, helper); got != 1 {
		t.Fatalf("custom header appears in %d batches, want 1: %v", got, stub.batches)
	}
	spec, err := apispec.Load(f.root)
	if err != nil {
		t.Fatalf("load api-spec: %v", err)
	}
	var found *apispec.Function
	for i := range spec.Functions {
		if spec.Functions[i].Name == "from_helper_h" {
			found = &spec.Functions[i]
		}
	}
	if found == nil {
		t.Fatalf("api-spec has no declaration from the custom header: %+v", spec.Functions)
	}
	// Recorded as written in wasmify.json so the generated bridge includes
	// it portably; the bridge compile has -I of the wasmify.json dir.
	if found.SourceFile != "extra/helper.h" {
		t.Errorf("SourceFile = %q, want %q", found.SourceFile, "extra/helper.h")
	}
}

func TestParseHeaders_CustomBridgeHeaders_AlreadyDiscoveredNotDuplicated(t *testing.T) {
	f := newFixture(t, []string{"a.h", "b.h"}, nil, [][]int{{0}, {1}})
	f.setCustomBridgeHeaders("a.h")
	stub := f.installSourceFileStub()

	stderr, err := f.runParseHeaders()
	if err != nil {
		t.Fatalf("parse-headers: %v\nstderr:\n%s", err, stderr)
	}
	// The per-batch cache would hide a duplicate from the batch count, so
	// assert on the header list itself.
	if !strings.Contains(stderr, "Headers: 2 files") {
		t.Fatalf("want 2 headers in the list, stderr:\n%s", stderr)
	}
	realA, err := filepath.EvalSymlinks(f.headers["a.h"])
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	if got := countInBatches(stub.batches, realA); got != 1 {
		t.Fatalf("a.h appears in %d batches, want 1: %v", got, stub.batches)
	}
}

func TestParseHeaders_CustomBridgeHeaders_MissingFile(t *testing.T) {
	f := newFixture(t, []string{"a.h", "b.h"}, nil, [][]int{{0, 1}})
	f.setCustomBridgeHeaders("missing.h")
	f.installSourceFileStub()

	_, err := f.runParseHeaders()
	if err == nil || !strings.Contains(err.Error(), "missing.h") {
		t.Fatalf("err = %v, want an error naming missing.h", err)
	}
}

func TestParseHeaders_CustomBridgeHeaders_DroppedFromBatches(t *testing.T) {
	f := newFixture(t, []string{"a.h", "b.h"}, nil, [][]int{{0, 1}})
	f.writeFile("broken.h", "this does not compile\n")
	f.setCustomBridgeHeaders("broken.h")
	f.installSourceFileStub()
	// The production merge drops a header that does not compile even alone.
	mergeCompatibleGroupsFn = func(_ string, groups [][]string, _ []string) [][]string {
		return [][]string{groups[0]}
	}

	_, err := f.runParseHeaders()
	if err == nil || !strings.Contains(err.Error(), "broken.h") {
		t.Fatalf("err = %v, want an error naming broken.h", err)
	}
}

func TestParseHeaders_CustomBridgeHeaders_DroppedOnStaleGroupRetry(t *testing.T) {
	f := newFixture(t, []string{"a.h", "b.h"}, nil, [][]int{{0, 1}})
	helper := f.writeFile("helper.h", "int helper(void);\n")
	f.setCustomBridgeHeaders("helper.h")
	stub := f.installSourceFileStub()
	if stderr, err := f.runParseHeaders(); err != nil {
		t.Fatalf("warm-up: %v\nstderr:\n%s", err, stderr)
	}

	// The group cache keys on paths only, so breaking helper.h keeps the
	// cached batches; its batch then fails to parse and the retry
	// recomputes the batches, where the merge drops it.
	f.writeFile("helper.h", "this does not compile\n")
	runBatchClang = func(clangPath, umbrella string, batch []string, flags []string) (*apispec.APISpec, error) {
		for _, h := range batch {
			if h == helper {
				return nil, fmt.Errorf("simulated compile error")
			}
		}
		return stub.handler(clangPath, umbrella, batch, flags)
	}
	mergeCompatibleGroupsFn = func(_ string, groups [][]string, _ []string) [][]string {
		return [][]string{groups[0]}
	}

	stderr, err := f.runParseHeaders()
	if err == nil || !strings.Contains(err.Error(), "helper.h") {
		t.Fatalf("err = %v, want an error naming helper.h\nstderr:\n%s", err, stderr)
	}
}

func TestParseHeaders_CustomBridgeHeaders_WarnsWithExplicitHeader(t *testing.T) {
	f := newFixture(t, []string{"a.h", "b.h"}, nil, [][]int{{0, 1}})
	f.writeFile("helper.h", "int helper(void);\n")
	f.setCustomBridgeHeaders("helper.h")
	f.installSourceFileStub()

	// Only the warning matters here, not whether the explicit parse succeeds.
	stderr, _ := f.runParseHeaders("--header", f.headers["a.h"], "--header", f.headers["b.h"])
	if !strings.Contains(stderr, "CustomBridgeHeaders") {
		t.Fatalf("want a warning that CustomBridgeHeaders is ignored with --header, stderr:\n%s", stderr)
	}
}

func TestRelativizeCustomBridgeHeaders_SymlinkedSpelling(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "helper.h"), []byte("int helper(void);\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	custom, err := resolveCustomBridgeHeaders(dir, []string{"real/helper.h"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// clang reports the path the header was first included by.
	spec := &apispec.APISpec{Functions: []apispec.Function{{Name: "helper", SourceFile: filepath.Join(link, "helper.h")}}}

	relativizeCustomBridgeHeaders(spec, custom)

	if got := spec.Functions[0].SourceFile; got != "real/helper.h" {
		t.Errorf("SourceFile = %q, want %q", got, "real/helper.h")
	}
}
