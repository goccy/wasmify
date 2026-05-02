package simplelib_proto

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/tetratelabs/wazero/api"
)

// countingFunction wraps a wazero api.Function and increments `count`
// on every Call / CallWithStack. We install one over the wasm export
// for Circle's Free RPC so the test can observe — end-to-end through
// the wazero runtime — exactly how many times the bridge handler
// `delete reinterpret_cast<const calc::Shape*>(_handle_ptr);`
// (internal/protogen/bridge.go:4596) actually runs for a given Circle.
type countingFunction struct {
	api.Function
	count *atomic.Int32
}

func (c *countingFunction) Call(ctx context.Context, params ...uint64) ([]uint64, error) {
	c.count.Add(1)
	return c.Function.Call(ctx, params...)
}

func (c *countingFunction) CallWithStack(ctx context.Context, stack []uint64) error {
	c.count.Add(1)
	return c.Function.CallWithStack(ctx, stack)
}

// installCircleFreeCounter swaps the cached api.Function for Circle's
// Free RPC (service 4, method 3 — see the (h *Circle).free() body in
// the generated simplelib.go) for a counting wrapper, returns the
// counter, and arranges restoration on test cleanup. With this in
// place every Free invocation issued by Circle wrappers — manual or
// finalizer-driven — is observable from the test.
func installCircleFreeCounter(t *testing.T) *atomic.Int32 {
	t.Helper()
	const circleSvc, circleFreeMid = int32(4), int32(3)
	m := module()
	orig, err := m.exportFor(circleSvc, circleFreeMid)
	if err != nil {
		t.Fatalf("exportFor(Circle.Free): %v", err)
	}
	var count atomic.Int32
	key := uint64(uint32(circleSvc))<<32 | uint64(uint32(circleFreeMid))
	m.mu.Lock()
	m.exportFns[key] = &countingFunction{Function: orig, count: &count}
	m.mu.Unlock()
	t.Cleanup(func() {
		m.mu.Lock()
		m.exportFns[key] = orig
		m.mu.Unlock()
	})
	return &count
}

// TestUniquePtrTakeOwnership_NoSecondFreeRPC reproduces the exact
// double-free scenario end-to-end through the wasm bridge and
// asserts the Free RPC is NOT issued after the C++ side has absorbed
// ownership.
//
// Bug origin in the generated bridge:
//
//   - `_self->add_unique(std::unique_ptr<calc::Shape>(
//          reinterpret_cast<calc::Shape*>(s)));`
//     in bridge/api_bridge.cc (emitted by handleArgExpr at
//     internal/protogen/bridge.go:3065). Constructing the unique_ptr
//     transfers ownership to ShapeBox::owned_ unconditionally — the
//     destructor of the unique_ptr will eventually `delete` the
//     calc::Circle.
//
//   - `delete reinterpret_cast<const calc::Shape*>(_handle_ptr);`
//     in bridge/api_bridge.cc (emitted by writeFreeBody at
//     internal/protogen/bridge.go:4596). Running this RPC after the
//     unique_ptr already deleted the same memory is the second free.
//
// Test scenario:
//
//  1. NewCircle(...) heap-allocates a calc::Circle on the wasm side
//     and returns its pointer, wrapped in a Go *Circle whose finalizer
//     would otherwise issue Free RPC on GC.
//  2. box.AddUnique(circle) sends the request through the bridge;
//     bridge/api_bridge.cc constructs the unique_ptr<calc::Shape> from
//     the raw pointer and ShapeBox now owns the Circle.
//  3. The Go-side wrapper at (h *ShapeBox).AddUnique calls
//     `clearPtr()` on the input handle (the post-invoke clear added by
//     the fix in protoc-gen-wasmify-go), zeroing circle.ptr.
//  4. Manually calling circle.free() exercises exactly the path the
//     finalizer would take. Its body is `if h.ptr != 0 { invoke...
//     }` — post-fix the guard short-circuits, so the wasm Free
//     export is not called and the counter stays at 0.
//
// Pre-fix, step 3 does not run: circle.ptr remains the live calc::Circle
// address, free() invokes the wasm export, and the counter trips to 1.
// That counter increment IS the second `delete` (= the double-free)
// happening at the bridge layer.
func TestUniquePtrTakeOwnership_NoSecondFreeRPC(t *testing.T) {
	t.Cleanup(gc)

	box, err := NewShapeBox()
	if err != nil {
		t.Fatalf("NewShapeBox: %v", err)
	}
	circle, err := NewCircle(1.0)
	if err != nil {
		t.Fatalf("NewCircle: %v", err)
	}
	if circle.ptr == 0 {
		t.Fatalf("freshly-constructed circle has ptr == 0; constructor failed silently?")
	}
	circlePtr := circle.ptr

	counter := installCircleFreeCounter(t)

	if err := box.AddUnique(circle); err != nil {
		t.Fatalf("box.AddUnique: %v", err)
	}

	// Drive the would-be finalizer path deterministically. (Relying on
	// runtime.GC to fire the finalizer is timing-dependent; calling
	// free() directly executes the same body.)
	circle.free()

	if got := counter.Load(); got != 0 {
		t.Fatalf("DOUBLE-FREE OBSERVED: Free RPC for calc::Circle@0x%x was issued %d time(s) after ShapeBox absorbed it via std::unique_ptr<Shape>. The bridge handler at internal/protogen/bridge.go:4596 has now run `delete` on memory ShapeBox::owned_ still owns; box's destructor will run the unique_ptr deleter on the same address (the second free). The fix in protoc-gen-wasmify-go must clearPtr() the input handle after invoking AddUnique so this Free RPC never fires.",
			circlePtr, got)
	}

	// Sanity: AddUnique must in fact have stored the Circle (size==1),
	// proving the C++ side actually took ownership rather than dropping
	// the call. Without this, count==0 could be a vacuous pass.
	if size, err := box.Size(); err != nil {
		t.Fatalf("box.Size: %v", err)
	} else if size != 1 {
		t.Fatalf("box.Size = %d, want 1 — AddUnique did not actually transfer ownership", size)
	}

	runtime.KeepAlive(box)
}

// TestRawPointerAdd_StillIssuesFreeRPC pins the negative case end-to-
// end: ShapeBox.Add takes Shape* (raw pointer), not unique_ptr<Shape>.
// The bridge does NOT auto-delete that pointer (it is appended to
// shapes_ and the C++ destructor runs `delete` on each entry — that
// chain is owned by ShapeBox via raw pointer, semantically distinct
// from the unique_ptr path). The fix must NOT mark this parameter as
// `wasm_take_ownership` and must NOT clearPtr() the wrapper, otherwise
// callers who hold a Shape they expect to keep using would lose the
// handle.
//
// Concretely: after box.Add(circle), the test calls circle.free() and
// observes that the Free RPC IS issued exactly once — the wrapper
// still owns the handle from the Go side's perspective.
func TestRawPointerAdd_StillIssuesFreeRPC(t *testing.T) {
	t.Cleanup(gc)

	box, err := NewShapeBox()
	if err != nil {
		t.Fatalf("NewShapeBox: %v", err)
	}
	circle, err := NewCircle(2.0)
	if err != nil {
		t.Fatalf("NewCircle: %v", err)
	}

	counter := installCircleFreeCounter(t)

	if err := box.Add(circle); err != nil {
		t.Fatalf("box.Add: %v", err)
	}

	// Important: do NOT call circle.free() here in production code —
	// in the raw-pointer path ShapeBox::~ShapeBox runs `delete` on
	// every entry of shapes_, so a Go-side free() would still be a
	// double-free. The test is artificial: we drive free() to prove
	// the wrapper still believes it owns the handle (the marker for
	// "no auto-clear was emitted"). The negative case being verified
	// here is only that the fix did NOT over-zero raw-pointer params.
	if circle.ptr == 0 {
		t.Fatalf("regression: ShapeBox.Add(Shape*) cleared circle.ptr — the fix must only fire on unique_ptr<T> params (raw pointers carry no static ownership signal)")
	}
	circle.free()

	if got := counter.Load(); got != 1 {
		t.Fatalf("expected raw-pointer ShapeBox.Add to leave the wrapper owning circle (so circle.free() issues exactly 1 Free RPC); got %d", got)
	}

	runtime.KeepAlive(box)
}
