package wasmbuild

import (
	"testing"

	"github.com/goccy/wasmify/internal/buildjson"
)

// TestResolveObjectRefs_SameWorkDirBasenameCollision models CPython's
// libpython3.14.a: ONE work dir compiles Python/gc.c and
// Modules/_testcapi/gc.c (and Python/context.c next to
// Modules/_decimal/libmpdec/context.c), and the archive lists both gc.o
// members. Each reference already carries its nested obj/ path, so it must
// resolve to THAT producer — not to whichever same-named object from the same
// work dir the registry saw first. Getting this wrong archived the test
// module's gc.o twice, dropped the interpreter's, and the final link imported
// _PyObject_GC_New from the host.
func TestResolveObjectRefs_SameWorkDirBasenameCollision(t *testing.T) {
	const wd = "/src/cpython/cross-build/wasm32-wasip1"
	steps := []WasmBuildStep{
		{ID: 1, Type: buildjson.StepCompile, WorkDir: wd,
			OutputFile: "/build/obj/cpython/cross-build/wasm32-wasip1/Modules/_testcapi/gc.o"},
		{ID: 2, Type: buildjson.StepCompile, WorkDir: wd,
			OutputFile: "/build/obj/cpython/cross-build/wasm32-wasip1/Python/gc.o"},
		{ID: 3, Type: buildjson.StepCompile, WorkDir: wd,
			OutputFile: "/build/obj/cpython/cross-build/wasm32-wasip1/Modules/_decimal/libmpdec/context.o"},
		{ID: 4, Type: buildjson.StepCompile, WorkDir: wd,
			OutputFile: "/build/obj/cpython/cross-build/wasm32-wasip1/Python/context.o"},
		{ID: 5, Type: buildjson.StepArchive, WorkDir: wd,
			Args: []string{"rcs", "/build/lib/cpython/cross-build/wasm32-wasip1/libpython3.14.a",
				"/build/obj/cpython/cross-build/wasm32-wasip1/Python/context.o",
				"/build/obj/cpython/cross-build/wasm32-wasip1/Python/gc.o",
				"/build/obj/cpython/cross-build/wasm32-wasip1/Modules/_testcapi/gc.o"}},
		{ID: 6, Type: buildjson.StepArchive, WorkDir: wd,
			Args: []string{"rcs", "/build/lib/cpython/cross-build/wasm32-wasip1/Modules/_decimal/libmpdec/libmpdec.a",
				"/build/obj/cpython/cross-build/wasm32-wasip1/Modules/_decimal/libmpdec/context.o"}},
	}
	resolveObjectRefs(steps)

	want := []string{
		"/build/obj/cpython/cross-build/wasm32-wasip1/Python/context.o",
		"/build/obj/cpython/cross-build/wasm32-wasip1/Python/gc.o",
		"/build/obj/cpython/cross-build/wasm32-wasip1/Modules/_testcapi/gc.o",
	}
	for i, w := range want {
		if got := steps[4].Args[2+i]; got != w {
			t.Errorf("libpython member %d = %q, want %q", i, got, w)
		}
	}
	if got := steps[5].Args[2]; got != "/build/obj/cpython/cross-build/wasm32-wasip1/Modules/_decimal/libmpdec/context.o" {
		t.Errorf("libmpdec context.o = %q, want the libmpdec object", got)
	}

	// A cross-work-dir reference (flattened, no exact producer) still picks
	// the same-work-dir producer whose path agrees best with the reference.
	other := []WasmBuildStep{
		steps[0], steps[1],
		{ID: 7, Type: buildjson.StepArchive, WorkDir: wd,
			Args: []string{"rcs", "/build/lib/x/libx.a", "/build/obj/x/Python/gc.o"}},
	}
	resolveObjectRefs(other)
	if got := other[2].Args[2]; got != "/build/obj/cpython/cross-build/wasm32-wasip1/Python/gc.o" {
		t.Errorf("suffix-ranked ref = %q, want the Python/gc.o producer", got)
	}
}

func TestCommonPathSuffix(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"gc.o", "Python/gc.o", 1},
		{"Python/gc.o", "/build/obj/ns/Python/gc.o", 2},
		{"Modules/_testcapi/gc.o", "/build/obj/ns/Python/gc.o", 1},
		{"a.o", "b.o", 0},
	}
	for _, c := range cases {
		if got := commonPathSuffix(c.a, c.b); got != c.want {
			t.Errorf("commonPathSuffix(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
