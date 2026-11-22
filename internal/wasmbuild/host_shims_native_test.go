package wasmbuild

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// clearenvDriver runs the shim's clearenv over an environment with entries
// unsetenv cannot remove ("FOO" without '=', "=x" without a name) between
// variables, with the C library and the host import replaced by stand-ins
// that record what the host is told.
const clearenvDriver = `#include <stdio.h>
#include <string.h>
extern char **environ;
int __wrap_clearenv(void);
static char *table[8];
static char told[256];
int __wasmify_host_proc_setenv(const char *name, int name_len, const char *value, int value_len,
                               int has_value) {
    (void)value;
    (void)value_len;
    strncat(told, name, (size_t)name_len);
    strcat(told, has_value ? "=;" : ";");
    return 0;
}
int __real_setenv(const char *name, const char *value, int overwrite) {
    (void)name;
    (void)value;
    (void)overwrite;
    return 0;
}
int __real_putenv(char *string) {
    (void)string;
    return 0;
}
int __real_unsetenv(const char *name) {
    size_t l = strlen(name);
    for (int i = 0; table[i] != NULL;) {
        if (strncmp(table[i], name, l) == 0 && table[i][l] == '=') {
            for (int j = i; table[j] != NULL; j++) table[j] = table[j + 1];
        } else {
            i++;
        }
    }
    return 0;
}
int __real_clearenv(void) {
    table[0] = NULL;
    return 0;
}
int main(void) {
    table[0] = "FOO";
    table[1] = "A=1";
    table[2] = "=x";
    table[3] = "B=2";
    table[4] = NULL;
    environ = table;
    if (__wrap_clearenv() != 0) return 1;
    if (environ != NULL && environ[0] != NULL) return 2;
    fputs(told, stdout);
    return 0;
}
`

// The shim's clearenv, compiled for the machine the test runs on, removes
// every variable it can name, telling the host of each, and leaves the
// entries without a name or without '=' to the C library's clearenv: it
// ends, and the host hears of A and B only. Skipped where there is no C++
// compiler.
func TestClearenvLeavesUnnamedEntriesToLibc(t *testing.T) {
	cxx, err := exec.LookPath("c++")
	if err != nil {
		t.Skipf("no c++: %v", err)
	}
	dir := t.TempDir()
	driver := filepath.Join(dir, "driver.c")
	if err := os.WriteFile(driver, []byte(clearenvDriver), 0o644); err != nil {
		t.Fatal(err)
	}
	shims, err := filepath.Abs("shims")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "clearenv")
	build := exec.Command(cxx, "-w", "-x", "c", driver, "-x", "c++", "-DWASMIFY_HOST_PROCESS_ENVIRONMENT",
		"-I", shims, filepath.Join(shims, "host_process.cc"), "-o", bin)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	out, err := exec.Command(bin).Output()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, want := string(out), "A;B;"; got != want {
		t.Errorf("the host was told %q, want %q", got, want)
	}
}
