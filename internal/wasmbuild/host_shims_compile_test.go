package wasmbuild

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/goccy/wasm2go/transpile"
	"github.com/goccy/wasmify/internal/hostcap"
	"github.com/goccy/wasmify/internal/wasmbuild/wasm64"
)

// hostImportDecl is an import of wasmify_posix.h: the import's name, then
// the C function declared for it.
var hostImportDecl = regexp.MustCompile(`WASMIFY_POSIX_IMPORT\((\w+)\)\s*\n[^(;]*?\b(__wasmify_host_\w+)\(`)

// Every import wasmify_posix.h declares is one the wasm2go runtime serves:
// a program that refers to each is built for both pointer widths (wasm64
// where its sysroot is installed), its import table has each under
// wasip1_posix_extension, and wasm2go translates it, which it refuses for
// a name of that module the runtime does not have. Skipped where no
// wasi-sdk is installed.
func TestHostImportsAreServedByWasm2go(t *testing.T) {
	sdk, err := DetectWasiSDK("")
	if err != nil {
		t.Skipf("no wasi-sdk: %v", err)
	}
	decls := hostImportDecl.FindAllStringSubmatch(wasmifyPosixHeader, -1)
	if len(decls) == 0 || len(decls) != strings.Count(wasmifyPosixHeader, "WASMIFY_POSIX_IMPORT(")-1 {
		t.Fatalf("found %d import declarations in wasmify_posix.h, want every WASMIFY_POSIX_IMPORT but the macro's own", len(decls))
	}
	var program strings.Builder
	program.WriteString("#include \"wasmify_posix.h\"\n__attribute__((used)) void *const refs[] = {\n")
	for _, d := range decls {
		fmt.Fprintf(&program, "    (void *)&%s,\n", d[2])
	}
	program.WriteString("};\nint main(void) { return refs[0] == 0; }\n")
	for _, wasm64Build := range []bool{false, true} {
		width := "wasm32"
		if wasm64Build {
			width = "wasm64"
		}
		t.Run(width, func(t *testing.T) {
			if wasm64Build {
				installed, err := wasm64.SysrootInstalled(sdk, false)
				if err != nil {
					t.Fatal(err)
				}
				if !installed {
					t.Skip("no wasm64 sysroot")
				}
			}
			dir := t.TempDir()
			main := filepath.Join(dir, "main.c")
			if err := os.WriteFile(main, []byte(program.String()), 0o644); err != nil {
				t.Fatal(err)
			}
			wasmBin, err := os.ReadFile(buildHostProgram(t, sdk, dir, hostWith(hostcap.Filesystem), wasm64Build, main))
			if err != nil {
				t.Fatal(err)
			}
			m, err := transpile.Parse(bytes.NewReader(wasmBin))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			imported := map[string]bool{}
			for _, i := range m.Imports {
				if i.Module == "wasip1_posix_extension" {
					imported[i.Name] = true
				}
			}
			for _, d := range decls {
				if !imported[d[1]] {
					t.Errorf("%s is not imported from wasip1_posix_extension", d[1])
				}
			}
			var out bytes.Buffer
			if _, err := transpile.Translate(&out, m, transpile.Options{Package: "guest", OutputImportPath: "hostimports/guest", PureOnly: true}); err != nil {
				t.Errorf("wasm2go does not serve the imports: %v", err)
			}
		})
	}
}

// Each part of the capability tree, given alone, builds
// for both pointer widths (wasm64 where its sysroot is installed) under the
// flags wasm-build gives it: its shims
// compile (with no unused function, so every section is guarded by the part
// it serves) and link into a program with nothing left undefined (so no shim
// calls into a part that is not given). Skipped where no wasi-sdk is
// installed.
func TestHostPartsBuildAlone(t *testing.T) {
	sdk, err := DetectWasiSDK("")
	if err != nil {
		t.Skipf("no wasi-sdk: %v", err)
	}
	for _, wasm64Build := range []bool{false, true} {
		width := "wasm32"
		if wasm64Build {
			width = "wasm64"
		}
		for _, p := range hostParts {
			name := partName(p)
			host := hostWith(name)
			wasm64Build := wasm64Build
			t.Run(name+"/"+width, func(t *testing.T) {
				if wasm64Build {
					installed, err := wasm64.SysrootInstalled(sdk, host.Feature(hostcap.Process, "threads"))
					if err != nil {
						t.Fatal(err)
					}
					if !installed {
						t.Skip("no wasm64 sysroot")
					}
				}
				t.Parallel()
				dir := t.TempDir()
				main := filepath.Join(dir, "main.c")
				if err := os.WriteFile(main, []byte("int main(void) { return 0; }\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				buildHostProgram(t, sdk, dir, host, wasm64Build, main)
			})
		}
	}
}

// buildHostProgram compiles the C program src and the shims of the host
// capabilities with the flags wasm-build uses, and links them into a
// command module, which it returns. Nothing is left undefined: a function
// the shims call that no given part provides is a link error, not an
// import. The shims compile with unused functions as errors.
func buildHostProgram(t *testing.T, sdk, dir string, host hostcap.Set, wasm64 bool, src string) string {
	t.Helper()
	buildDir := filepath.Join(dir, "build")
	compat, err := DeployPosixCompat(buildDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := WasmConfig{
		Target:         "wasm32-wasip1",
		Wasm64:         wasm64,
		WasiSDKPath:    sdk,
		PosixCompatDir: compat,
		Host:           host,
	}
	if cfg.HostIncludeDir, err = DeployHostHeaders(buildDir, host); err != nil {
		t.Fatal(err)
	}
	shims, err := DeployHostShims(buildDir, host)
	if err != nil {
		t.Fatal(err)
	}
	run := func(tool string, args ...string) {
		t.Helper()
		out, err := exec.Command(filepath.Join(sdk, "bin", tool), args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s: %v\n%s", tool, strings.Join(args, " "), err, out)
		}
	}
	flags := append(wasmCompileFlags(cfg), "-fno-exceptions")
	var objs []string
	for _, shim := range shims {
		obj := filepath.Join(buildDir, filepath.Base(shim)+".o")
		run("clang++", append(append([]string{"-c", "-std=c++20", "-Werror=unused-function"}, flags...), "-o", obj, shim)...)
		objs = append(objs, obj)
	}
	prog := filepath.Join(buildDir, "program.o")
	run("clang", append(append([]string{"-c", "-Wno-deprecated-declarations",
		"-I", filepath.Join(buildDir, "host-shims")}, flags...), "-o", prog, src)...)
	wasm := filepath.Join(dir, "program.wasm")
	link := []string{"--target=" + effectiveTarget(cfg), "--sysroot=" + Sysroot(sdk), "-fno-exceptions",
		"-o", wasm, prog}
	link = append(link, objs...)
	link = append(link, cfg.EffectiveExtraLDFlags()...)
	link = append(link, "-lwasi-emulated-process-clocks")
	if cfg.Threads() {
		// the shared memory of a threads build, as the wasm-build link has it
		link = append(link, "-pthread", "-Wl,--shared-memory", "-Wl,--export-memory",
			"-Wl,--max-memory=67108864", "-Wl,--export=wasi_thread_start")
	}
	if wasm64 {
		// wasi-sdk's libc++ for wasm64 is built with exception handling
		// and refers to the tag of C++ exceptions without defining it.
		tagSrc, err := filepath.Abs(filepath.Join("testdata", "cpp_exception_tag_wasm64.s"))
		if err != nil {
			t.Fatal(err)
		}
		tag := filepath.Join(buildDir, "cpp_exception_tag.o")
		run("clang", "-c", "--target="+effectiveTarget(cfg), "-mexception-handling", "-o", tag, tagSrc)
		link = append(link, tag)
	}
	run("clang++", link...)
	return wasm
}

// A program that calls every exec function, built with process.spawn for
// both pointer widths (wasm64 where its sysroot is installed), links them
// from the subprocess shim and imports proc_exec. Skipped where no
// wasi-sdk is installed.
func TestHostExecFamilyLinks(t *testing.T) {
	sdk, err := DetectWasiSDK("")
	if err != nil {
		t.Skipf("no wasi-sdk: %v", err)
	}
	const program = `#include <unistd.h>
extern char **environ;
int main(int argc, char **argv) {
    char *const args[] = {"sh", "-c", "exit 0", 0};
    switch (argc) {
    case 1: return execl("/bin/sh", "sh", "-c", "exit 0", (char *)0);
    case 2: return execlp("sh", "sh", "-c", "exit 0", (char *)0);
    case 3: return execle("/bin/sh", "sh", "-c", "exit 0", (char *)0, environ);
    case 4: return execve("/bin/sh", args, environ);
    case 5: return execv("/bin/sh", args);
    default: return execvp(argv[0], args);
    }
}
`
	for _, wasm64Build := range []bool{false, true} {
		width := "wasm32"
		if wasm64Build {
			width = "wasm64"
		}
		t.Run(width, func(t *testing.T) {
			if wasm64Build {
				installed, err := wasm64.SysrootInstalled(sdk, false)
				if err != nil {
					t.Fatal(err)
				}
				if !installed {
					t.Skip("no wasm64 sysroot")
				}
			}
			dir := t.TempDir()
			main := filepath.Join(dir, "main.c")
			if err := os.WriteFile(main, []byte(program), 0o644); err != nil {
				t.Fatal(err)
			}
			wasmBin, err := os.ReadFile(buildHostProgram(t, sdk, dir, hostWith("process.spawn"), wasm64Build, main))
			if err != nil {
				t.Fatal(err)
			}
			m, err := transpile.Parse(bytes.NewReader(wasmBin))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			imported := false
			for _, i := range m.Imports {
				if i.Module == "wasip1_posix_extension" && i.Name == "proc_exec" {
					imported = true
				}
			}
			if !imported {
				t.Error("proc_exec is not imported")
			}
		})
	}
}

// A program that forks with __wasmify_fork, built with process.fork for
// both pointer widths (with and without signals), exports the child's
// entry __wasip1_posix_extension_fork_child as a function () -> i32 and imports proc_fork.
// Skipped where no wasi-sdk is installed.
func TestHostForkChildExport(t *testing.T) {
	sdk, err := DetectWasiSDK("")
	if err != nil {
		t.Skipf("no wasi-sdk: %v", err)
	}
	const program = `#include "wasmify_posix.h"
static int child(void *arg) { return *(int *)arg; }
int main(void) {
    static int status = 7;
    return __wasmify_fork(child, &status) < 0;
}
`
	for _, wasm64 := range []bool{false, true} {
		for _, parts := range [][]string{
			{"process.fork", "process.spawn"},
			{"process.fork", "process.spawn", "signals"},
		} {
			width := "wasm32"
			if wasm64 {
				width = "wasm64"
			}
			t.Run(strings.Join(parts, "+")+"/"+width, func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				main := filepath.Join(dir, "main.c")
				if err := os.WriteFile(main, []byte(program), 0o644); err != nil {
					t.Fatal(err)
				}
				wasmBin, err := os.ReadFile(buildHostProgram(t, sdk, dir, hostWith(parts...), wasm64, main))
				if err != nil {
					t.Fatal(err)
				}
				m, err := transpile.Parse(bytes.NewReader(wasmBin))
				if err != nil {
					t.Fatalf("parse: %v", err)
				}
				exported := false
				for _, e := range m.Exports {
					if e.Name != "__wasip1_posix_extension_fork_child" {
						continue
					}
					exported = true
					if e.Kind != 0 {
						t.Fatalf("__wasip1_posix_extension_fork_child is exported with kind %d, want a function", e.Kind)
					}
					ft := m.FuncTypeOf(e.Index)
					if len(ft.Params) != 0 || len(ft.Results) != 1 || ft.Results[0] != 0x7f {
						t.Errorf("__wasip1_posix_extension_fork_child has params %v and results %v, want () -> i32", ft.Params, ft.Results)
					}
				}
				if !exported {
					t.Error("__wasip1_posix_extension_fork_child is not exported")
				}
				imported := false
				for _, i := range m.Imports {
					if i.Module == "wasip1_posix_extension" && i.Name == "proc_fork" {
						imported = true
					}
				}
				if !imported {
					t.Error("proc_fork is not imported")
				}
			})
		}
	}
}
