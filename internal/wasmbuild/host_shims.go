package wasmbuild

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/goccy/wasmify/internal/hostcap"
)

// hostShimFS holds wasmify's host shims: the libc functions of the host
// capabilities (wasmify.json's "host"). Each source is guarded inside by a
// macro of hostcap.Set.Macros: the file system's and the processes' by the
// macros of the parts they serve, the network's by the macro of the
// deprecated flag it served (WASMIFY_HOST_SOCKETS).
//
//go:embed shims/*.cc
var hostShimFS embed.FS

// wasmifyPosixHeader declares every import of the "wasip1_posix_extension"
// module the shims are built on. The shims and an application include it
// from the host-include directory.
//
//go:embed shims/wasmify_posix.h
var wasmifyPosixHeader string

// hostIncludeFS holds wasmify's own host-include headers: a C library
// header, with the declarations of the functions the shims define that the
// C library does not declare.
//
//go:embed shims/include
var hostIncludeFS embed.FS

// hostShim returns the content of one of the shims.
func hostShim(name string) ([]byte, error) {
	data, err := hostShimFS.ReadFile("shims/" + name)
	if err != nil {
		return nil, fmt.Errorf("missing embedded host shim %s: %w", name, err)
	}
	return data, nil
}

// hostHeaderContent returns the content of a header a capability part puts
// in host-include: wasmify_posix.h; one of hostIncludeFS (unistd.h);
// netdb.h, which is the POSIX-compat overlay's (the same file under both,
// so that a project and the socket shim see one layout of struct addrinfo
// whether or not the overlay is on); or the stub registry's (spawn.h,
// sys/wait.h).
func hostHeaderContent(name string) (string, error) {
	if name == "wasmify_posix.h" {
		return wasmifyPosixHeader, nil
	}
	data, err := hostIncludeFS.ReadFile("shims/include/" + name)
	if err == nil {
		return string(data), nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("embedded header <%s>: %w", name, err)
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

// HostIncludeSubdir is the build-local directory (under the wasm-build
// BuildDir) where wasmify materializes the host-capability headers.
const HostIncludeSubdir = "host-include"

// DeployHostHeaders writes the headers of the given host capabilities into
// <buildDir>/host-include and returns that directory ("" when no given part
// has a header): per capability part the headers wasi-libc does not have
// (wasmify_posix.h with filesystem and process.spawn, spawn.h, sys/wait.h
// and unistd.h with process.spawn, netdb.h with network).
//
// The directory is added to every wasm-build compile's include path (see
// wasmCompileFlags), so the upstream sources and wasmify's shims resolve the
// headers WITHOUT touching the shared wasi-sdk sysroot. The capability is
// gated by the -DWASMIFY_HOST_* macros wasmify defines only at wasm-build
// (and in the capture build): the upstream's own (host-arch-equivalent)
// build phase never sees them.
func DeployHostHeaders(buildDir string, host hostcap.Set) (string, error) {
	headers := hostHeaderNames(host)
	if len(headers) == 0 {
		return "", nil
	}
	incDir := filepath.Join(buildDir, HostIncludeSubdir)
	for _, header := range headers {
		content, err := hostHeaderContent(header)
		if err != nil {
			return "", err
		}
		if err := DeployStubHeader(incDir, header, content); err != nil {
			return "", fmt.Errorf("failed to deploy host header <%s>: %w", header, err)
		}
	}
	return incDir, nil
}

// DeployHostShims writes the shim sources of the given host capabilities
// under <buildDir>/host-shims/ and returns their absolute paths, in
// hostShimOrder, for the bridge compile+link to pick up. With no capability
// given, nothing is written and an empty slice is returned (the wasm imports
// only standard wasi and stays portable).
func DeployHostShims(buildDir string, host hostcap.Set) ([]string, error) {
	names := hostShimNames(host)
	if len(names) == 0 {
		return nil, nil
	}
	shimDir := filepath.Join(buildDir, "host-shims")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create host-shims dir: %w", err)
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
