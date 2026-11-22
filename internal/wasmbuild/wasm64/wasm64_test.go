package wasm64

import (
	"os"
	"path/filepath"
	"testing"
)

// A sysroot is installed only when its compiler-rt builtins carry the stamp
// of the current recipe: builtins of an earlier recipe (whose soft-float
// comparisons return a long, which LLVM's wasm backend does not call them
// with) make the install incomplete, so the next wasm64 build rebuilds them.
func TestSysrootInstalledNeedsTheCurrentBuiltins(t *testing.T) {
	for _, c := range []struct {
		name    string
		threads bool
		triple  string
		rt      string
	}{
		{"wasm64-wasip1", false, "wasm64-wasip1", "wasm64-unknown-wasip1"},
		{"wasm64-wasip1-threads", true, "wasm64-wasip1-threads", "wasm64-unknown-wasip1-threads"},
	} {
		t.Run(c.name, func(t *testing.T) {
			sdk := t.TempDir()
			write := func(rel, content string) {
				t.Helper()
				path := filepath.Join(sdk, rel)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			lib := filepath.Join("share", "wasi-sysroot", "lib", c.triple)
			write(filepath.Join(lib, ".wasmify-libc-tag"), "8d8348ec24253d0638a693b8af82445c13d92d32\n")
			write(filepath.Join(lib, ".wasmify-cxx-tag"), "22.1.0\n")
			installed := func() bool {
				t.Helper()
				ok, err := SysrootInstalled(sdk, c.threads)
				if err != nil {
					t.Fatal(err)
				}
				return ok
			}
			if installed() {
				t.Fatal("installed without builtins")
			}
			stamp := filepath.Join("lib", "clang", "22", "lib", c.rt, ".wasmify-tag")
			write(stamp, "22.1.0\n")
			if installed() {
				t.Fatal("installed with builtins of the earlier recipe")
			}
			write(stamp, "22.1.0 "+BuiltinsRevision+"\n")
			if !installed() {
				t.Fatal("not installed with the current builtins")
			}

			// A stamp that cannot be read is an error, not an absent install.
			if err := os.Chmod(filepath.Join(sdk, stamp), 0); err != nil {
				t.Fatal(err)
			}
			if _, err := SysrootInstalled(sdk, c.threads); err == nil && os.Geteuid() != 0 {
				t.Error("an unreadable builtins stamp is not an error")
			}
		})
	}
}
