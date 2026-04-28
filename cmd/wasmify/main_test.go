package main

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/goccy/wasmify/internal/buildjson"
)

func TestScanProjectHeaders(t *testing.T) {
	// Create a temp project structure
	root := t.TempDir()

	// Create header files in various directories
	dirs := []string{
		"public",
		"analyzer",
		"resolved_ast",
		"internal/impl",
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	expectedHeaders := []string{
		filepath.Join(root, "public/api.h"),
		filepath.Join(root, "analyzer/analyzer.h"),
		filepath.Join(root, "resolved_ast/resolved_statement.h"),
		filepath.Join(root, "internal/impl/detail.hpp"),
	}
	for _, h := range expectedHeaders {
		if err := os.WriteFile(h, []byte("// header\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Create a non-header file that should be ignored
	_ = os.WriteFile(filepath.Join(root, "public/main.cc"), []byte("int main(){}"), 0o644)

	// Create a hidden directory that should be skipped
	_ = os.MkdirAll(filepath.Join(root, ".git/refs"), 0o755)
	_ = os.WriteFile(filepath.Join(root, ".git/config.h"), []byte("// hidden\n"), 0o644)

	headers := scanProjectHeaders(root)

	if len(headers) != len(expectedHeaders) {
		t.Errorf("expected %d headers, got %d", len(expectedHeaders), len(headers))
		for h := range headers {
			t.Logf("  found: %s", h)
		}
	}

	for _, h := range expectedHeaders {
		real, _ := filepath.EvalSymlinks(h)
		if !headers[real] {
			t.Errorf("expected header not found: %s", h)
		}
	}
}

func TestScanProjectHeaders_FollowsSymlinks(t *testing.T) {
	root := t.TempDir()

	// Create a "bazel-out" like structure with symlinks
	// Use a separate temp dir for the real output to simulate bazel's layout
	realDir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(realDir, "genfiles"), 0o755)
	_ = os.WriteFile(filepath.Join(realDir, "genfiles/generated.h"), []byte("// generated\n"), 0o644)

	// Create symlink bazel-out -> realDir (outside project)
	_ = os.Symlink(realDir, filepath.Join(root, "bazel-out"))

	// Create "external" symlink that should NOT be followed
	// extDir is outside project root, only accessible via symlink
	extDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(extDir, "third_party.h"), []byte("// external\n"), 0o644)
	_ = os.Symlink(extDir, filepath.Join(realDir, "external"))

	headers := scanProjectHeaders(root)

	// generated.h should be found (under bazel-out which is a symlink)
	genReal, _ := filepath.EvalSymlinks(filepath.Join(root, "bazel-out/genfiles/generated.h"))
	if !headers[genReal] {
		t.Errorf("expected generated.h to be found via symlink")
		for h := range headers {
			t.Logf("  found: %s", h)
		}
	}

	// third_party.h should NOT be found (under external/ which is excluded)
	extReal, _ := filepath.EvalSymlinks(filepath.Join(root, "bazel-out/external/third_party.h"))
	if headers[extReal] {
		t.Errorf("external header should not be included via symlink: %s", extReal)
	}
}

func TestScanProjectHeaders_ExternalInsideProject(t *testing.T) {
	root := t.TempDir()

	// A real external/ directory inside the project root should NOT be skipped
	// (only external/ reached via symlinks outside the project is excluded)
	_ = os.MkdirAll(filepath.Join(root, "external/mylib"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "external/mylib/api.h"), []byte("// mylib\n"), 0o644)

	headers := scanProjectHeaders(root)

	extReal, _ := filepath.EvalSymlinks(filepath.Join(root, "external/mylib/api.h"))
	if !headers[extReal] {
		t.Errorf("external/ inside project root should be included, but was not: %s", extReal)
	}
}

func TestScanProjectHeaders_ExternalOutsideProject(t *testing.T) {
	root := t.TempDir()

	// Simulate Bazel execroot outside the project root
	execroot := t.TempDir()
	_ = os.MkdirAll(filepath.Join(execroot, "googlesql/public"), 0o755)
	_ = os.WriteFile(filepath.Join(execroot, "googlesql/public/analyzer.h"), []byte("// analyzer\n"), 0o644)
	// Bazel places third-party deps at <execroot>/external/
	_ = os.MkdirAll(filepath.Join(execroot, "external/icu"), 0o755)
	_ = os.WriteFile(filepath.Join(execroot, "external/icu/unicode.h"), []byte("// icu\n"), 0o644)

	// Create a project header
	_ = os.MkdirAll(filepath.Join(root, "src"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "src/main.h"), []byte("// main\n"), 0o644)

	// Symlink bazel-workspace -> execroot (outside project root)
	_ = os.Symlink(execroot, filepath.Join(root, "bazel-workspace"))

	headers := scanProjectHeaders(root)

	// Project header should be found
	srcReal, _ := filepath.EvalSymlinks(filepath.Join(root, "src/main.h"))
	if !headers[srcReal] {
		t.Errorf("expected src/main.h to be found")
	}

	// Generated header inside execroot (via symlink) should be found
	analyzerReal, _ := filepath.EvalSymlinks(filepath.Join(root, "bazel-workspace/googlesql/public/analyzer.h"))
	if !headers[analyzerReal] {
		t.Errorf("expected analyzer.h to be found via bazel symlink")
	}

	// external/ inside execroot should be excluded (Bazel third-party)
	icuReal, _ := filepath.EvalSymlinks(filepath.Join(root, "bazel-workspace/external/icu/unicode.h"))
	if headers[icuReal] {
		t.Errorf("external/icu/unicode.h via symlink outside project should not be included")
	}
}

func TestDiscoverProjectHeadersFromBuild(t *testing.T) {
	root := t.TempDir()

	// Create project headers
	_ = os.MkdirAll(filepath.Join(root, "public"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "resolved_ast"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "public/api.h"), []byte("// api\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "resolved_ast/resolved_stmt.h"), []byte("// resolved\n"), 0o644)

	// Create a .d file
	depDir := filepath.Join(root, "build_out")
	_ = os.MkdirAll(depDir, 0o755)

	// The .d file references both project headers and system headers
	depContent := filepath.Join(root, "build_out/foo.o") + ": " +
		filepath.Join(root, "public/api.h") + " " +
		filepath.Join(root, "resolved_ast/resolved_stmt.h") + " " +
		"/usr/include/stdio.h\n"
	depFile := filepath.Join(depDir, "foo.d")
	_ = os.WriteFile(depFile, []byte(depContent), 0o644)

	// Build a minimal BuildJSON
	b := &buildjson.BuildJSON{
		Steps: []buildjson.BuildStep{
			{
				Type:    buildjson.StepCompile,
				WorkDir: root,
				Args:    []string{"gcc", "-c", "foo.cc", "-MF", depFile},
			},
		},
	}

	// Scan project headers
	projectHeaders := scanProjectHeaders(root)

	// Discover headers from build
	headers, groups := discoverProjectHeadersFromBuild(root, b, projectHeaders)

	// Should have one group (one .d file)
	if len(groups) != 1 {
		t.Errorf("expected 1 group, got %d", len(groups))
	}

	// Should find both project headers but NOT /usr/include/stdio.h
	sort.Strings(headers)
	if len(headers) != 2 {
		t.Errorf("expected 2 headers, got %d", len(headers))
		for _, h := range headers {
			t.Logf("  found: %s", h)
		}
	}

	// Verify system headers are excluded
	for _, h := range headers {
		if h == "/usr/include/stdio.h" {
			t.Errorf("system header should not be included")
		}
	}
}
