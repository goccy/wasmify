package hostcap

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func parse(t *testing.T, text string) (Set, error) {
	t.Helper()
	var s Set
	err := json.Unmarshal([]byte(text), &s)
	return s, err
}

// A capability given as true has all its features; one given as an object
// has the features set true in it and its base; false and a missing key
// give nothing.
func TestSetFromJSON(t *testing.T) {
	s, err := parse(t, `{"network": true, "process": {"spawn": true, "threads": false}, "filesystem": false}`)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Feature(Network, "sockets") {
		t.Error("network.sockets not given by network: true")
	}
	if !s.Feature(Process, "spawn") || s.Feature(Process, "threads") {
		t.Errorf("process: spawn %v threads %v; want true false",
			s.Feature(Process, "spawn"), s.Feature(Process, "threads"))
	}
	if s.Has(Filesystem) {
		t.Error("filesystem: false gives the capability")
	}
	base, err := parse(t, `{"network": {}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !base.Has(Network) || base.Feature(Network, "sockets") {
		t.Errorf("network: {} gives the base alone, got has %v sockets %v", base.Has(Network), base.Feature(Network, "sockets"))
	}
}

func TestSetFromJSONErrors(t *testing.T) {
	for _, tt := range []struct{ text, want string }{
		{`{"net": true}`, `no capability "net"`},
		{`{"network": {"socket": true}}`, `host.network: no feature "socket"`},
		{`{"network": 1}`, `host.network: want true, false or an object`},
		{`{"network": {"sockets": "yes"}}`, `host.network.sockets: want true or false`},
		{`[]`, `host:`},
	} {
		if _, err := parse(t, tt.text); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: error %v, want one containing %q", tt.text, err, tt.want)
		}
	}
}

// What the JSON says comes back from the JSON written.
func TestSetJSONRoundTrip(t *testing.T) {
	s, err := parse(t, `{"filesystem": true, "process": {"spawn": true}, "network": {}}`)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"filesystem":true,"network":{},"process":{"spawn":true}}`; string(data) != want {
		t.Errorf("marshal: %s, want %s", data, want)
	}
	back, err := parse(t, string(data))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, s) {
		t.Errorf("round trip: %v, want %v", back, s)
	}
}

// The deprecated bridge flags stand for the features they covered; giving
// them and the tree is an error.
func TestResolveAliases(t *testing.T) {
	s, err := Resolve(Set{}, false, Aliases{FS: true, Sockets: true, Subprocess: true, Threads: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range [][2]string{
		{Filesystem, "attributes"}, {Filesystem, "locking"}, {Network, "sockets"}, {Process, "spawn"}, {Process, "threads"},
	} {
		if !s.Feature(f[0], f[1]) {
			t.Errorf("the aliases do not give %s.%s", f[0], f[1])
		}
	}
	if s.Feature(Filesystem, "fifo") {
		t.Errorf("the aliases give more than they covered: %v", s.Macros())
	}
	var tree Set
	tree.Enable(Network, "sockets")
	if _, err := Resolve(tree, true, Aliases{Sockets: true}); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("tree and alias: error %v, want one saying not both", err)
	}
	if _, err := Resolve(Set{}, true, Aliases{Sockets: true}); err == nil {
		t.Error("an empty tree and an alias: no error, want one")
	}
	if got, err := Resolve(tree, true, Aliases{}); err != nil || !got.Feature(Network, "sockets") {
		t.Errorf("tree alone: %v, %v", got, err)
	}
}

func TestMacros(t *testing.T) {
	var s Set
	s.Enable(Network, "sockets")
	s.Enable(Process, "threads")
	want := []string{
		"WASMIFY_HOST_NETWORK", "WASMIFY_HOST_NETWORK_SOCKETS", "WASMIFY_HOST_PROCESS",
		"WASMIFY_HOST_PROCESS_THREADS", "WASMIFY_HOST_SOCKETS", "WASMIFY_HOST_THREADS",
	}
	if got := s.Macros(); !reflect.DeepEqual(got, want) {
		t.Errorf("macros %v, want %v", got, want)
	}
}
