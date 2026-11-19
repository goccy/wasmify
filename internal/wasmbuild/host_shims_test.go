package wasmbuild

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The linker only reports a --wrap flag without a __wrap_ definition, or a
// __wrap_ definition without its flag (the wrapper is then dead code and
// libc's version stays in use), when a real project is linked. Both sides
// are written by hand in different files, so they are held together here.
func TestHostFSWrapFlagsMatchShim(t *testing.T) {
	const flagPrefix = "-Wl,--wrap="
	wrapped := map[string]bool{}
	for _, flag := range (WasmConfig{HostFS: true}).EffectiveExtraLDFlags() {
		name, ok := strings.CutPrefix(flag, flagPrefix)
		if !ok {
			continue
		}
		wrapped[name] = true
		for _, symbol := range []string{"__wrap_" + name + "(", "__real_" + name + "("} {
			if !bytes.Contains(hostFSShim, []byte(symbol)) {
				t.Errorf("%s is passed to the linker but the shim has no %s", flag, strings.TrimSuffix(symbol, "("))
			}
		}
	}
	for _, name := range []string{"chmod", "fchmod", "stat", "lstat", "fstat", "open", "mkdir", "chdir", "access", "faccessat", "__wasilibc_find_relpath_alloc"} {
		if !wrapped[name] {
			t.Errorf("%s is not wrapped under HostFS", name)
		}
	}
	for _, flag := range (WasmConfig{}).EffectiveExtraLDFlags() {
		if strings.HasPrefix(flag, flagPrefix) {
			t.Errorf("%s is passed to the linker without HostFS", flag)
		}
	}
}

// The subprocess capability wraps libc functions its shims define the
// __wrap_ side of (fcntl, the setenv family, the writes that deliver
// SIGPIPE); as for HostFS, flag and definition are held together here.
func TestHostSubprocessWrapFlagsMatchShims(t *testing.T) {
	const flagPrefix = "-Wl,--wrap="
	shims := [][]byte{hostSubprocessShim}
	for _, name := range hostSubprocessCompanionNames {
		content, err := hostSubprocessCompanionFS.ReadFile("shims/" + name)
		if err != nil {
			t.Fatal(err)
		}
		shims = append(shims, content)
	}
	defined := func(symbol string) bool {
		for _, shim := range shims {
			if bytes.Contains(shim, []byte(symbol)) {
				return true
			}
		}
		return false
	}
	wrapped := map[string]bool{}
	for _, flag := range (WasmConfig{HostSubprocess: true}).EffectiveExtraLDFlags() {
		name, ok := strings.CutPrefix(flag, flagPrefix)
		if !ok {
			continue
		}
		wrapped[name] = true
		for _, symbol := range []string{"__wrap_" + name + "(", "__real_" + name + "("} {
			if !defined(symbol) {
				t.Errorf("%s is passed to the linker but no subprocess shim has %s", flag, strings.TrimSuffix(symbol, "("))
			}
		}
	}
	for _, name := range []string{"fcntl", "setenv", "unsetenv", "putenv", "clearenv", "write", "writev", "send"} {
		if !wrapped[name] {
			t.Errorf("%s is not wrapped under HostSubprocess", name)
		}
	}
}

func TestDeployHostShims(t *testing.T) {
	subprocessNames := append([]string{hostSubprocessShimName}, hostSubprocessCompanionNames...)
	tests := []struct {
		name           string
		hostSockets    bool
		hostSubprocess bool
		hostFS         bool
		wantNames      []string
	}{
		{name: "none", wantNames: nil},
		{name: "sockets only", hostSockets: true, wantNames: []string{hostSocketsShimName}},
		{name: "subprocess only", hostSubprocess: true, wantNames: subprocessNames},
		{name: "fs only", hostFS: true, wantNames: []string{hostFSShimName}},
		{name: "all", hostSockets: true, hostSubprocess: true, hostFS: true,
			wantNames: append(append([]string{hostSocketsShimName}, subprocessNames...), hostFSShimName)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buildDir := t.TempDir()

			paths, err := DeployHostShims(buildDir, tt.hostSockets, tt.hostSubprocess, tt.hostFS)
			if err != nil {
				t.Fatalf("DeployHostShims error: %v", err)
			}

			if len(paths) != len(tt.wantNames) {
				t.Fatalf("got %d paths %v, want %d", len(paths), paths, len(tt.wantNames))
			}

			shimDir := filepath.Join(buildDir, "host-shims")
			for i, name := range tt.wantNames {
				want := filepath.Join(shimDir, name)
				if paths[i] != want {
					t.Fatalf("path[%d]=%q, want %q", i, paths[i], want)
				}
				if _, err := os.Stat(want); err != nil {
					t.Fatalf("expected shim file %s on disk: %v", want, err)
				}
			}

			// When neither flag is set, nothing must be written (the dir is not
			// even created) so the wasm stays portable.
			if !tt.hostSockets && !tt.hostSubprocess && !tt.hostFS {
				if _, err := os.Stat(shimDir); !os.IsNotExist(err) {
					t.Fatalf("host-shims dir should not exist when no capability is opted in")
				}
			}

			// A shim that was NOT requested must not be on disk.
			if !tt.hostSockets {
				if _, err := os.Stat(filepath.Join(shimDir, hostSocketsShimName)); err == nil {
					t.Fatalf("sockets shim deployed but not requested")
				}
			}
			if !tt.hostSubprocess {
				for _, name := range append(append([]string(nil), subprocessNames...), hostShimPrivateHeaders...) {
					if _, err := os.Stat(filepath.Join(shimDir, name)); err == nil {
						t.Fatalf("subprocess file %s deployed but not requested", name)
					}
				}
			} else {
				// the headers the subprocess shims include from beside them
				for _, name := range hostShimPrivateHeaders {
					if _, err := os.Stat(filepath.Join(shimDir, name)); err != nil {
						t.Fatalf("expected %s beside the shims: %v", name, err)
					}
				}
			}
			// every shim includes wasmify_posix.h from beside it
			if tt.hostSockets || tt.hostSubprocess || tt.hostFS {
				if _, err := os.Stat(filepath.Join(shimDir, wasmifyPosixHeaderName)); err != nil {
					t.Fatalf("expected %s beside the shims: %v", wasmifyPosixHeaderName, err)
				}
			}
			if !tt.hostFS {
				if _, err := os.Stat(filepath.Join(shimDir, hostFSShimName)); err == nil {
					t.Fatalf("fs shim deployed but not requested")
				}
			}
		})
	}
}

func TestApplyEnvOverridesHostCapabilities(t *testing.T) {
	// Defaults: both off, and nothing in the environment turns them on.
	var def WasmConfig
	def.ApplyEnvOverrides()
	if def.HostSockets || def.HostSubprocess {
		t.Fatalf("defaults should be off, got sockets=%v subprocess=%v", def.HostSockets, def.HostSubprocess)
	}

	// wasmify.json opt-in (already true) survives the fold.
	cfgOpt := WasmConfig{HostSockets: true, HostSubprocess: true}
	cfgOpt.ApplyEnvOverrides()
	if !cfgOpt.HostSockets || !cfgOpt.HostSubprocess {
		t.Fatalf("config opt-in should stay enabled, got sockets=%v subprocess=%v", cfgOpt.HostSockets, cfgOpt.HostSubprocess)
	}

	// Environment sets the same option — presence (not "1" specifically) enables it.
	t.Setenv("WASMIFY_HOST_SOCKETS", "yes")
	var env WasmConfig
	env.ApplyEnvOverrides()
	if !env.HostSockets {
		t.Fatalf("env override should enable sockets")
	}
	if env.HostSubprocess {
		t.Fatalf("only sockets was set, subprocess should stay off")
	}
}
