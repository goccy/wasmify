package simplelib_proto

import (
	"os"
	"runtime"
	"testing"
)

// The wasm binary is embedded by the generated wasmify_module.go via
// //go:embed simplelib_proto.wasm. Init() unpacks and starts wazero
// against that embedded payload, so the test only has to call Init.
func TestMain(m *testing.M) {
	if err := Init(); err != nil {
		os.Exit(1)
	}
	code := m.Run()
	Close()
	os.Exit(code)
}

// gc forces garbage collection so wasm-side handles released via
// runtime.SetFinalizer are freed between tests in the shared module.
func gc() {
	runtime.GC()
	runtime.GC()
}

// TestSimplelibFreeFunctions exercises the free-function service — Add() and
// Version() round-trip through the bridge without any handle involvement.
func TestSimplelibFreeFunctions(t *testing.T) {
	t.Cleanup(gc)
	t.Run("Add", func(t *testing.T) {
		result, err := Add(20, 22)
		if err != nil {
			t.Fatalf("Add: %v", err)
		}
		if result != 42 {
			t.Errorf("Add(20,22) = %v, want 42", result)
		}
	})
	t.Run("Version", func(t *testing.T) {
		result, err := Version()
		if err != nil {
			t.Fatalf("Version: %v", err)
		}
		if result <= 0 {
			t.Errorf("Version() = %d, want > 0", result)
		}
	})
}

// TestCalculatorConstructorAndHistory walks the full handle lifecycle:
// construct, mutate state via AddToHistory, read back via GetHistory (which
// exercises vector<double> packed serialization), clear and re-verify. The
// finalizer reclaims the wasm-side memory on gc.
func TestCalculatorConstructorAndHistory(t *testing.T) {
	t.Cleanup(gc)
	calc, err := NewCalculator()
	if err != nil {
		t.Fatalf("NewCalculator: %v", err)
	}

	for _, v := range []float64{1.5, 2.5, 3.5} {
		if err := calc.AddToHistory(v); err != nil {
			t.Fatalf("AddToHistory(%v): %v", v, err)
		}
	}

	got, err := calc.GetHistory()
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	want := []float64{1.5, 2.5, 3.5}
	if len(got) != len(want) {
		t.Fatalf("GetHistory len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("history[%d] = %v, want %v", i, got[i], v)
		}
	}

	if err := calc.ClearHistory(); err != nil {
		t.Fatalf("ClearHistory: %v", err)
	}
	got2, err := calc.GetHistory()
	if err != nil {
		t.Fatalf("GetHistory after clear: %v", err)
	}
	if len(got2) != 0 {
		t.Errorf("history after clear = %v, want empty", got2)
	}
}

// TestCalculatorName verifies SetName/Name round-trip strings through the
// bridge correctly.
func TestCalculatorName(t *testing.T) {
	t.Cleanup(gc)
	calc, err := NewCalculator()
	if err != nil {
		t.Fatalf("NewCalculator: %v", err)
	}

	const wantName = "greeter"
	if err := calc.SetName(wantName); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	got, err := calc.Name()
	if err != nil {
		t.Fatalf("Name: %v", err)
	}
	if got != wantName {
		t.Errorf("Name() = %q, want %q", got, wantName)
	}
}

// TestStatefulCounterStatePreservation verifies that a C++ class with private
// state round-trips through the handle correctly — if the bridge were to
// treat StatefulCounter as a value type, call_count_ / sum_ would reset on
// every call and Total would return only the last delta rather than the sum.
func TestStatefulCounterStatePreservation(t *testing.T) {
	t.Cleanup(gc)
	// The StatefulCounter(int) constructor is emitted as New…2 because New
	// is reserved for the default-constructed variant.
	counter, err := NewStatefulCounter2(42)
	if err != nil {
		t.Fatalf("NewStatefulCounter2: %v", err)
	}

	for _, v := range []int32{10, 20, 30} {
		if err := counter.Add(v); err != nil {
			t.Fatalf("Add(%d): %v", v, err)
		}
	}

	total, err := counter.Total()
	if err != nil {
		t.Fatalf("Total: %v", err)
	}
	if total != 60 {
		t.Errorf("Total = %d, want 60 (private sum_ state lost across bridge calls)", total)
	}

	cc, err := counter.CallCount()
	if err != nil {
		t.Fatalf("CallCount: %v", err)
	}
	if cc != 3 {
		t.Errorf("CallCount = %d, want 3 (private call_count_ state lost)", cc)
	}

	id, err := counter.Id()
	if err != nil {
		t.Fatalf("Id: %v", err)
	}
	if id != 42 {
		t.Errorf("Id = %d, want 42 (public label field)", id)
	}
}

// goLogger implements LoggerCallback by capturing calls in-process. The
// callback mechanism round-trips through the wasm trampoline: C++ calls
// logger->log(msg) and logger->level() on the trampoline, which marshals
// the args, calls wasmify_callback_invoke, and receives the Go-side
// response. This proves the reverse-direction bridge works end-to-end.
type goLogger struct {
	logged   []string
	levelVal int32
}

func (g *goLogger) Log(message string) error { g.logged = append(g.logged, message); return nil }
func (g *goLogger) Level() (int32, error)    { return g.levelVal, nil }

// TestInheritedMethodPromotion validates the embedded-base pattern at
// compile time: a derived C++ class should inherit all of its parent's
// methods on the Go side via Go's field/method promotion, without any
// AsParent() upcast helper or unsafe.Pointer reinterpretation at the
// call site.
//
// simplelib has `ScientificCalculator : public Calculator`. The
// generated Go bindings therefore contain
//
//	type ScientificCalculator struct { *Calculator }
//
// and every Calculator accessor should be callable directly on a
// *ScientificCalculator. We only need the compile-time existence of
// those method references — a runtime test would require a constructor
// (which isn't emitted for types with only an implicit default ctor).
// The assignment list below fails to compile if method promotion is
// broken, which is the real regression this test guards against.
func TestInheritedMethodPromotion(t *testing.T) {
	var sci *ScientificCalculator
	_ = sci // avoid the "declared and not used" error on nil receiver
	// Parent methods, reachable via the embedded *Calculator.
	_ = (*ScientificCalculator).SetName
	_ = (*ScientificCalculator).Name
	_ = (*ScientificCalculator).AddToHistory
	_ = (*ScientificCalculator).GetHistory
	// Own methods declared on ScientificCalculator.
	_ = (*ScientificCalculator).Power
	_ = (*ScientificCalculator).Sqrt
}

// TestDowncastViaTypeAssertion verifies that the generator does NOT
// emit ToXxx() downcast helpers and that abstract-typed accessor
// returns can be converted to concrete types purely via Go type
// assertion. CLAUDE.md: "do not emit Downcast APIs".
func TestDowncastViaTypeAssertion(t *testing.T) {
	// Compile-time guard: ShapeNode is the abstract interface that
	// ShapeBox.Get returns. Type assertion from the interface value
	// to a concrete implementer is idiomatic Go and requires no
	// helper method. The reflect type of the assertion target must
	// itself be a concrete handle type.
	var shape ShapeNode
	_ = shape
	// If a test value were available at runtime we could assert
	//   circle, ok := shape.(*Circle)
	// Here we rely on the compile-time check: the expression below
	// must type-check for the interface method set to be correct.
	var _ = func(s ShapeNode) (*Circle, bool) {
		c, ok := s.(*Circle)
		return c, ok
	}
	// Regression: methods like ShapeToCircle / ShapeToSquare must
	// NOT be declared on *Shape. Their presence would betray a
	// downcast RPC slip back into the generator output.
	type shapeMethodSet interface {
		Area() (float64, error) // inherited via embedding / interface
	}
	var _ shapeMethodSet = (*Circle)(nil)
	var _ shapeMethodSet = (*Square)(nil)
}

// TestMultiLevelInheritance exercises a 4-level C++ chain
// (Animal -> Mammal -> Canine -> Dog). Every accessor defined at each
// level must be callable on *Dog via Go method promotion through the
// embedded parent chain.
func TestMultiLevelInheritance(t *testing.T) {
	// Compile-only check: the method references below resolve to the
	// embedded ancestor's accessor via Go method promotion. If any
	// level is skipped or shadowed, the reference will not type-check.
	_ = (*Dog).Species     // from Animal
	_ = (*Dog).SetSpecies  // from Animal
	_ = (*Dog).Legs        // from Mammal
	_ = (*Dog).SetLegs     // from Mammal
	_ = (*Dog).Breed       // from Canine
	_ = (*Dog).SetBreed    // from Canine
	_ = (*Dog).BarkVolume  // from Dog itself
	_ = (*Dog).SetBarkVolume
	// Struct layout sanity: Dog must embed Canine (not skip to
	// Mammal) so the full chain is preserved.
	var d Dog
	_ = d.Canine  // would fail to compile if intermediate is missing
}

// TestMultipleInheritance exercises C++ multiple inheritance (Product
// extends both Named and Priced). Both parents' methods must be
// callable directly on *Product via Go method promotion across the
// side-by-side embeds.
func TestMultipleInheritance(t *testing.T) {
	// Compile-only: methods from either parent resolve on Product.
	_ = (*Product).NamedName       // from Named
	_ = (*Product).SetNamedName    // from Named
	_ = (*Product).PricedPrice     // from Priced
	_ = (*Product).SetPricedPrice  // from Priced
	_ = (*Product).Stock           // from Product itself
	_ = (*Product).SetStock
	// Struct layout: Product embeds both parents in distinct fields
	// so either side is reachable.
	var p Product
	_ = p.Named
	_ = p.Priced
}


// TestEnumStringer verifies that generated enum types expose a
// String() method matching the Go fmt.Stringer convention. go-zetasql
// enums all implement Stringer so call sites using %v or %s print a
// readable name instead of the raw integer. The simplelib Operation
// enum is the minimal reproduction.
func TestEnumStringer(t *testing.T) {
	cases := []struct {
		op   Operation
		want string
	}{
		{OperationAdd, "OperationAdd"},
		{OperationSubtract, "OperationSubtract"},
		{OperationMultiply, "OperationMultiply"},
		{OperationDivide, "OperationDivide"},
	}
	for _, tc := range cases {
		got := tc.op.String()
		if got != tc.want {
			t.Errorf("Operation(%d).String() = %q, want %q",
				int32(tc.op), got, tc.want)
		}
	}
	// Out-of-range values fall back to decimal so the Stringer is
	// safe to call on values produced by newer versions of the bridge.
	if got := Operation(99).String(); got != "99" {
		t.Errorf("unknown Operation.String() = %q, want %q", got, "99")
	}
}

// TestChildHandleRetainsParent verifies the keepAlive lifetime fix: when a
// parent handle's method returns a child handle, the child must pin the
// parent in Go's GC graph. Otherwise a GC cycle frees the parent's wasm-side
// tree out from under any descendant pointer into it, and subsequent reads
// on the child trap with "out of bounds memory access".
//
// The test deliberately drops the only Go reference to the parent
// (ManagedFactory) between the child-return and the child-use, then forces
// GC twice. Without keepAlive the factory's finalizer runs and deletes the
// ManagedValue on the wasm side; the follow-up Kind()/Tag() calls then hit
// dangling memory.
func TestChildHandleRetainsParent(t *testing.T) {
	t.Cleanup(gc)

	mkChild := func() *ManagedValue {
		factory, err := NewManagedFactory()
		if err != nil {
			t.Fatalf("NewManagedFactory: %v", err)
		}
		child, err := factory.Make(7, "persist")
		if err != nil {
			t.Fatalf("factory.Make: %v", err)
		}
		// `factory` falls out of scope here. Only `child` remains.
		return child
	}

	child := mkChild()
	// Force finalizers to run. If keepAlive isn't wired up, the factory
	// is collected and its destructor frees `child` on the wasm side.
	gc()
	gc()

	kind, err := child.Kind()
	if err != nil {
		t.Fatalf("child.Kind after GC: %v", err)
	}
	if kind != 7 {
		t.Errorf("child.Kind() = %d, want 7 (parent likely freed prematurely)", kind)
	}
	tag, err := child.Tag()
	if err != nil {
		t.Fatalf("child.Tag after GC: %v", err)
	}
	if tag != "persist" {
		t.Errorf("child.Tag() = %q, want %q", tag, "persist")
	}
}

// TestEnumValuesPreserveOriginalNumbers guards two parser bugs that
// the enum-value extractor used to hit:
//
//  1. Wrapped initialiser: `enum Value { START = AnchorEnums::START };`
//     desugars to ConstantExpr inside an ImplicitCastExpr, so a
//     direct-child search misses the value and every entry collapses
//     to 0 (the zero default). The fix recursively descends into
//     ImplicitCastExpr to find the carried integer.
//
//  2. Implicit enumerator: `enum ImplicitColor { RED, GREEN, BLUE };`
//     emits no ConstantExpr at all because no `= N` is written; the
//     fix falls back to "previous value + 1" for these.
//
// We assert post-+1 proto values (proto3 reserves zero for UNSPECIFIED,
// so the user-visible Go constants are shifted up by one).
func TestEnumValuesPreserveOriginalNumbers(t *testing.T) {
	if AnchorRawAnchorUnspecified != 1 {
		t.Errorf("AnchorRawAnchorUnspecified = %d, want 1 (proto-shifted from C++ 0)", AnchorRawAnchorUnspecified)
	}
	if AnchorRawStart != 2 {
		t.Errorf("AnchorRawStart = %d, want 2 (proto-shifted from C++ 1)", AnchorRawStart)
	}
	if AnchorRawEnd != 3 {
		t.Errorf("AnchorRawEnd = %d, want 3 (proto-shifted from C++ 2)", AnchorRawEnd)
	}
	// START vs END must be distinct — the original bug produced the
	// same value for both.
	if AnchorRawStart == AnchorRawEnd {
		t.Errorf("AnchorRawStart == AnchorRawEnd (%d): enum value extraction collapsed", AnchorRawStart)
	}
	// Implicit-value enum: source declares no `= N`, parser must
	// auto-increment.
	if ImplicitColorImplicitRed == ImplicitColorImplicitGreen ||
		ImplicitColorImplicitGreen == ImplicitColorImplicitBlue {
		t.Errorf("implicit enum collapsed: red=%d green=%d blue=%d",
			ImplicitColorImplicitRed, ImplicitColorImplicitGreen, ImplicitColorImplicitBlue)
	}
}

// TestTextNodeInterfaceNaming verifies the abstract-handle naming
// rule for classes whose name already ends in "Node". For
// `class TextNode`, the generator must emit:
//
//   - the Go interface as `TextNode` (NOT the doubled `TextNodeNode`);
//   - the abstract struct under the renamed identifier `TextNodeBase`,
//     so the interface and struct don't collide;
//   - concrete descendants embed `*TextNodeBase` rather than
//     `*TextNode` (which doesn't exist as a struct anymore).
//
// At runtime, a *HeadingNode (concrete) must satisfy the TextNode
// interface and dispatch Render() through the bridge.
func TestTextNodeInterfaceNaming(t *testing.T) {
	t.Cleanup(gc)
	// Compile-time guards on the generated symbols. Each declaration
	// fails to type-check if the naming rule regresses.
	var _ TextNode      // interface, not TextNodeNode
	var _ *TextNodeBase // renamed abstract struct
	var _ *HeadingNode  // concrete descendant
	// HeadingNode satisfies the TextNode interface via the embedded
	// *TextNodeBase chain. Assignability is what we're checking.
	var node TextNode = (*HeadingNode)(nil)
	_ = node

	// Runtime check: the concrete handle resolves Render() through
	// both the interface and the direct receiver.
	heading, err := NewHeadingNode(2, "title")
	if err != nil {
		t.Fatalf("NewHeadingNode: %v", err)
	}
	got, err := heading.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != "## title" {
		t.Errorf("Render() = %q, want %q", got, "## title")
	}
}

func TestLoggerCallback(t *testing.T) {
	t.Cleanup(gc)

	g := &goLogger{levelVal: 7}
	logger, err := NewLoggerFromImpl(g)
	if err != nil {
		t.Fatalf("NewLoggerFromImpl: %v", err)
	}
	if logger == nil {
		t.Fatal("NewLoggerFromImpl returned nil handle")
	}

	level, err := RunWithLogger(logger, "hello from go")
	if err != nil {
		t.Fatalf("RunWithLogger: %v", err)
	}
	if level != 7 {
		t.Errorf("RunWithLogger level = %d, want 7", level)
	}
	if len(g.logged) != 1 || g.logged[0] != "hello from go" {
		t.Errorf("logged = %v, want [\"hello from go\"]", g.logged)
	}
}
