package buildjson

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/goccy/wasmify/internal/wrapper"
)

func lookPathOrSkip(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not found on PATH: %v", name, err)
	}
	return p
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	original := &BuildJSON{
		Version: "1.0",
		Project: ProjectInfo{
			Name:        "demo",
			RootDir:     "/tmp/demo",
			BuildType:   "library",
			BuildSystem: "cmake",
		},
		Steps: []BuildStep{
			{
				ID:         1,
				Type:       StepCompile,
				Compiler:   "clang",
				Executable: "/usr/bin/clang",
				Args:       []string{"-c", "foo.c", "-o", "foo.o"},
				WorkDir:    "/tmp/demo",
				InputFiles: []string{"foo.c"},
				OutputFile: "foo.o",
				Language:   "c",
			},
		},
		CapturedAt: "2025-01-01T00:00:00Z",
	}
	if err := Save(dir, original); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "build.json")); err != nil {
		t.Fatalf("build.json not created: %v", err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if loaded == nil {
		t.Fatal("Load() returned nil after Save")
	}
	if loaded.Version != original.Version {
		t.Errorf("Version = %q, want %q", loaded.Version, original.Version)
	}
	if len(loaded.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(loaded.Steps))
	}
	if loaded.Steps[0].Type != StepCompile {
		t.Errorf("Step.Type = %q, want %q", loaded.Steps[0].Type, StepCompile)
	}
}

func TestLoad_NonExistent(t *testing.T) {
	dir := t.TempDir()
	b, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if b != nil {
		t.Errorf("Load() returned %+v for missing file, want nil", b)
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "build.json"), []byte("z"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestClassifyStep(t *testing.T) {
	tests := []struct {
		name string
		tool string
		args []string
		want StepType
	}{
		{"ar archive", "ar", []string{"rcs", "lib.a", "foo.o"}, StepArchive},
		{"ranlib archive", "ranlib", []string{"lib.a"}, StepArchive},
		{"ld link", "ld", []string{"-o", "out", "foo.o"}, StepLink},
		{"strip other", "strip", []string{"binary"}, StepOther},
		{"clang compile", "clang", []string{"-c", "foo.c"}, StepCompile},
		{"clang link no -c", "clang", []string{"foo.o", "-o", "out"}, StepLink},
		{"gcc compile", "gcc", []string{"-c", "foo.c"}, StepCompile},
		{"unknown tool", "ninja", []string{"all"}, StepOther},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyStep(tc.tool, tc.args)
			if got != tc.want {
				t.Errorf("classifyStep(%q, %v) = %q, want %q", tc.tool, tc.args, got, tc.want)
			}
		})
	}
}

func TestDetectLanguage(t *testing.T) {
	tests := []struct {
		name string
		tool string
		args []string
		want string
	}{
		{"x c", "clang", []string{"-x", "c", "input"}, "c"},
		{"x c++", "clang", []string{"-x", "c++", "input"}, "c++"},
		{"x asm", "clang", []string{"-x", "assembler", "input"}, "asm"},
		{"ext .c", "clang", []string{"-c", "foo.c"}, "c"},
		{"ext .cpp", "clang", []string{"-c", "foo.cpp"}, "c++"},
		{"ext .cc", "clang", []string{"-c", "foo.cc"}, "c++"},
		{"ext .cxx", "clang", []string{"-c", "foo.cxx"}, "c++"},
		{"ext .s", "clang", []string{"-c", "foo.s"}, "asm"},
		{"fallback c++ compiler", "clang++", []string{"-c"}, "c++"},
		{"fallback c compiler", "clang", []string{"-c"}, "c"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := detectLanguage(tc.tool, tc.args)
			if got != tc.want {
				t.Errorf("detectLanguage(%q, %v) = %q, want %q", tc.tool, tc.args, got, tc.want)
			}
		})
	}
}

func TestParseFlags_Combined(t *testing.T) {
	args := []string{
		"-Iinc", "-I", "other",
		"-DDEBUG", "-D", "NDEBUG=1",
		"-lm", "-l", "pthread",
		"-L/usr/lib", "-L", "/opt/lib",
		"-std=c++20",
		"-O2",
		"-Wall", "-Wextra",
		"-o", "out",
		"-c",
		"foo.c",
		"-fPIC",
	}
	flags := parseFlags(args)
	wantIncludes := []string{"inc", "other"}
	if !reflect.DeepEqual(flags.IncludePaths, wantIncludes) {
		t.Errorf("IncludePaths = %v, want %v", flags.IncludePaths, wantIncludes)
	}
	wantDefines := []string{"DEBUG", "NDEBUG=1"}
	if !reflect.DeepEqual(flags.Defines, wantDefines) {
		t.Errorf("Defines = %v, want %v", flags.Defines, wantDefines)
	}
	wantLibs := []string{"m", "pthread"}
	if !reflect.DeepEqual(flags.LinkLibs, wantLibs) {
		t.Errorf("LinkLibs = %v, want %v", flags.LinkLibs, wantLibs)
	}
	wantLibPaths := []string{"/usr/lib", "/opt/lib"}
	if !reflect.DeepEqual(flags.LibPaths, wantLibPaths) {
		t.Errorf("LibPaths = %v, want %v", flags.LibPaths, wantLibPaths)
	}
	if flags.Standard != "c++20" {
		t.Errorf("Standard = %q, want c++20", flags.Standard)
	}
	if flags.Optimization != "2" {
		t.Errorf("Optimization = %q, want 2", flags.Optimization)
	}
	wantWarnings := []string{"all", "extra"}
	if !reflect.DeepEqual(flags.Warnings, wantWarnings) {
		t.Errorf("Warnings = %v, want %v", flags.Warnings, wantWarnings)
	}
	if !contains(flags.OtherFlags, "-fPIC") {
		t.Errorf("OtherFlags = %v, expected -fPIC", flags.OtherFlags)
	}
}

func TestExtractOutputFile(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"-c", "foo.c", "-o", "foo.o"}, "foo.o"},
		{[]string{"-c", "foo.c"}, ""},
		{[]string{"-o"}, ""},
	}
	for _, tc := range tests {
		got := extractOutputFile(tc.args)
		if got != tc.want {
			t.Errorf("extractOutputFile(%v) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

func TestExtractInputFiles(t *testing.T) {
	args := []string{
		"-c", "foo.c", "-o", "foo.o",
		"bar.cpp", "obj1.o",
		"-I", "inc",
		"-D", "X",
		"-L", "lib",
		"-l", "m",
		"-x", "c++",
		"-isystem", "/sys",
		"-MF", "dep.d",
		"-MT", "target",
		"-MQ", "q",
	}
	got := extractInputFiles(args)
	want := []string{"foo.c", "bar.cpp", "obj1.o"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("extractInputFiles = %v, want %v", got, want)
	}
}

func TestIsSourceFile(t *testing.T) {
	cases := map[string]bool{
		"foo.c":   true,
		"foo.cpp": true,
		"foo.CC":  true,
		"foo.cxx": true,
		"foo.s":   true,
		"foo.asm": true,
		"foo.m":   true,
		"foo.mm":  true,
		"foo.o":   false,
		"foo.h":   false,
		"foo":     false,
	}
	for name, want := range cases {
		if got := isSourceFile(name); got != want {
			t.Errorf("isSourceFile(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestIsObjectFile(t *testing.T) {
	cases := map[string]bool{
		"foo.o":     true,
		"foo.obj":   true,
		"foo.a":     true,
		"foo.so":    true,
		"foo.dylib": true,
		"foo.c":     false,
		"foo":       false,
	}
	for name, want := range cases {
		if got := isObjectFile(name); got != want {
			t.Errorf("isObjectFile(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestNormalize(t *testing.T) {
	entries := []wrapper.LogEntry{
		{
			Tool:       "clang",
			Executable: "/usr/bin/clang",
			Args:       []string{"-c", "foo.c", "-o", "foo.o", "-I/inc", "-DX", "-std=c11", "-O2"},
			WorkDir:    "/tmp",
		},
		{
			Tool:       "ar",
			Executable: "/usr/bin/ar",
			Args:       []string{"rcs", "lib.a", "foo.o"},
			WorkDir:    "/tmp",
		},
		{
			Tool:       "clang++",
			Executable: "/usr/bin/clang++",
			Args:       []string{"foo.o", "-o", "binary"},
			WorkDir:    "/tmp",
		},
	}
	steps := Normalize(entries)
	if len(steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(steps))
	}
	if steps[0].ID != 1 || steps[1].ID != 2 || steps[2].ID != 3 {
		t.Errorf("IDs not sequential: %d %d %d", steps[0].ID, steps[1].ID, steps[2].ID)
	}
	if steps[0].Type != StepCompile {
		t.Errorf("step[0].Type = %q, want compile", steps[0].Type)
	}
	if steps[0].Language != "c" {
		t.Errorf("step[0].Language = %q, want c", steps[0].Language)
	}
	if steps[0].OutputFile != "foo.o" {
		t.Errorf("step[0].OutputFile = %q, want foo.o", steps[0].OutputFile)
	}
	if !contains(steps[0].InputFiles, "foo.c") {
		t.Errorf("step[0].InputFiles = %v, want to contain foo.c", steps[0].InputFiles)
	}
	if steps[0].Flags.Standard != "c11" {
		t.Errorf("step[0].Flags.Standard = %q, want c11", steps[0].Flags.Standard)
	}
	if steps[1].Type != StepArchive {
		t.Errorf("step[1].Type = %q, want archive", steps[1].Type)
	}
	// ar is not a compiler, so no language detection
	if steps[1].Language != "" {
		t.Errorf("step[1].Language = %q, want empty", steps[1].Language)
	}
	if steps[2].Type != StepLink {
		t.Errorf("step[2].Type = %q, want link", steps[2].Type)
	}
	if steps[2].Language != "c++" {
		t.Errorf("step[2].Language = %q, want c++", steps[2].Language)
	}
}

func TestFilterBuildArgs(t *testing.T) {
	args := []string{
		"-fPIC",
		"-fmodules-strict-decluse",
		"-Wprivate-header",
		"-Xclang", "-fmodule-map-file=/x.map",
		"-Xclang", "-fmodule-name=foo",
		"-Xclang", "-fno-cxx-modules",
		"-O2",
		"-Xclang", "-other-flag",
	}
	got := filterBuildArgs(args)
	want := []string{"-fPIC", "-O2", "-Xclang", "-other-flag"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("filterBuildArgs =\n  %v\nwant\n  %v", got, want)
	}
}

func TestOutputsExist(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "out.o")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !outputsExist([]string{"out.o"}, dir) {
		t.Error("outputsExist(existing relative) = false, want true")
	}
	if !outputsExist([]string{existing}, dir) {
		t.Error("outputsExist(existing absolute) = false, want true")
	}
	if outputsExist([]string{"nope.o"}, dir) {
		t.Error("outputsExist(missing) = true, want false")
	}
	if outputsExist(nil, dir) {
		t.Error("outputsExist(nil) = true, want false")
	}
}

func TestMakeWritable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ro")
	if err := os.WriteFile(path, []byte("x"), 0o444); err != nil {
		t.Fatal(err)
	}
	// Should not panic even on missing files.
	makeWritable(filepath.Join(dir, "nope"))
	makeWritable(path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o200 == 0 {
		t.Errorf("makeWritable did not add owner-write: mode=%v", info.Mode().Perm())
	}
}

func TestFindOutputFiles(t *testing.T) {
	dir := t.TempDir()
	paramsFile := filepath.Join(dir, "params.rsp")
	if err := os.WriteFile(paramsFile, []byte("-o\nout-from-params.o\nother\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	step := &BuildStep{
		OutputFile: "explicit.o",
		Args:       []string{"-o", "from-args.o", "-MF", "dep.d", "@" + paramsFile},
		WorkDir:    dir,
	}
	got := findOutputFiles(step)

	// Expect OutputFile + -o arg + -MF arg + params file output
	expected := map[string]bool{
		"explicit.o":         true,
		"from-args.o":        true,
		"dep.d":              true,
		"out-from-params.o":  true,
	}
	for _, o := range got {
		delete(expected, o)
	}
	if len(expected) > 0 {
		t.Errorf("findOutputFiles missing entries: %v (got %v)", expected, got)
	}
}

func TestParseParamsFileOutputs(t *testing.T) {
	dir := t.TempDir()

	// Valid params file with -o entry
	p := filepath.Join(dir, "ok.rsp")
	if err := os.WriteFile(p, []byte("-c\nfoo.c\n-o\nfoo.o\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := parseParamsFileOutputs(p)
	if len(got) != 1 || got[0] != "foo.o" {
		t.Errorf("parseParamsFileOutputs = %v, want [foo.o]", got)
	}

	// Missing file returns nil
	if out := parseParamsFileOutputs(filepath.Join(dir, "missing.rsp")); out != nil {
		t.Errorf("parseParamsFileOutputs(missing) = %v, want nil", out)
	}
}

func TestFindOutputFiles_RelativeParamsFile(t *testing.T) {
	dir := t.TempDir()
	paramsFile := filepath.Join(dir, "params.rsp")
	if err := os.WriteFile(paramsFile, []byte("-o\nrel.o\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	step := &BuildStep{
		Args:    []string{"@params.rsp"},
		WorkDir: dir,
	}
	got := findOutputFiles(step)
	if len(got) != 1 || got[0] != "rel.o" {
		t.Errorf("findOutputFiles relative params = %v, want [rel.o]", got)
	}
}

func TestExecute_SkipsWhenOutputsExist(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "done.o")
	if err := os.WriteFile(out, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &BuildJSON{
		Steps: []BuildStep{
			{
				ID:         1,
				Type:       StepCompile,
				Executable: "/this/does/not/exist", // would fail if actually invoked
				Args:       []string{"-o", out},
				WorkDir:    dir,
				OutputFile: out,
			},
		},
	}
	res, err := Execute(b)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if res.Cached != 1 || res.Succeeded != 0 || res.Failed != 0 || res.Total != 1 {
		t.Errorf("Execute result = %+v, want Cached=1 Total=1", res)
	}
}

func TestExecute_FailingStep(t *testing.T) {
	dir := t.TempDir()
	falseBin := lookPathOrSkip(t, "false")
	b := &BuildJSON{
		Steps: []BuildStep{
			{
				ID:         1,
				Type:       StepCompile,
				Executable: falseBin,
				Args:       []string{},
				WorkDir:    dir,
				// No output file, so the cache won't short-circuit.
			},
		},
	}
	res, err := Execute(b)
	if err == nil {
		t.Error("expected error from failing step")
	}
	if res.Failed != 1 {
		t.Errorf("Execute result Failed = %d, want 1", res.Failed)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
