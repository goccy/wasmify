package wasmbuild

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinkLibrary_NoLibDir(t *testing.T) {
	tmp := t.TempDir()
	cfg := WasmConfig{
		WasiSDKPath: "/opt/wasi-sdk",
		Target:      "wasm32-wasip1",
		BuildDir:    tmp, // no lib/ subdir
	}
	_, err := LinkLibrary("mylib", cfg, nil)
	if err == nil {
		t.Error("expected error when lib dir missing")
	}
}

func TestLinkLibrary_NoArchives(t *testing.T) {
	tmp := t.TempDir()
	// Create empty lib dir
	libDir := filepath.Join(tmp, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := WasmConfig{
		WasiSDKPath: "/opt/wasi-sdk",
		Target:      "wasm32-wasip1",
		BuildDir:    tmp,
	}
	_, err := LinkLibrary("mylib", cfg, nil)
	if err == nil {
		t.Error("expected error for empty lib dir")
	}
}

// touch creates an empty file, making any missing parent directories.
func touch(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A project whose library is shipped already compiled replays no build step, so
// <BuildDir>/lib/ never exists. That must not be an error as long as the
// prebuilt archives carry the link.
func TestCollectArchives_PrebuiltOnly_NoLibDir(t *testing.T) {
	tmp := t.TempDir()
	ar := touch(t, filepath.Join(tmp, "deps", "libengine.a"))

	got, err := collectArchives(WasmConfig{
		BuildDir:         filepath.Join(tmp, "build"), // never created
		PrebuiltArchives: []string{ar},
	})
	if err != nil {
		t.Fatalf("collectArchives: %v", err)
	}
	if len(got) != 1 || got[0] != ar {
		t.Errorf("archives = %v, want [%s]", got, ar)
	}
}

// Prebuilt archives link AFTER the ones the replay produced, so a symbol the
// project builds from source wins over the same symbol in a vendored archive.
// Within the prebuilt list the declared order is preserved rather than sorted.
func TestCollectArchives_PrebuiltLinkAfterBuiltAndKeepOrder(t *testing.T) {
	tmp := t.TempDir()
	buildDir := filepath.Join(tmp, "build")
	// Deliberately named so basename sorting would interleave them with the
	// prebuilt entries if prebuilt archives were sorted in.
	zzz := touch(t, filepath.Join(buildDir, "lib", "zzz.a"))
	aaa := touch(t, filepath.Join(buildDir, "lib", "nested", "aaa.a"))
	// Declared in an order that is NOT sorted.
	second := touch(t, filepath.Join(tmp, "deps", "second.a"))
	first := touch(t, filepath.Join(tmp, "deps", "first.a"))

	got, err := collectArchives(WasmConfig{
		BuildDir:         buildDir,
		PrebuiltArchives: []string{second, first},
	})
	if err != nil {
		t.Fatalf("collectArchives: %v", err)
	}
	want := []string{aaa, zzz, second, first}
	if len(got) != len(want) {
		t.Fatalf("archives = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("archives[%d] = %s, want %s (full: %v)", i, got[i], want[i], got)
		}
	}
}

// A prebuilt archive that is declared but absent is a configuration error the
// user must see by path, not a silent "0 archives" or an opaque linker failure.
func TestCollectArchives_MissingPrebuilt(t *testing.T) {
	tmp := t.TempDir()
	touch(t, filepath.Join(tmp, "build", "lib", "real.a"))
	missing := filepath.Join(tmp, "deps", "absent.a")

	_, err := collectArchives(WasmConfig{
		BuildDir:         filepath.Join(tmp, "build"),
		PrebuiltArchives: []string{missing},
	})
	if err == nil {
		t.Fatal("expected an error for a missing prebuilt archive")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error %q does not name the missing archive %q", err, missing)
	}
	// The stat error must stay unwrappable so callers can distinguish "declared
	// but not fetched yet" from a genuine misconfiguration.
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error %q does not unwrap to fs.ErrNotExist", err)
	}
}
