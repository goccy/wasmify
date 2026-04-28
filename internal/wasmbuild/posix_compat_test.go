package wasmbuild

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeployPosixCompat(t *testing.T) {
	tmp := t.TempDir()
	includeDir, err := DeployPosixCompat(tmp)
	if err != nil {
		t.Fatalf("DeployPosixCompat failed: %v", err)
	}
	expected := filepath.Join(tmp, "posix-compat", "include")
	if includeDir != expected {
		t.Errorf("got include dir %q, want %q", includeDir, expected)
	}
	// Verify known embedded headers deployed
	for _, header := range []string{
		"sys/socket.h",
		"netdb.h",
		"signal.h",
		"netinet/in.h",
		"time.h",
	} {
		path := filepath.Join(includeDir, header)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected deployed header %q: %v", header, err)
		}
	}
}

func TestLookupHeaderStub(t *testing.T) {
	// Known header
	stub, ok := LookupHeaderStub("grp.h")
	if !ok {
		t.Error("expected grp.h to be registered")
	}
	if stub.Content == "" {
		t.Error("expected non-empty content")
	}
	if stub.Description == "" {
		t.Error("expected non-empty description")
	}
	// Unknown header
	_, ok = LookupHeaderStub("nonexistent-header.h")
	if ok {
		t.Error("expected unknown header to miss")
	}
}

func TestDeployStubHeader(t *testing.T) {
	tmp := t.TempDir()
	err := DeployStubHeader(tmp, "foo/bar.h", "// test stub")
	if err != nil {
		t.Fatalf("DeployStubHeader failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(tmp, "foo", "bar.h"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "// test stub" {
		t.Errorf("content mismatch: %q", data)
	}
}

func TestDetectMissingHeaders(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   []string
	}{
		{
			name:   "one missing",
			stderr: "foo.cc:1:10: fatal error: 'grp.h' file not found",
			want:   []string{"grp.h"},
		},
		{
			name:   "multiple unique",
			stderr: "foo.cc:1: fatal error: 'a.h' file not found\nbar.cc:2: fatal error: 'b.h' file not found",
			want:   []string{"a.h", "b.h"},
		},
		{
			name:   "duplicate deduped",
			stderr: "fatal error: 'grp.h' file not found\nfatal error: 'grp.h' file not found",
			want:   []string{"grp.h"},
		},
		{
			name:   "none",
			stderr: "no errors here",
			want:   nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectMissingHeaders(tt.stderr)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i, w := range tt.want {
				if got[i] != w {
					t.Errorf("idx %d: got %q, want %q", i, got[i], w)
				}
			}
		})
	}
}

// Optional: smoke test that the embedded signal.h contains useful content
func TestDeployPosixCompat_ContentValid(t *testing.T) {
	tmp := t.TempDir()
	includeDir, err := DeployPosixCompat(tmp)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(includeDir, "signal.h"))
	if err != nil {
		t.Fatal(err)
	}
	// Simple sanity check - signal.h should declare SIGKILL or sigaction
	text := string(data)
	if !strings.Contains(text, "SIG") {
		t.Errorf("signal.h should contain SIG* defs, got first 200 bytes: %s",
			text[:min(200, len(text))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
