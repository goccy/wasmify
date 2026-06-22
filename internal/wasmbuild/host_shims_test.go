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
		wantNames      []string
	}{
		{name: "neither", hostSockets: false, hostSubprocess: false, wantNames: nil},
		{name: "sockets only", hostSockets: true, hostSubprocess: false, wantNames: []string{hostSocketsShimName}},
		{name: "subprocess only", hostSockets: false, hostSubprocess: true, wantNames: []string{hostSubprocessShimName}},
		{name: "both", hostSockets: true, hostSubprocess: true, wantNames: []string{hostSocketsShimName, hostSubprocessShimName}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buildDir := t.TempDir()

			paths, err := DeployHostShims(buildDir, tt.hostSockets, tt.hostSubprocess)
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
			if !tt.hostSockets && !tt.hostSubprocess {
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
		})
	}
}

func TestHostShimFlags(t *testing.T) {
	// Defaults: both off.
	gotS, gotP := HostShimFlags(WasmConfig{})
	if gotS || gotP {
		t.Fatalf("defaults should be off, got sockets=%v subprocess=%v", gotS, gotP)
	}

	// cfg opt-in.
	gotS, gotP = HostShimFlags(WasmConfig{HostSockets: true, HostSubprocess: true})
	if !gotS || !gotP {
		t.Fatalf("cfg opt-in should enable both, got sockets=%v subprocess=%v", gotS, gotP)
	}

	// Env override.
	t.Setenv("WASMIFY_HOST_SOCKETS", "1")
	gotS, _ = HostShimFlags(WasmConfig{})
	if !gotS {
		t.Fatalf("env override should enable sockets")
	}
}
