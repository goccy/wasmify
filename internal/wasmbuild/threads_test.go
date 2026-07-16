package wasmbuild

import (
	"strings"
	"testing"
)

// The threads capability must reach both phases of the build: every compile
// (so wasi-libc's pthread headers and the guest's threaded paths turn on) and
// the link (so wasm-ld emits a SHARED memory with the declared maximum the
// proposal requires, plus the wasi_thread_start export wasm2go starts on a
// goroutine).
func TestHostThreadsTargetSwap(t *testing.T) {
	cfg := WasmConfig{Target: "wasm32-wasip1", HostThreads: true}
	if got := effectiveTarget(cfg); got != "wasm32-wasip1-threads" {
		t.Errorf("effectiveTarget with HostThreads = %q, want wasm32-wasip1-threads", got)
	}
	cfg.HostThreads = false
	if got := effectiveTarget(cfg); got != "wasm32-wasip1" {
		t.Errorf("effectiveTarget without HostThreads = %q, want wasm32-wasip1", got)
	}
}

func TestHostThreadsFlags(t *testing.T) {
	cfg := WasmConfig{Target: "wasm32-wasip1", HostThreads: true, MaxMemoryPages: 2048}

	compile := strings.Join(wasmCompileFlags(cfg), " ")
	for _, want := range []string{"-DWASMIFY_HOST_THREADS", "-pthread"} {
		if !strings.Contains(compile, want) {
			t.Errorf("compile flags missing %q: %s", want, compile)
		}
	}

	link := strings.Join(wasmLinkFlags(cfg), " ")
	for _, want := range []string{
		"-pthread",
		"-Wl,--shared-memory",
		"-Wl,--max-memory=134217728", // 2048 pages * 64 KiB
		"-Wl,--export=wasi_thread_start",
	} {
		if !strings.Contains(link, want) {
			t.Errorf("link flags missing %q: %s", want, link)
		}
	}
}

// Off by default: a project that does not opt in must get a plain,
// single-threaded wasm — no shared memory, no pthread, no atomics.
func TestHostThreadsOffByDefault(t *testing.T) {
	cfg := WasmConfig{Target: "wasm32-wasip1"}
	all := strings.Join(wasmCompileFlags(cfg), " ") + " " + strings.Join(wasmLinkFlags(cfg), " ")
	for _, unwanted := range []string{"-pthread", "shared-memory", "WASMIFY_HOST_THREADS", "max-memory"} {
		if strings.Contains(all, unwanted) {
			t.Errorf("threads-off build leaked %q: %s", unwanted, all)
		}
	}
}

// A threads build with no explicit ceiling still declares one — a shared
// memory without a maximum is invalid wasm.
func TestHostThreadsDefaultMaxMemory(t *testing.T) {
	cfg := WasmConfig{Target: "wasm32-wasip1", HostThreads: true}
	link := strings.Join(wasmLinkFlags(cfg), " ")
	if !strings.Contains(link, "-Wl,--max-memory=67108864") { // 1024 pages
		t.Errorf("default max-memory missing: %s", link)
	}
}
