package main

import (
	"regexp"
	"strings"
	"testing"
)

// TestInvokeReqFreeOnErrorPath pins the request-buffer free contract that
// both the wazero callExport and the wasm2go runtime invoke must honour:
// once the host has WasmAlloc'd `reqPtr` to carry the serialised request
// into wasm memory, that allocation must be freed before the function
// returns, regardless of whether the wasm export call succeeded, the
// response read failed, or the response length was zero.
//
// The earlier shape returned `nil, err` directly from the error branch
// without freeing reqPtr; every trapping call leaked the request buffer
// in wasm linear memory until the module was torn down. The fix is a
// single `defer freeFn / WasmFree` placed right after the allocation, so
// every return path runs the free. This test guards against re-
// introducing the old shape on either runtime path.
func TestInvokeReqFreeOnErrorPath(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "runtime_wasm2go.go moduleBodyWasm2go template", body: moduleBodyWasm2go},
		{name: "wazero callExport (main.go generateModule output)", body: generateModule("pkg")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Both runtimes use the same allocate-then-defer-free shape.
			// We match it loosely on whitespace so cosmetic reformatting
			// of the surrounding code does not silently break the
			// regression.
			wasm2goPat := regexp.MustCompile(`(?s)reqPtr\s*=\s*wasm2go\.WasmAlloc.*?defer\s+wasm2go\.WasmFree\([^)]*reqPtr\)`)
			wazeroPat := regexp.MustCompile(`(?s)reqPtr\s*=\s*results\[0\].*?defer\s+m\.freeFn\.Call\([^)]*reqPtr\)`)
			if !wasm2goPat.MatchString(tc.body) && !wazeroPat.MatchString(tc.body) {
				t.Errorf("%s: missing `defer …Free(reqPtr)` after `reqPtr = …Alloc(...)`.\nIf you refactored the invoke path, keep the unconditional free or every trapping call will leak the request buffer.\nBody excerpt:\n%s",
					tc.name, excerptAround(tc.body, "reqPtr ="))
			}

			// The defer must come BEFORE the `call(...)` / `fn.Call(...)`
			// site, otherwise an error / panic on that call still returns
			// without freeing. We assert the order by index.
			body := tc.body
			allocIdx := strings.Index(body, "reqPtr = ")
			callIdx := indexOfFirst(body, []string{
				"packed, err := call(",
				"results, err := fn.Call(",
			})
			deferIdx := indexOfFirst(body, []string{
				"defer wasm2go.WasmFree(m.g, reqPtr)",
				"defer m.freeFn.Call(ctx, reqPtr)",
			})
			if allocIdx < 0 || callIdx < 0 || deferIdx < 0 {
				t.Skipf("template missing one of the expected anchors (alloc=%d call=%d defer=%d) — adjust this test if the invoke shape was redesigned", allocIdx, callIdx, deferIdx)
				return
			}
			if allocIdx >= deferIdx || deferIdx >= callIdx {
				t.Errorf("%s: defer-free of reqPtr must be installed between the WasmAlloc and the wasm call so the alloc is freed even when the call traps. Got allocIdx=%d deferIdx=%d callIdx=%d",
					tc.name, allocIdx, deferIdx, callIdx)
			}
		})
	}
}

func excerptAround(s, anchor string) string {
	idx := strings.Index(s, anchor)
	if idx < 0 {
		return "<anchor not found>"
	}
	start := idx - 80
	if start < 0 {
		start = 0
	}
	end := idx + 240
	if end > len(s) {
		end = len(s)
	}
	return s[start:end]
}

func indexOfFirst(haystack string, needles []string) int {
	best := -1
	for _, n := range needles {
		i := strings.Index(haystack, n)
		if i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	return best
}
