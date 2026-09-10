package wasmbuild

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDeployHostSocketsHeaders pins the contract that opting in to host
// sockets materializes the socket shim's <netdb.h> into the build-local
// host-include dir — independent of the POSIX-compat overlay, which a
// wasi-native project (NoPosixCompat) never deploys. Before this, such a
// project's wasm-build died compiling wasmify's own host_sockets.cc with
// "'netdb.h' file not found".
func TestDeployHostSocketsHeaders(t *testing.T) {
	buildDir := t.TempDir()
	incDir, err := DeployHostSocketsHeaders(buildDir)
	if err != nil {
		t.Fatalf("DeployHostSocketsHeaders: %v", err)
	}
	if want := filepath.Join(buildDir, HostIncludeSubdir); incDir != want {
		t.Fatalf("include dir = %q, want %q", incDir, want)
	}
	data, err := os.ReadFile(filepath.Join(incDir, "netdb.h"))
	if err != nil {
		t.Fatalf("netdb.h not deployed: %v", err)
	}
	// The deployed file must be the very header the POSIX-compat overlay
	// ships, so both paths agree on struct addrinfo's layout by construction.
	embedded, err := posixCompatFS.ReadFile("stubs/include/netdb.h")
	if err != nil {
		t.Fatalf("embedded netdb.h: %v", err)
	}
	if string(data) != string(embedded) {
		t.Fatalf("deployed netdb.h differs from the embedded POSIX-compat copy")
	}
	if !strings.Contains(string(data), "struct addrinfo") {
		t.Fatalf("deployed netdb.h lacks struct addrinfo")
	}
}

// TestWasmCompileFlagsHostSocketsIncludeDir pins that the host-include dir
// reaches EVERY compile when host sockets alone are on (no host subprocess):
// the upstream's getaddrinfo() callers and the shim must resolve the same
// <netdb.h>, or they disagree on struct addrinfo's field order.
func TestWasmCompileFlagsHostSocketsIncludeDir(t *testing.T) {
	cfg := WasmConfig{
		WasiSDKPath:    "/opt/wasi-sdk",
		Target:         "wasm32-wasip1",
		HostSockets:    true,
		HostIncludeDir: "/tmp/host-include",
	}
	flags := wasmCompileFlags(cfg)
	joined := strings.Join(flags, " ")
	for _, must := range []string{"-DWASMIFY_HOST_SOCKETS", "-I /tmp/host-include"} {
		if !strings.Contains(joined, must) {
			t.Errorf("expected %q in wasmCompileFlags, got %v", must, flags)
		}
	}
	if strings.Contains(joined, "-DWASMIFY_HOST_SUBPROCESS") {
		t.Errorf("host subprocess macro leaked into a sockets-only config: %v", flags)
	}

	// Both capabilities on: the shared dir is added exactly once.
	cfg.HostSubprocess = true
	if n := strings.Count(strings.Join(wasmCompileFlags(cfg), " "), "-I /tmp/host-include"); n != 1 {
		t.Errorf("host-include added %d times, want 1", n)
	}

	// Neither capability on: the dir is never added, even when set.
	cfg.HostSockets, cfg.HostSubprocess = false, false
	if strings.Contains(strings.Join(wasmCompileFlags(cfg), " "), "/tmp/host-include") {
		t.Errorf("host-include added with no capability opted in")
	}
}
