package wasmbuild

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

// hostSocketsShim is the generic libc->import socket shim. It DEFINES
// socket()/connect()/getaddrinfo() backed by the host's sock_* imports and is
// gated internally on WASMIFY_HOST_SOCKETS, so it only emits symbols/imports
// when wasmify defines that macro for the compile.
//
//go:embed shims/host_sockets.cc
var hostSocketsShim []byte

// hostSubprocessShim is the generic libc->import subprocess shim. It DEFINES
// the posix_spawn family + waitpid/pipe backed by the host's proc_*/pipe
// imports and is gated internally on WASMIFY_HOST_SUBPROCESS.
//
//go:embed shims/host_subprocess.cc
var hostSubprocessShim []byte

const (
	hostSocketsShimName    = "host_sockets.cc"
	hostSubprocessShimName = "host_subprocess.cc"
)

// HostShimFlags resolves the effective host-capability opt-ins for the bridge
// build, honouring both wasmify.json (via cfg) and the WASMIFY_HOST_* env
// overrides. It is the single source of truth callers use to decide which
// shims to deploy, matching the macros buildBridgeCompileArgs defines for the
// compile.
func HostShimFlags(cfg WasmConfig) (hostSockets, hostSubprocess bool) {
	return hostShimFlags(cfg)
}

// HostSubprocessStubHeaders lists the stub headers the subprocess shim needs to
// compile (resolved from the stub registry against cfg.PosixCompatDir). The
// socket shim's headers are part of the always-deployed POSIX-compat set, so
// only the subprocess shim contributes here.
var HostSubprocessStubHeaders = []string{"spawn.h", "sys/wait.h"}

// DeployHostShims writes the enabled host-capability shim sources under
// <buildDir>/host-shims/ and returns their absolute source paths (sockets
// first, then subprocess) for the bridge compile+link to pick up. Each shim is
// written only when its flag is on; with both flags off, nothing is written and
// an empty slice is returned (the wasm imports only standard wasi and stays
// portable). The shim bodies are additionally guarded by their respective
// WASMIFY_HOST_* macros, which wasmify defines for the compile only when the
// flag is on.
func DeployHostShims(buildDir string, hostSockets, hostSubprocess bool) ([]string, error) {
	if !hostSockets && !hostSubprocess {
		return nil, nil
	}

	shimDir := filepath.Join(buildDir, "host-shims")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create host-shims dir: %w", err)
	}

	var paths []string
	write := func(name string, content []byte) error {
		dst := filepath.Join(shimDir, name)
		if err := os.WriteFile(dst, content, 0o644); err != nil {
			return fmt.Errorf("failed to write host shim %s: %w", name, err)
		}
		paths = append(paths, dst)
		return nil
	}

	if hostSockets {
		if err := write(hostSocketsShimName, hostSocketsShim); err != nil {
			return nil, err
		}
	}
	if hostSubprocess {
		if err := write(hostSubprocessShimName, hostSubprocessShim); err != nil {
			return nil, err
		}
	}

	return paths, nil
}
