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
)

// hostShimsRunWant is what testdata/host_shims_run.c prints when every
// shim works, run as a process of its own (not the host process): the
// fixed identity and empty databases of such a process, no system logger,
// no System V IPC, files without owners to give away, and libc's own work
// (signals, the interval timer, time zones, the protocols database,
// inet_ntop, shutdown, poll) done in full.
const hostShimsRunWant = `ids: 42 1 0 0
user 0: none
setenv: 1
unsetenv: unset
uname: 0 wasi
getpriority: -1 ENOSYS
raise: 1
blocked: 1 1
unblocked: 2
itimer: 1
alarm: 10
times: 1
tcgetattr: -1 ENOTTY
ctermid: /dev/tty
tz: 1970-01-01 00 UTC UTC
openlog: ENOSYS
msgget: ENOSYS
mkstemp: 1
fchown unchanged: 0
chown other: -1 EPERM
chroot: -1 EPERM
fchdir: 0 /
getprotobyname: 6
inet_ntop: ::ffff:1.2.3.4
shutdown: 0 0
poll: 1 1
`

// A C program that calls the libc functions of every host shim is built
// with every host capability given, for both pointer widths, transpiled by
// wasm2go and run:
// each function does its job on the host's imports (module wasmify_posix),
// and an exec ends the program with the command's status. Skipped where
// no wasi-sdk is installed.
func TestHostShimsRun(t *testing.T) {
	sdk, err := DetectWasiSDK("")
	if err != nil {
		t.Skipf("no wasi-sdk: %v", err)
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh to exec")
	}
	for _, wasm64 := range []bool{false, true} {
		name := "wasm32"
		if wasm64 {
			name = "wasm64"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			src, err := filepath.Abs(filepath.Join("testdata", "host_shims_run.c"))
			if err != nil {
				t.Fatal(err)
			}
			var all hostcap.Set
			all.EnableAll()
			wasm := buildHostProgram(t, sdk, dir, all, wasm64, src)
			runner := transpileRunner(t, wasm, dir)
			out, status := runHostShimsProgram(t, runner, "all")
			if status != 0 || out != hostShimsRunWant {
				t.Errorf("status %d, output:\n%s\nwant status 0, output:\n%s", status, out, hostShimsRunWant)
			}
			out, status = runHostShimsProgram(t, runner, "exec")
			if status != 3 || out != "" {
				t.Errorf("exec: status %d, output %q; want 3 and nothing", status, out)
			}
		})
	}
}

// transpileRunner transpiles the module and writes beside it a program
// that runs it once per invocation: its arguments are the module's, its
// output the module's, and its exit status the module's.
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
	w := guest.DefaultWASI()
	w.SetArgs(append([]string{"host_shims_run"}, os.Args[1:]...))
	w.SetPreopenDir(os.Getenv("HOST_SHIMS_RUN_DIR"))
	m := guest.NewWithWASI(w)
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

// runHostShimsProgram runs the transpiled program with one argument, its
// files in a directory of its own, and returns what it printed and its
// exit status.
func runHostShimsProgram(t *testing.T, runner, arg string) (string, int) {
	t.Helper()
	cmd := exec.Command(runner, arg)
	cmd.Env = append(os.Environ(), "HOST_SHIMS_RUN_DIR="+t.TempDir())
	var out bytes.Buffer
	cmd.Stdout = &out
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
