package wasmbuild

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/goccy/wasmify/internal/hostcap"
)

// hostWith returns the capability set of the parts named "capability" (its
// base) or "capability.feature".
func hostWith(parts ...string) hostcap.Set {
	var s hostcap.Set
	for _, p := range parts {
		capability, feature, _ := strings.Cut(p, ".")
		if feature == "" {
			s.Enable(capability)
		} else {
			s.Enable(capability, feature)
		}
	}
	return s
}

// partName names a part as hostWith takes it.
func partName(p hostPart) string {
	if p.feature == "" {
		return p.capability
	}
	return p.capability + "." + p.feature
}

// The linker only reports a --wrap flag without a __wrap_ definition, or a
// __wrap_ definition without its flag (the wrapper is then dead code and
// libc's version stays in use), when a real project is linked. Both sides
// are written by hand in different files, so they are held together here:
// every function a part wraps has its __wrap_ and __real_ in a shim of the
// part, and each part's wraps reach the link exactly when it is given.
func TestHostWrapsMatchShims(t *testing.T) {
	const flagPrefix = "-Wl,--wrap="
	for _, p := range hostParts {
		var shims [][]byte
		for _, name := range p.shims {
			data, err := hostShim(name)
			if err != nil {
				t.Fatal(err)
			}
			shims = append(shims, data)
		}
		for _, wrap := range p.wraps {
			for _, symbol := range []string{"__wrap_" + wrap + "(", "__real_" + wrap + "("} {
				if !slices.ContainsFunc(shims, func(shim []byte) bool { return bytes.Contains(shim, []byte(symbol)) }) {
					t.Errorf("%s wraps %s but none of its shims %v has %s", partName(p), wrap, p.shims, strings.TrimSuffix(symbol, "("))
				}
			}
		}
		cfg := WasmConfig{Host: hostWith(partName(p))}
		var got []string
		for _, flag := range cfg.EffectiveExtraLDFlags() {
			if name, ok := strings.CutPrefix(flag, flagPrefix); ok {
				got = append(got, name)
			}
		}
		// a feature brings its capability's base and its wraps
		want := append([]string(nil), hostWraps(hostWith(p.capability))...)
		for _, w := range p.wraps {
			if !slices.Contains(want, w) {
				want = append(want, w)
			}
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: wraps %v, want %v", partName(p), got, want)
		}
	}
	for _, flag := range (WasmConfig{}).EffectiveExtraLDFlags() {
		if strings.HasPrefix(flag, flagPrefix) {
			t.Errorf("%s is passed to the linker with no host capability", flag)
		}
	}
}

// Every shim a part names exists, and every shim is some part's.
func TestHostPartsNameTheShims(t *testing.T) {
	entries, err := hostShimFS.ReadDir("shims")
	if err != nil {
		t.Fatal(err)
	}
	named := map[string]bool{}
	for _, p := range hostParts {
		for _, name := range p.shims {
			named[name] = true
			if !slices.Contains(hostShimOrder, name) {
				t.Errorf("%s names %s, which hostShimOrder does not have", partName(p), name)
			}
		}
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".cc") && !named[e.Name()] {
			t.Errorf("shim %s is no part's", e.Name())
		}
	}
}

func TestDeployHostShims(t *testing.T) {
	buildDir := t.TempDir()
	paths, err := DeployHostShims(buildDir, hostcap.Set{})
	if err != nil || len(paths) != 0 {
		t.Fatalf("no capability: %v, %v; want nothing", paths, err)
	}
	if _, err := os.Stat(filepath.Join(buildDir, "host-shims")); !os.IsNotExist(err) {
		t.Fatalf("host-shims dir should not exist when no capability is given")
	}

	for _, tt := range []struct {
		host      hostcap.Set
		wantShims []string
	}{
		{hostWith("network.sockets"), []string{"host_sockets.cc"}},
		{hostWith("process.spawn"), []string{"host_subprocess.cc"}},
		{hostWith("filesystem.attributes"), []string{"host_fs.cc"}},
		{hostWith("process.threads"), nil},
	} {
		buildDir := t.TempDir()
		paths, err := DeployHostShims(buildDir, tt.host)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, p := range paths {
			got = append(got, filepath.Base(p))
			if _, err := os.Stat(p); err != nil {
				t.Errorf("shim %s not on disk: %v", p, err)
			}
		}
		var want []string
		for _, name := range hostShimOrder {
			if slices.Contains(tt.wantShims, name) {
				want = append(want, name)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%v: shims %v, want %v", tt.host.Macros(), got, want)
		}
	}
}

// The headers of each part go into host-include; a header no given part
// needs is not there.
func TestDeployHostHeaders(t *testing.T) {
	if dir, err := DeployHostHeaders(t.TempDir(), hostcap.Set{}); err != nil || dir != "" {
		t.Fatalf("no capability: %q, %v; want nothing", dir, err)
	}
	buildDir := t.TempDir()
	incDir, err := DeployHostHeaders(buildDir, hostWith("process.spawn"))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(buildDir, HostIncludeSubdir); incDir != want {
		t.Fatalf("include dir %q, want %q", incDir, want)
	}
	for _, header := range []string{"spawn.h", "sys/wait.h"} {
		if _, err := os.Stat(filepath.Join(incDir, header)); err != nil {
			t.Errorf("%s not deployed: %v", header, err)
		}
	}
	for _, header := range []string{"netdb.h"} {
		if _, err := os.Stat(filepath.Join(incDir, header)); err == nil {
			t.Errorf("%s deployed without the part that needs it", header)
		}
	}
}

// The deprecated WASMIFY_HOST_* variables set the aliases, which
// ResolveHost turns into the capabilities they stand for.
func TestApplyEnvOverridesHostCapabilities(t *testing.T) {
	var def WasmConfig
	def.ApplyEnvOverrides()
	if def.HostAliases.Any() {
		t.Fatalf("defaults should be off, got %+v", def.HostAliases)
	}

	t.Setenv("WASMIFY_HOST_SOCKETS", "yes")
	var env WasmConfig
	env.ApplyEnvOverrides()
	if err := env.ResolveHost(); err != nil {
		t.Fatal(err)
	}
	if !env.Host.Feature(hostcap.Network, "sockets") {
		t.Fatalf("WASMIFY_HOST_SOCKETS should give network.sockets, got %v", env.Host.Macros())
	}
	if env.Host.Has(hostcap.Process) {
		t.Fatalf("only sockets was set, process should stay off")
	}

	// an alias from the environment and a tree from wasmify.json
	tree := WasmConfig{Host: hostWith("process.spawn"), HostGiven: true}
	tree.ApplyEnvOverrides()
	if err := tree.ResolveHost(); err == nil {
		t.Fatalf("a tree with an alias from the environment should be refused")
	}
}
