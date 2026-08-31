package wasmbuild

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeployHostShims(t *testing.T) {
	tests := []struct {
		name           string
		hostSockets    bool
		hostSubprocess bool
		hostFS         bool
		wantNames      []string
	}{
		{name: "none", wantNames: nil},
		{name: "sockets only", hostSockets: true, wantNames: []string{hostSocketsShimName}},
		{name: "subprocess only", hostSubprocess: true, wantNames: []string{hostSubprocessShimName}},
		{name: "fs only", hostFS: true, wantNames: []string{hostFSShimName}},
		{name: "all", hostSockets: true, hostSubprocess: true, hostFS: true, wantNames: []string{hostSocketsShimName, hostSubprocessShimName, hostFSShimName}},
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
				if _, err := os.Stat(filepath.Join(shimDir, hostSubprocessShimName)); err == nil {
					t.Fatalf("subprocess shim deployed but not requested")
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
