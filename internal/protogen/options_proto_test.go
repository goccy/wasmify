package protogen

import (
	"bytes"
	"os"
	"testing"
)

// TestOptionsProtoInSync guards against drift between the embedded
// copy of wasmify/options.proto used by the proto generator and the
// canonical file under proto/wasmify/. Both are needed for distinct
// reasons:
//
//   - proto/wasmify/options.proto is the public schema imported by
//     every generated proto in downstream repositories (go-googlesql,
//     etc.), so it must live under proto/ for buf to discover it.
//   - internal/protogen/options.proto is loaded via //go:embed in
//     proto.go to validate / emit generated protos at build time.
//     `go:embed` cannot reach files outside the package directory,
//     hence the duplicate.
//
// Keeping the two files byte-identical is mandatory: a drift would
// silently let `wasmify gen-proto` emit options that consumers can't
// resolve. This test fails as soon as the canonical file is edited
// without copying the change into internal/protogen/.
func TestOptionsProtoInSync(t *testing.T) {
	embedded, err := os.ReadFile("options.proto")
	if err != nil {
		t.Fatalf("read internal/protogen/options.proto: %v", err)
	}
	canonical, err := os.ReadFile("../../proto/wasmify/options.proto")
	if err != nil {
		t.Fatalf("read proto/wasmify/options.proto: %v", err)
	}
	if !bytes.Equal(embedded, canonical) {
		t.Fatal("internal/protogen/options.proto is out of sync with proto/wasmify/options.proto.\n" +
			"After editing the canonical schema at proto/wasmify/options.proto, run:\n" +
			"\tcp proto/wasmify/options.proto internal/protogen/options.proto\n" +
			"so the //go:embed copy used by SaveOptionsProto / ValidateProto picks up the change.")
	}
}
