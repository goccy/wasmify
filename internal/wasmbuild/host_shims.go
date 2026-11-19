package wasmbuild

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/wasmify/internal/hostcap"
)

// hostShimFS holds wasmify's host shims: the libc functions of the host
// capabilities (wasmify.json's "host"), on imports of the module
// "wasmify_posix". Each source is guarded inside by the macros of the
// capability parts it serves (see hostParts), so a build compiles exactly
// what it is given.
//
//go:embed shims/*.cc
var hostShimFS embed.FS

// wasmifyPosixHeader declares every import of the "wasmify_posix" module the
// shims are built on, and the building blocks the shims export. The shims
// include it from beside them; an application includes it from the
// host-include directory.
//
//go:embed shims/wasmify_posix.h
var wasmifyPosixHeader []byte

// hostHeaderFS holds the headers of the C library interfaces the shims
// implement and wasi-libc has no header for.
//
//go:embed shims/include
var hostHeaderFS embed.FS

// hostSubprocessHeader returns the content of a header of hostHeaderFS.
func hostSubprocessHeader(name string) string {
	data, err := hostHeaderFS.ReadFile("shims/include/" + name)
	if err != nil {
		panic(fmt.Sprintf("embedded host header %s: %v", name, err))
	}
	return string(data)
}

const wasmifyPosixHeaderName = "wasmify_posix.h"

// hostShim returns the content of one of the shims.
func hostShim(name string) ([]byte, error) {
	data, err := hostShimFS.ReadFile("shims/" + name)
	if err != nil {
		return nil, fmt.Errorf("missing embedded host shim %s: %w", name, err)
	}
	return data, nil
}

// hostHeaderContent returns the content of a header a capability part puts
// in host-include or beside the shims: hostHeaderFS's own; netdb.h, which is
// the POSIX-compat overlay's (the same file under both, so that a project
// and the socket shim see one layout of struct addrinfo whether or not the
// overlay is on); or the stub registry's (spawn.h, sys/wait.h, pwd.h,
// grp.h).
func hostHeaderContent(name string) (string, error) {
	if data, err := hostHeaderFS.ReadFile("shims/include/" + name); err == nil {
		return string(data), nil
	}
	if name == "netdb.h" {
		data, err := posixCompatFS.ReadFile("stubs/include/netdb.h")
		if err != nil {
			return "", fmt.Errorf("missing embedded header <%s>: %w", name, err)
		}
		return string(data), nil
	}
	if stub, ok := LookupHeaderStub(name); ok {
		return stub.Content, nil
	}
	return "", fmt.Errorf("no embedded header <%s>", name)
}

// deployWasmifyPosixHeader writes wasmify_posix.h into dir.
func deployWasmifyPosixHeader(dir string) error {
	if err := DeployStubHeader(dir, wasmifyPosixHeaderName, string(wasmifyPosixHeader)); err != nil {
		return fmt.Errorf("failed to deploy %s: %w", wasmifyPosixHeaderName, err)
	}
	return nil
}

// HostIncludeSubdir is the build-local directory (under the wasm-build
// BuildDir) where wasmify materializes the host-capability headers.
const HostIncludeSubdir = "host-include"

// DeployHostHeaders writes the headers of the given host capabilities into
// <buildDir>/host-include and returns that directory ("" when no
// capability is given): wasmify_posix.h, and per capability part the
// headers wasi-libc does not have (spawn.h and sys/wait.h with
// process.spawn, netdb.h with network, termios.h with terminal, syslog.h
// with system.logging, the System V IPC headers with ipc.sysv).
//
// The directory is added to every wasm-build compile's include path (see
// wasmCompileFlags), so the upstream sources and wasmify's shims resolve the
// headers WITHOUT touching the shared wasi-sdk sysroot. The capability is
// gated by the -DWASMIFY_HOST_* macros wasmify defines only at wasm-build
// (and in the capture build): the upstream's own (host-arch-equivalent)
// build phase never sees them.
func DeployHostHeaders(buildDir string, host hostcap.Set) (string, error) {
	if host.Empty() {
		return "", nil
	}
	incDir := filepath.Join(buildDir, HostIncludeSubdir)
	for _, header := range hostHeaderNames(host) {
		content, err := hostHeaderContent(header)
		if err != nil {
			return "", err
		}
		if err := DeployStubHeader(incDir, header, content); err != nil {
			return "", fmt.Errorf("failed to deploy host header <%s>: %w", header, err)
		}
	}
	if err := deployWasmifyPosixHeader(incDir); err != nil {
		return "", err
	}
	return incDir, nil
}

// DeployHostShims writes the shim sources of the given host capabilities
// under <buildDir>/host-shims/ and returns their absolute paths, in
// hostShimOrder, for the bridge compile+link to pick up. With no capability
// given, nothing is written and an empty slice is returned (the wasm imports
// only standard wasi and stays portable). The headers the shims include from
// beside them (wasmify_posix.h, and pwd.h and grp.h with users) are written
// there too, and are not returned.
func DeployHostShims(buildDir string, host hostcap.Set) ([]string, error) {
	names := hostShimNames(host)
	if len(names) == 0 {
		return nil, nil
	}
	shimDir := filepath.Join(buildDir, "host-shims")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create host-shims dir: %w", err)
	}
	if err := deployWasmifyPosixHeader(shimDir); err != nil {
		return nil, err
	}
	for _, header := range hostPrivateHeaderNames(host) {
		content, err := hostHeaderContent(header)
		if err != nil {
			return nil, err
		}
		if err := DeployStubHeader(shimDir, header, content); err != nil {
			return nil, fmt.Errorf("failed to deploy host shim header <%s>: %w", header, err)
		}
	}
	var paths []string
	for _, name := range names {
		content, err := hostShim(name)
		if err != nil {
			return nil, err
		}
		dst := filepath.Join(shimDir, name)
		if err := os.WriteFile(dst, content, 0o644); err != nil {
			return nil, fmt.Errorf("failed to write host shim %s: %w", name, err)
		}
		paths = append(paths, dst)
	}
	return paths, nil
}
