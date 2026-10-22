package simplelib_proto_wasm2go

import (
	"os"
	"runtime"
	"sync"
	"testing"
	"time"
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

// blockingLogger implements LoggerCallback. The first callback it
// receives closes entered and then blocks until release is closed.
// When reenter is set, that callback also calls back into the module
// on its own goroutine before blocking.
type blockingLogger struct {
	LoggerCallbackDefaults
	level   int32
	reenter bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	sum     float64
	err     error
}

func (l *blockingLogger) block() {
	l.once.Do(func() {
		if l.reenter {
			l.sum, l.err = Add(20, 22)
		}
		close(l.entered)
		<-l.release
	})
}

func (l *blockingLogger) Level() (int32, error) { l.block(); return l.level, nil }

func (l *blockingLogger) Log(string) error { l.block(); return nil }

// TestCallbackSerialisesAcrossGoroutines pins the shadow-stack
// invariant for reentrant callbacks. Every call into the module shares
// one C stack, so while goroutine A's call is suspended inside a Go
// callback, goroutine B's call must not start: if B suspended in a
// callback of its own and A then resumed and returned first, the stack
// pointer would rewind past B's live C++ frames and the next call would
// overwrite them. A's handler may still re-enter the module on its own
// goroutine.
func TestCallbackSerialisesAcrossGoroutines(t *testing.T) {
	t.Cleanup(gc)
	newLogger := func(l *blockingLogger) *Logger {
		t.Helper()
		h, err := NewLoggerFromImpl(l)
		if err != nil {
			t.Fatalf("NewLoggerFromImpl: %v", err)
		}
		return h
	}
	a := &blockingLogger{level: 1, reenter: true, entered: make(chan struct{}), release: make(chan struct{})}
	b := &blockingLogger{level: 2, entered: make(chan struct{}), release: make(chan struct{})}
	close(b.release)
	loggerA, loggerB := newLogger(a), newLogger(b)

	type result struct {
		level int32
		err   error
	}
	run := func(logger *Logger, msg string) <-chan result {
		ch := make(chan result, 1)
		go func() {
			level, err := RunWithLogger(logger, msg)
			ch <- result{level, err}
		}()
		return ch
	}

	doneA := run(loggerA, "a")
	<-a.entered
	if a.err != nil || a.sum != 42 {
		t.Fatalf("reentrant Add from A's handler = %v, %v; want 42, nil", a.sum, a.err)
	}

	doneB := run(loggerB, "b")
	select {
	case <-b.entered:
		t.Fatal("B entered its callback while A was suspended in one")
	case <-time.After(200 * time.Millisecond):
	}

	close(a.release)
	if r := <-doneA; r.err != nil || r.level != 1 {
		t.Fatalf("RunWithLogger(A) = %d, %v; want 1, nil", r.level, r.err)
	}
	if r := <-doneB; r.err != nil || r.level != 2 {
		t.Fatalf("RunWithLogger(B) = %d, %v; want 2, nil", r.level, r.err)
	}
}
