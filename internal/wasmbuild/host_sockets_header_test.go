package wasmbuild

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The network capability materializes the socket shim's <netdb.h> into
// the build-local host-include dir — independent of the POSIX-compat
// overlay, which a wasi-native project (NoPosixCompat) never deploys.
// Before this, such a project's wasm-build died compiling wasmify's own
// host_sockets.cc with "'netdb.h' file not found".
func TestDeployHostHeadersNetdb(t *testing.T) {
	buildDir := t.TempDir()
	incDir, err := DeployHostHeaders(buildDir, hostWith("network.sockets"))
	if err != nil {
		t.Fatalf("DeployHostHeaders: %v", err)
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

// The host-include dir reaches EVERY compile when a capability is given:
// the upstream's getaddrinfo() callers and the shim must resolve the same
// <netdb.h>, or they disagree on struct addrinfo's field order.
func TestWasmCompileFlagsHostIncludeDir(t *testing.T) {
	cfg := WasmConfig{
		WasiSDKPath:    "/opt/wasi-sdk",
		Target:         "wasm32-wasip1",
		Host:           hostWith("network.sockets"),
		HostIncludeDir: "/tmp/host-include",
	}
	flags := wasmCompileFlags(cfg)
	joined := strings.Join(flags, " ")
	for _, must := range []string{"-DWASMIFY_HOST_NETWORK", "-DWASMIFY_HOST_NETWORK_SOCKETS", "-I /tmp/host-include"} {
		if !strings.Contains(joined, must) {
			t.Errorf("expected %q in wasmCompileFlags, got %v", must, flags)
		}
	}
	if strings.Contains(joined, "-DWASMIFY_HOST_PROCESS") {
		t.Errorf("a process macro leaked into a network-only config: %v", flags)
	}

	// Two capabilities: the shared dir is added exactly once.
	cfg.Host = hostWith("network.sockets", "process.spawn")
	if n := strings.Count(strings.Join(wasmCompileFlags(cfg), " "), "-I /tmp/host-include"); n != 1 {
		t.Errorf("host-include added %d times, want 1", n)
	}

	// No capability: the dir is never added, even when set.
	cfg.Host = hostWith()
	if strings.Contains(strings.Join(wasmCompileFlags(cfg), " "), "/tmp/host-include") {
		t.Errorf("host-include added with no capability given")
	}
}
