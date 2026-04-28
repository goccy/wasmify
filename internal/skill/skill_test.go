package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContent_HasFrontmatter(t *testing.T) {
	if !strings.HasPrefix(Content, "---\nname: wasmify") {
		t.Fatalf("SKILL.md missing expected frontmatter. First 40 chars: %q", Content[:40])
	}
}

func TestInstall_WritesFile(t *testing.T) {
	root := t.TempDir()
	path, err := Install(root, false)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	want := filepath.Join(root, ".claude", "skills", "wasmify", "SKILL.md")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != Content {
		t.Fatal("written content does not match embedded content")
	}
}

func TestInstall_PreservesByDefault(t *testing.T) {
	root := t.TempDir()
	path, err := Install(root, false)
	if err != nil {
		t.Fatal(err)
	}
	// Modify the file; Install without --force should keep our edit.
	if err := os.WriteFile(path, []byte("EDITED"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(root, false); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "EDITED" {
		t.Fatal("Install(force=false) overwrote existing file")
	}
}

func TestInstall_ForceOverwrites(t *testing.T) {
	root := t.TempDir()
	path, _ := Install(root, false)
	_ = os.WriteFile(path, []byte("EDITED"), 0o644)
	if _, err := Install(root, true); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) == "EDITED" {
		t.Fatal("Install(force=true) did not overwrite")
	}
}
