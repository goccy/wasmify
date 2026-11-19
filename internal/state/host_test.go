package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/wasmify/internal/hostcap"
)

func writeState(t *testing.T, text string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, stateFile), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// wasmify.json's "host" reads into the capability tree, the bridge's
// deprecated flags into the aliases, and the tree comes back from Save.
func TestHostCapabilities(t *testing.T) {
	dir := writeState(t, `{"version": "1", "host": {"network": {"sockets": true}, "process": true, "terminal": true},
		"bridge": {"HostFS": true}}`)
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	host := s.HostTree()
	if !host.Feature(hostcap.Network, "sockets") || host.Feature(hostcap.Network, "resolver") {
		t.Errorf("network: sockets %v resolver %v, want true false",
			host.Feature(hostcap.Network, "sockets"), host.Feature(hostcap.Network, "resolver"))
	}
	if !host.Feature(hostcap.Process, "fork") || !host.Has(hostcap.Terminal) {
		t.Errorf("process: true should give every process feature, terminal: true the terminal")
	}
	if a := s.Bridge.Aliases(); !a.FS || a.Sockets {
		t.Errorf("aliases %+v, want HostFS alone", a)
	}
	if _, err := hostcap.Resolve(host, s.Bridge.Aliases()); err == nil {
		t.Error("the tree with a deprecated flag should not resolve")
	}

	if err := Save(dir, s); err != nil {
		t.Fatal(err)
	}
	back, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := back.HostTree(); !got.Feature(hostcap.Network, "sockets") || got.Feature(hostcap.Network, "resolver") || !got.Feature(hostcap.Process, "threads") {
		t.Errorf("the tree did not come back from Save: %v", got.Macros())
	}

	if (&State{}).HostTree().Has(hostcap.Network) || (*BridgeConfig)(nil).Aliases().Any() {
		t.Error("no host key and no bridge should give nothing")
	}
}

// A name the tree does not have fails the load, saying what the names are.
func TestHostCapabilitiesUnknownName(t *testing.T) {
	dir := writeState(t, `{"version": "1", "host": {"sockets": true}}`)
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), `no capability "sockets"`) {
		t.Errorf("Load: %v, want an error naming the capability", err)
	}
}
