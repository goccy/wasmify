package simplelib_proto_wasm2go

import (
	"os"
	"runtime"
	"testing"
)

// TestMain initialises the wasm2go-backed bridge once for the package.
// Unlike the wazero counterpart there is no engine to release at exit,
// so no Close() call is needed.
func TestMain(m *testing.M) {
	if err := Init(); err != nil {
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// gc forces garbage collection so wasm-side handles released via
// runtime.SetFinalizer are freed between tests in the shared module.
func gc() {
	runtime.GC()
	runtime.GC()
}

// TestSimplelibFreeFunctions exercises the free-function service: Add and
// Version round-trip through the wasm2go-backed bridge without any handle
// involvement.
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

// TestCalculatorConstructorAndHistory walks the full handle lifecycle —
// construct, mutate state via AddToHistory, read back via GetHistory (which
// exercises vector<double> packed serialization), clear, and re-verify.
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
