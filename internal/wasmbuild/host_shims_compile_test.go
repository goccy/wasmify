package wasmbuild

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// Every host shim compiles, for both pointer widths and with each
// capability on its own as well as with all of them, under the flags
// wasm-build gives it: the POSIX compat overlay, the host-include headers
// and the WASMIFY_HOST_* macros. A shim is written by hand against headers
// deployed from other files, so only a compile holds the two together.
// Skipped where no wasi-sdk is installed.
func TestHostShimsCompile(t *testing.T) {
	sdk, err := DetectWasiSDK("")
	if err != nil {
		t.Skipf("no wasi-sdk: %v", err)
	}
	capabilities := []struct {
		name                        string
		sockets, subprocess, hostFS bool
	}{
		{name: "sockets", sockets: true},
		{name: "subprocess", subprocess: true},
		{name: "fs", hostFS: true},
		{name: "all", sockets: true, subprocess: true, hostFS: true},
	}
	for _, wasm64 := range []bool{false, true} {
		for _, c := range capabilities {
			name := c.name + "/wasm32"
			if wasm64 {
				name = c.name + "/wasm64"
			}
			t.Run(name, func(t *testing.T) {
				buildDir := t.TempDir()
				compat, err := DeployPosixCompat(buildDir)
				if err != nil {
					t.Fatal(err)
				}
				cfg := WasmConfig{
					Target:         "wasm32-wasip1",
					Wasm64:         wasm64,
					WasiSDKPath:    sdk,
					PosixCompatDir: compat,
					HostSockets:    c.sockets,
					HostSubprocess: c.subprocess,
					HostFS:         c.hostFS,
				}
				if c.subprocess {
					if cfg.HostIncludeDir, err = DeployHostSubprocessHeaders(buildDir); err != nil {
						t.Fatal(err)
					}
				}
				if c.sockets {
					if cfg.HostIncludeDir, err = DeployHostSocketsHeaders(buildDir); err != nil {
						t.Fatal(err)
					}
				}
				srcs, err := DeployHostShims(buildDir, c.sockets, c.subprocess, c.hostFS)
				if err != nil {
					t.Fatal(err)
				}
				for _, src := range srcs {
					args := append([]string{"-fsyntax-only", "-std=c++20", "-Werror=implicit-function-declaration"},
						wasmCompileFlags(cfg)...)
					args = append(args, src)
					out, err := exec.Command(filepath.Join(sdk, "bin", "clang++"), args...).CombinedOutput()
					if err != nil {
						t.Errorf("%s does not compile: %v\n%s", filepath.Base(src), err, out)
					}
				}
			})
		}
	}
}
