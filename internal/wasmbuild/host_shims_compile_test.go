package wasmbuild

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/wasmify/internal/hostcap"
	"github.com/goccy/wasmify/internal/wasmbuild/wasm64"
)

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
