package simplelib_proto_wasm2go

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/goccy/wasmify/testdata/simplelib_proto_wasm2go/internal/wasm2go"
)

type callbackFunc func(methodID int32, req []byte) ([]byte, error)

func (f callbackFunc) HandleCallback(methodID int32, req []byte) ([]byte, error) {
	return f(methodID, req)
}

// callInvoke runs the host side of callback_invoke as the guest's call
// reaches it: with m.mu held by the bridge call that entered the guest.
func callInvoke(handler CallbackHandler) int64 {
	id := RegisterCallback(handler)
	defer UnregisterCallback(id)
	m := module()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.handleCallback(id, 0, 0, 0)
}

// TestCallbackJumped verifies that a handler returning errCallbackJumped
// (also wrapped) is answered callbackJumped, while any other error is
// answered with the error message as before.
func TestCallbackJumped(t *testing.T) {
	t.Run("jumped", func(t *testing.T) {
		got := callInvoke(callbackFunc(func(int32, []byte) ([]byte, error) {
			return nil, fmt.Errorf("wrapped: %w", errCallbackJumped)
		}))
		if got != callbackJumped {
			t.Fatalf("callback_invoke = %d, want callbackJumped (%d)", got, callbackJumped)
		}
	})
	t.Run("other error", func(t *testing.T) {
		herr := errors.New("handler failed")
		got := callInvoke(callbackFunc(func(int32, []byte) ([]byte, error) {
			return nil, herr
		}))
		if got == 0 || got == callbackJumped {
			t.Fatalf("callback_invoke = %d, want an answer carrying the error", got)
		}
		m := module()
		m.mu.Lock()
		defer m.mu.Unlock()
		ptr, n := decodeResult(m.g, got)
		answer := bytes.Clone(wasm2go.Memory(m.g)[ptr : ptr+n])
		wasm2go.WasmFree(m.g, wptr(ptr))
		if want := pbAppendString(nil, 15, herr.Error()); !bytes.Equal(answer, want) {
			t.Errorf("answer = %q, want %q", answer, want)
		}
	})
}
