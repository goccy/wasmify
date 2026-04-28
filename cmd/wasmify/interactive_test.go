package main

import (
	"testing"

	"github.com/goccy/wasmify/internal/state"
)

func TestStripNonInteractiveFlag(t *testing.T) {
	defer func(prev bool) { globalNonInteractive = prev }(globalNonInteractive)
	globalNonInteractive = false

	got := stripNonInteractiveFlag([]string{"build", "--non-interactive", "foo"})
	if !globalNonInteractive {
		t.Error("globalNonInteractive should be true after seeing --non-interactive")
	}
	if len(got) != 2 || got[0] != "build" || got[1] != "foo" {
		t.Errorf("stripped argv = %v, want [build foo]", got)
	}
}

func TestStripNonInteractiveFlag_Absent(t *testing.T) {
	defer func(prev bool) { globalNonInteractive = prev }(globalNonInteractive)
	globalNonInteractive = false

	got := stripNonInteractiveFlag([]string{"build", "--no-cache"})
	if globalNonInteractive {
		t.Error("globalNonInteractive should remain false")
	}
	if len(got) != 2 || got[0] != "build" || got[1] != "--no-cache" {
		t.Errorf("stripped argv = %v, want unchanged", got)
	}
}

func TestShouldDeployStubHeader(t *testing.T) {
	s := &state.State{
		Skip: &state.SkipConfig{
			DeployStubHeaders: []string{"sys/futex.h", "missing.h"},
		},
	}
	cases := []struct {
		header string
		want   bool
	}{
		{"sys/futex.h", true},
		{"missing.h", true},
		{"unknown.h", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := shouldDeployStubHeader(s, tc.header); got != tc.want {
			t.Errorf("shouldDeployStubHeader(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
	if shouldDeployStubHeader(nil, "anything.h") {
		t.Error("nil state should produce false")
	}
	if shouldDeployStubHeader(&state.State{}, "anything.h") {
		t.Error("state with no Skip should produce false")
	}
}

func TestShouldSkipFile(t *testing.T) {
	s := &state.State{
		Skip: &state.SkipConfig{
			Files: []state.SkipFile{
				{Path: "googlesql/foo.cc", Reason: "C++23"},
				{Path: "bar.cc"},
			},
		},
	}
	cases := []struct {
		path string
		want bool
	}{
		{"googlesql/foo.cc", true},                                              // exact
		{"some/prefix/googlesql/foo.cc", true},                                  // suffix match
		{"bazel-out/k8-opt/bin/external/abseil/some/path/bar.cc", true},         // suffix match
		{"foo.cc", false},                                                       // not "/foo.cc"
		{"unrelated.cc", false},
	}
	for _, tc := range cases {
		if got := shouldSkipFile(s, tc.path); got != tc.want {
			t.Errorf("shouldSkipFile(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
	if shouldSkipFile(nil, "foo.cc") {
		t.Error("nil state should produce false")
	}
}
