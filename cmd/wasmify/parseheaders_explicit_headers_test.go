package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/goccy/wasmify/internal/apispec"
	"github.com/goccy/wasmify/internal/state"
)

// batchRecorder is a runBatchClang stand-in that records each batch and
// the ExternalTypes allow-list in effect when it was parsed.
type batchRecorder struct {
	batches [][]string
	allowed [][]string
}

func (r *batchRecorder) handler(_ string, _ string, batch []string, _ []string) (*apispec.APISpec, error) {
	r.batches = append(r.batches, append([]string(nil), batch...))
	r.allowed = append(r.allowed, append([]string(nil), runBatchAllowedExternalClasses...))
	spec := &apispec.APISpec{}
	for _, h := range batch {
		name := "from_" + sanitize(filepath.Base(h))
		spec.Functions = append(spec.Functions, apispec.Function{
			Name:       name,
			QualName:   name,
			ReturnType: apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int"},
		})
	}
	return spec, nil
}

func (f *fixture) installBatchRecorder() *batchRecorder {
	f.t.Helper()
	f.installStub(&clangStub{})
	r := &batchRecorder{}
	runBatchClang = r.handler
	return r
}

func (f *fixture) setExternalTypes(types ...string) {
	f.t.Helper()
	s, err := state.Load(f.root)
	if err != nil {
		f.t.Fatalf("load wasmify.json: %v", err)
	}
	s.Bridge = &state.BridgeConfig{ExternalTypes: types}
	if err := state.Save(f.root, s); err != nil {
		f.t.Fatalf("save wasmify.json: %v", err)
	}
}

func TestParseHeaders_MultipleExplicitHeaders_ParsedTogether(t *testing.T) {
	f := newFixture(t, []string{"a.h", "b.h", "c.h"}, nil, [][]int{{0}, {1}, {2}})
	rec := f.installBatchRecorder()

	// Relative, as typed on the command line: the umbrella header lives in
	// a temp dir, so they must reach it as absolute paths.
	if stderr, err := f.runParseHeaders("--header", "a.h", "--header", "b.h"); err != nil {
		t.Fatalf("parse-headers: %v\nstderr:\n%s", err, stderr)
	}

	// filepath.Abs resolves against the real cwd, which differs from
	// t.TempDir() where the temp dir sits behind a symlink (macOS /var).
	root, err := filepath.EvalSymlinks(f.root)
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	want := [][]string{{filepath.Join(root, "a.h"), filepath.Join(root, "b.h")}}
	if !slices.EqualFunc(rec.batches, want, slices.Equal) {
		t.Errorf("batches = %v, want %v", rec.batches, want)
	}
	spec, err := apispec.Load(f.root)
	if err != nil {
		t.Fatalf("load api-spec: %v", err)
	}
	if len(spec.Functions) != 2 {
		t.Errorf("api-spec has %d functions, want 2: %+v", len(spec.Functions), spec.Functions)
	}
}

func TestParseHeaders_ExplicitHeaders_ApplyExternalTypes(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"one header", []string{"--header", "a.h"}},
		{"two headers", []string{"--header", "a.h", "--header", "b.h"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, []string{"a.h", "b.h"}, nil, [][]int{{0, 1}})
			f.setExternalTypes("ext::Thing")
			rec := f.installBatchRecorder()

			if stderr, err := f.runParseHeaders(tc.args...); err != nil {
				t.Fatalf("parse-headers: %v\nstderr:\n%s", err, stderr)
			}
			if len(rec.allowed) == 0 {
				t.Fatal("no batch was parsed")
			}
			for i, allowed := range rec.allowed {
				if !slices.Equal(allowed, []string{"ext::Thing"}) {
					t.Errorf("batch %d parsed with allowed external classes %v, want [ext::Thing]", i, allowed)
				}
			}
		})
	}
}

func TestParseHeaders_ExplicitHeaderDroppedByMerge_Errors(t *testing.T) {
	f := newFixture(t, []string{"a.h", "b.h"}, nil, [][]int{{0, 1}})
	f.installBatchRecorder()
	// What the umbrella merge does with a header that does not compile
	// on its own: leaves it out of every batch.
	mergeCompatibleGroupsFn = func(_ string, groups [][]string, _ []string) [][]string {
		var out [][]string
		for _, g := range groups {
			var kept []string
			for _, h := range g {
				if filepath.Base(h) != "b.h" {
					kept = append(kept, h)
				}
			}
			out = append(out, kept)
		}
		return out
	}

	stderr, err := f.runParseHeaders("--header", "a.h", "--header", "b.h")
	if err == nil {
		t.Fatalf("parse-headers succeeded without b.h; want an error naming it\nstderr:\n%s", stderr)
	}
	if !strings.Contains(err.Error(), "b.h") {
		t.Errorf("error %q does not name b.h", err)
	}
}

func TestParseHeaders_ExplicitHeaders_KeepDiscoveryCache(t *testing.T) {
	f := newFixture(t, []string{"a.h", "b.h", "c.h"}, nil, [][]int{{0}, {1}, {2}})
	f.installBatchRecorder()

	if stderr, err := f.runParseHeaders(); err != nil {
		t.Fatalf("discovery run: %v\nstderr:\n%s", err, stderr)
	}
	if stderr, err := f.runParseHeaders("--header", "a.h", "--header", "b.h"); err != nil {
		t.Fatalf("--header run: %v\nstderr:\n%s", err, stderr)
	}
	stderr, err := f.runParseHeaders()
	if err != nil {
		t.Fatalf("second discovery run: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "group cache hit") {
		t.Errorf("second discovery run missed the group cache\nstderr:\n%s", stderr)
	}
	if hit, miss := parseCacheStats(t, stderr); hit != 3 || miss != 0 {
		t.Errorf("second discovery run: batch cache %d hit, %d miss; want 3 hit, 0 miss", hit, miss)
	}
}
