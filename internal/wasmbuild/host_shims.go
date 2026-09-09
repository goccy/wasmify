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

// hostFSShim is the generic filesystem-fidelity shim. It provides __wrap_
// definitions for chmod/stat/lstat/chdir backed by the path_chmod/
// path_filestat_mode host imports (plus lexical chdir normalization) and is
// gated internally on WASMIFY_HOST_FS; the matching -Wl,--wrap= flags come
// from EffectiveExtraLDFlags.
//
//go:embed shims/host_fs.cc
var hostFSShim []byte

const (
	hostSocketsShimName    = "host_sockets.cc"
	hostSubprocessShimName = "host_subprocess.cc"
	hostFSShimName         = "host_fs.cc"
)

// HostSubprocessStubHeaders lists the stub headers the subprocess shim needs to
// compile (resolved from the stub registry against cfg.PosixCompatDir).
var HostSubprocessStubHeaders = []string{"spawn.h", "sys/wait.h"}

// hostSocketsHeader is the header the socket shim and its consumers share:
// struct addrinfo, the AI_*/EAI_* values, and the getaddrinfo family
// prototypes. It is the same file the POSIX-compat overlay ships as <netdb.h>,
// deployed separately so the socket capability does not depend on the overlay
// being enabled (a wasi-native project builds with NoPosixCompat and still
// opts in to host sockets).
const hostSocketsHeader = "netdb.h"

// HostIncludeSubdir is the build-local directory (under the wasm-build
// BuildDir) where wasmify materializes the host-capability stub headers.
const HostIncludeSubdir = "host-include"

// DeployHostSocketsHeaders writes the host-sockets contract header (netdb.h)
// into <buildDir>/host-include and returns that directory. The wasip1 sysroot
// has no netdb.h; the socket shim #includes it for struct addrinfo and the
// EAI_* values, and every upstream translation unit that calls getaddrinfo()
// must see the SAME layout, so the header goes on every wasm-build compile's
// -I (see wasmCompileFlags) rather than only on the shim's. Under the
// POSIX-compat overlay the identical file is also reachable via -isystem; the
// include guard makes the duplicate harmless. The build-local dir keeps the
// shared wasi-sdk sysroot pristine, and the capability stays gated by the
// -DWASMIFY_HOST_SOCKETS macro wasmify defines only at wasm-build.
func DeployHostSocketsHeaders(buildDir string) (string, error) {
	incDir := filepath.Join(buildDir, HostIncludeSubdir)
	data, err := posixCompatFS.ReadFile("stubs/include/" + hostSocketsHeader)
	if err != nil {
		return "", fmt.Errorf("missing embedded host sockets header <%s>: %w", hostSocketsHeader, err)
	}
	if err := DeployStubHeader(incDir, hostSocketsHeader, string(data)); err != nil {
		return "", fmt.Errorf("failed to deploy host sockets header <%s>: %w", hostSocketsHeader, err)
	}
	return incDir, nil
}

// DeployHostSubprocessHeaders writes the host-subprocess stub headers (spawn.h,
// sys/wait.h) into <buildDir>/host-include and returns that directory. wasi-libc
// ships neither header (WASI preview1 has no processes); the stubs declare the
// posix_spawn/waitpid surface the host-subprocess shim backs with host imports.
//
// The directory is added to every wasm-build compile's include path (see
// wasmCompileFlags), so the upstream sources and wasmify's shim resolve
// <spawn.h>/<sys/wait.h> WITHOUT touching the shared wasi-sdk sysroot. The
// capability is gated by the -DWASMIFY_HOST_SUBPROCESS macro wasmify defines
// only at wasm-build: the upstream's own (host-arch-equivalent) build phase
// never sees that macro, so it stays unmodified and #includes neither header
// — which is why the build-local dir suffices and the sysroot is left pristine
// (no cross-project leakage). The dir's mere presence is inert until the macro
// is on.
func DeployHostSubprocessHeaders(buildDir string) (string, error) {
	incDir := filepath.Join(buildDir, HostIncludeSubdir)
	for _, header := range HostSubprocessStubHeaders {
		stub, ok := LookupHeaderStub(header)
		if !ok {
			return "", fmt.Errorf("missing stub for host subprocess header <%s>", header)
		}
		if err := DeployStubHeader(incDir, header, stub.Content); err != nil {
			return "", fmt.Errorf("failed to deploy host subprocess header <%s>: %w", header, err)
		}
	}
	return incDir, nil
}

// DeployHostShims writes the enabled host-capability shim sources under
// <buildDir>/host-shims/ and returns their absolute source paths (sockets
// first, then subprocess) for the bridge compile+link to pick up. Each shim is
// written only when its flag is on; with both flags off, nothing is written and
// an empty slice is returned (the wasm imports only standard wasi and stays
// portable). The shim bodies are additionally guarded by their respective
// WASMIFY_HOST_* macros, which wasmify defines for the compile only when the
// flag is on.
func DeployHostShims(buildDir string, hostSockets, hostSubprocess, hostFS bool) ([]string, error) {
	if !hostSockets && !hostSubprocess && !hostFS {
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
	if hostFS {
		if err := write(hostFSShimName, hostFSShim); err != nil {
			return nil, err
		}
	}

	return paths, nil
}
