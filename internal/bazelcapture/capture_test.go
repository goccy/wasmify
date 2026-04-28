package bazelcapture

import (
	"strings"
	"testing"
)

func TestToolFromAction(t *testing.T) {
	cases := []struct {
		name     string
		mnemonic string
		args     []string
		want     string
	}{
		{"cpp compile", "CppCompile", []string{"cc_wrapper.sh", "-c", "foo.cc"}, "clang++"},
		{"c compile", "CppCompile", []string{"cc_wrapper.sh", "-c", "foo.c"}, "clang"},
		{"mixed picks cpp", "CppCompile", []string{"cc_wrapper.sh", "-c", "foo.cc", "bar.c"}, "clang++"},
		{"archive", "CppArchive", []string{"ar", "rcs", "lib.a"}, "ar"},
		{"link", "CppLink", []string{"clang++", "-o", "bin"}, "clang++"},
		{"unrelated", "FileWrite", []string{"bash"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := toolFromAction(aqueryAction{Mnemonic: tc.mnemonic, Arguments: tc.args})
			if got != tc.want {
				t.Errorf("tool = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestToLogEntries(t *testing.T) {
	actions := []aqueryAction{
		{
			Mnemonic:  "CppCompile",
			Arguments: []string{"external/toolchain/bin/cc_wrapper.sh", "-c", "a.cc", "-o", "a.o"},
		},
		{
			Mnemonic:  "FileWrite",
			Arguments: []string{"bash", "write"},
		},
		{
			Mnemonic:  "CppArchive",
			Arguments: []string{"ar", "rcs", "lib.a", "a.o"},
		},
	}
	entries := toLogEntries(actions, "/execroot")
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (FileWrite should be dropped)", len(entries))
	}
	if entries[0].Tool != "clang++" || entries[0].Executable != "external/toolchain/bin/cc_wrapper.sh" {
		t.Errorf("compile entry wrong: %+v", entries[0])
	}
	if entries[0].WorkDir != "/execroot" {
		t.Errorf("workdir = %q, want /execroot", entries[0].WorkDir)
	}
	if len(entries[0].Args) != 4 {
		t.Errorf("compile args dropped arguments[0]? got %d args", len(entries[0].Args))
	}
	if entries[1].Tool != "ar" {
		t.Errorf("archive tool = %q, want ar", entries[1].Tool)
	}
}

func TestToLogEntries_SkipsEmpty(t *testing.T) {
	entries := toLogEntries([]aqueryAction{{Mnemonic: "CppCompile", Arguments: nil}}, "/x")
	if len(entries) != 0 {
		t.Errorf("empty arguments should be skipped")
	}
}

// TestFilterTargetActions ensures host/exec-config actions (like protoc and
// codegen tools that run during the build) are dropped so they are not
// compiled for wasm alongside the actual target libraries.
func TestFilterTargetActions(t *testing.T) {
	out := aqueryOutput{
		Configurations: []aqueryConfig{
			{ID: 1, Mnemonic: "darwin_arm64-opt"},                        // target
			{ID: 2, Mnemonic: "darwin_arm64-opt-exec-ST-abc", IsTool: true}, // host tool
		},
		Actions: []aqueryAction{
			{Mnemonic: "CppCompile", Arguments: []string{"clang++", "target.cc"}, ConfigurationID: 1},
			{Mnemonic: "CppCompile", Arguments: []string{"clang++", "protoc.cc"}, ConfigurationID: 2},
			{Mnemonic: "CppArchive", Arguments: []string{"ar", "libtarget.a"}, ConfigurationID: 1},
			{Mnemonic: "CppArchive", Arguments: []string{"ar", "libprotoc.a"}, ConfigurationID: 2},
		},
	}
	kept := filterTargetActions(out)
	if len(kept) != 2 {
		t.Fatalf("expected 2 target actions kept, got %d", len(kept))
	}
	for _, a := range kept {
		if a.ConfigurationID != 1 {
			t.Errorf("kept a host-config action: %+v", a)
		}
	}
}

// TestFilterTargetActions_DropsHeaderChecks verifies Bazel's
// process_headers syntax-only actions are excluded. Replaying them with
// wasi-sdk adds hundreds of redundant compiles and no library contribution.
func TestFilterTargetActions_DropsHeaderChecks(t *testing.T) {
	out := aqueryOutput{
		Configurations: []aqueryConfig{{ID: 1, Mnemonic: "target"}},
		Actions: []aqueryAction{
			{Mnemonic: "CppCompile", Arguments: []string{"clang++", "-c", "foo.cc", "-o", "foo.o"}, ConfigurationID: 1},
			{Mnemonic: "CppCompile", Arguments: []string{"clang++", "-xc++-header", "-fsyntax-only", "foo.h", "-o", "foo.h.processed"}, ConfigurationID: 1},
			{Mnemonic: "CppCompile", Arguments: []string{"clang++", "bar.cc", "-o", "bar.h.processed"}, ConfigurationID: 1},
		},
	}
	kept := filterTargetActions(out)
	if len(kept) != 1 {
		t.Fatalf("expected 1 action kept (the real .cc compile), got %d", len(kept))
	}
	if !strings.Contains(strings.Join(kept[0].Arguments, " "), "foo.cc") {
		t.Errorf("kept the wrong action: %+v", kept[0])
	}
}
