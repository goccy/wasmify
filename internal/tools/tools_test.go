package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDetect_ByName(t *testing.T) {
	// `sh` must exist on every posix system we run tests on.
	if !Detect(Tool{Name: "sh"}) {
		t.Fatal("expected sh to be detected")
	}
	if Detect(Tool{Name: "definitely-not-a-real-binary-xyz"}) {
		t.Fatal("bogus binary should not be detected")
	}
}

func TestDetect_ByCommand(t *testing.T) {
	if !Detect(Tool{Name: "anything", DetectCmd: "true"}) {
		t.Fatal("DetectCmd=true should succeed")
	}
	if Detect(Tool{Name: "anything", DetectCmd: "false"}) {
		t.Fatal("DetectCmd=false should fail detection")
	}
}

func TestCurrentOS(t *testing.T) {
	got := CurrentOS()
	switch runtime.GOOS {
	case "darwin":
		if got != OSDarwin {
			t.Fatalf("expected darwin, got %q", got)
		}
	case "linux":
		// debian/ubuntu → OSDebian, other distros → empty. Either is acceptable.
	}
}

func TestInstall_UnsupportedOS(t *testing.T) {
	// Tool with no recipe for current OS → error path.
	err := Install(Tool{Name: "x", Install: map[OS]InstallSpec{}})
	if err == nil {
		t.Fatal("expected error for missing recipe")
	}
}

func TestInstall_RunsCommands(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "done")
	osID := CurrentOS()
	if osID == "" {
		t.Skip("no supported OS detected; skipping")
	}
	tool := Tool{
		Name: "fake",
		Install: map[OS]InstallSpec{
			osID: {Commands: []string{"touch " + marker}},
		},
	}
	if err := Install(tool); err != nil {
		t.Fatalf("Install failed: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("install command did not run: %v", err)
	}
}

func TestEnsure_SkipsIfPresent(t *testing.T) {
	// `sh` is present; Install would fail (no recipe) — if Ensure tried to run
	// it the test would fail.
	if err := Ensure(Tool{Name: "sh"}); err != nil {
		t.Fatalf("Ensure returned error for already-present tool: %v", err)
	}
}

func TestCatalog_HasCoreTools(t *testing.T) {
	c := Catalog()
	for _, name := range []string{"cmake", "ninja", "bazel", "autoconf", "pkg-config"} {
		if _, ok := c[name]; !ok {
			t.Errorf("catalog missing %q", name)
		}
	}
	bz := c["bazel"]
	if _, ok := bz.Install[OSDarwin]; !ok {
		t.Error("bazel missing darwin recipe")
	}
	if _, ok := bz.Install[OSDebian]; !ok {
		t.Error("bazel missing debian recipe")
	}
}

func TestLookup(t *testing.T) {
	if _, ok := Lookup("cmake"); !ok {
		t.Fatal("cmake should be in catalog")
	}
	if _, ok := Lookup("does-not-exist"); ok {
		t.Fatal("unknown tool returned ok=true")
	}
}
