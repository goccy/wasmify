package wasmbuild

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/goccy/wasm2go/transpile"
	"github.com/goccy/wasmify/internal/hostcap"
	"github.com/goccy/wasmify/internal/wasmbuild/wasm64"
)

// testdata/host_shims_run.c, built with every host capability for both
// pointer widths (wasm64 where its sysroot is installed, and without
// process.threads, whose wasm64 sysroot is another), transpiled by
// wasm2go and run once per mode, prints what each mode expects:
//   - signals: a signal that comes while blocked is delivered once when
//     sigprocmask, sigsuspend or a wait's own mask lets it in, and sigset_t
//     holds any set (64 bits);
//   - nosignal: MSG_NOSIGNAL keeps the SIGPIPE of a broken connection from
//     the program, and sendto delivers it before it returns otherwise;
//   - cloexec: a command does not inherit what O_CLOEXEC and SOCK_CLOEXEC
//     made, and inherits the rest (the guest's descriptor numbers being the
//     host's);
//   - waitgroup: waitpid(0) reaps a child of the caller's group, and a
//     group with no child is ECHILD.
//
// Skipped where no wasi-sdk is installed.
func TestHostShimsRun(t *testing.T) {
	sdk, err := DetectWasiSDK("")
	if err != nil {
		t.Skipf("no wasi-sdk: %v", err)
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skipf("no /bin/sh for a command: %v", err)
	}
	for _, wasm64Build := range []bool{false, true} {
		width := "wasm32"
		var host hostcap.Set
		host.EnableAll()
		if wasm64Build {
			width = "wasm64"
			var parts []string
			for _, p := range hostParts {
				if name := partName(p); name != "process.threads" {
					parts = append(parts, name)
				}
			}
			host = hostWith(parts...)
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
			src, err := filepath.Abs(filepath.Join("testdata", "host_shims_run.c"))
			if err != nil {
				t.Fatal(err)
			}
			runner := transpileRunner(t, buildHostProgram(t, sdk, dir, host, wasm64Build, src), dir)
			for _, tt := range []struct {
				mode            string
				hostDescriptors bool
				want            string
			}{
				{"signals", false, "blocked: 0 1\nunblocked: 1\nsigsuspend: -1 1 1\nwait_mask: 1 0 1\nsets: 0 8\n"},
				{"nosignal", false, "nosignal: -1 1\nsigpipe: -1 1 1\n"},
				{"cloexec", true, "open\nclosed\nclosed\nclosed\nclosed\nclosed\n"},
				{"waitgroup", false, "group: 1 3\nnone left: -1 1\nother group: -1 1\n"},
			} {
				t.Run(tt.mode, func(t *testing.T) {
					out, status := runHostShimsProgram(t, runner, tt.mode, tt.hostDescriptors)
					if status != 0 || out != tt.want {
						t.Errorf("status %d, output:\n%s\nwant status 0, output:\n%s", status, out, tt.want)
					}
				})
			}
		})
	}
}

// transpileRunner transpiles the module and writes beside it a program
// that runs it once per invocation: its arguments are the module's, its
// output the module's, and its exit status the module's. The environment
// variable HOST_SHIMS_RUN_HOST_DESCRIPTORS set gives the guest the host's
// descriptor numbers (WASIConfig.HostDescriptors).
func transpileRunner(t *testing.T, wasmPath, dir string) string {
	t.Helper()
	wasmBin, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatal(err)
	}
	m, err := transpile.Parse(bytes.NewReader(wasmBin))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var main bytes.Buffer
	res, err := transpile.Translate(&main, m, transpile.Options{
		Package:          "guest",
		OutputImportPath: "hostshimsrun/guest",
		// the Go output alone: the test is about the shims, not about the
		// code generator's assembly backends
		PureOnly: true,
	})
	if err != nil {
		t.Fatalf("transpile: %v", err)
	}
	src := filepath.Join(dir, "runner")
	write := func(rel string, data []byte) {
		t.Helper()
		p := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", []byte("module hostshimsrun\n\ngo 1.25.0\n"))
	if main.Len() > 0 {
		write("guest/guest.go", main.Bytes())
	}
	for rel, data := range res.Files {
		write("guest/"+rel, data)
	}
	for rel, data := range res.Sidecars {
		write("guest/"+rel, data)
	}
	for rel, data := range res.AuxFiles {
		write("guest/"+rel, data)
	}
	write("main.go", []byte(`package main

import (
	"errors"
	"os"

	"hostshimsrun/guest"
)

func main() {
	cfg := guest.HostWASIConfig()
	cfg.Args = append([]string{"host_shims_run"}, os.Args[1:]...)
	cfg.HostDescriptors = os.Getenv("HOST_SHIMS_RUN_HOST_DESCRIPTORS") != ""
	m := guest.NewWithWASI(guest.NewWASI(cfg))
	defer func() {
		r := recover()
		if r == nil {
			os.Exit(0)
		}
		var exit *guest.WasiExitError
		if e, ok := r.(error); ok && errors.As(e, &exit) {
			os.Exit(int(exit.Code))
		}
		panic(r)
	}()
	m.Start()
}
`))
	bin := filepath.Join(dir, "runner-bin")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = src
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build of the transpiled program: %v\n%s", err, out)
	}
	return bin
}

// runHostShimsProgram runs the transpiled program with one argument and
// returns what it printed and its exit status.
func runHostShimsProgram(t *testing.T, runner, arg string, hostDescriptors bool) (string, int) {
	t.Helper()
	cmd := exec.Command(runner, arg)
	cmd.Env = os.Environ()
	if hostDescriptors {
		cmd.Env = append(cmd.Env, "HOST_SHIMS_RUN_HOST_DESCRIPTORS=1")
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return out.String(), 0
	case errors.As(err, &exitErr):
		return out.String(), exitErr.ExitCode()
	}
	t.Fatalf("run %s: %v", arg, err)
	return "", 0
}
