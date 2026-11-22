// Package hostcap is what a wasm build is given of the host it runs on:
// the capabilities of a POSIX system that WASI preview1 does not have,
// which wasmify's host shims implement on host imports: of the module
// "wasip1_posix_extension" for the file system, of "wasi_snapshot_preview1"
// for the network and processes (wasmify.json's "host").
//
// The capabilities follow the option groups of POSIX.1-2017 (Annex E),
// each with the features that belong inside it:
//
//	filesystem  {attributes, fifo, locking}
//	network     {sockets}
//	process     {spawn, threads}
//
// A capability is given as true (with all its features) or as an object of
// the features it is given (a feature left out is not). A capability that
// is given at all also brings what it is without any feature (its base).
package hostcap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Capability names.
const (
	Filesystem = "filesystem"
	Network    = "network"
	Process    = "process"
)

// capabilityFeatures is a capability of the schema and its features.
type capabilityFeatures struct {
	name     string
	features []string
}

// The features of each capability, in the order of the schema.
var schema = []capabilityFeatures{
	{Filesystem, []string{"attributes", "fifo", "locking"}},
	{Network, []string{"sockets"}},
	{Process, []string{"spawn", "threads"}},
}

func features(capability string) ([]string, bool) {
	i := slices.IndexFunc(schema, func(c capabilityFeatures) bool { return c.name == capability })
	if i < 0 {
		return nil, false
	}
	return schema[i].features, true
}

func capabilityNames() string {
	names := make([]string, len(schema))
	for i, c := range schema {
		names[i] = c.name
	}
	return strings.Join(names, ", ")
}

// Set is the capabilities a build is given. The zero Set gives none.
type Set struct {
	// caps holds every capability given, with the features given of it.
	caps map[string]map[string]bool
}

// Enable gives the capability and the named features of it (none: the
// capability's base alone). It panics on a name the schema does not have:
// the names are wasmify's own.
func (s *Set) Enable(capability string, feats ...string) {
	known, ok := features(capability)
	if !ok {
		panic("hostcap: no capability " + capability)
	}
	if s.caps == nil {
		s.caps = map[string]map[string]bool{}
	}
	if s.caps[capability] == nil {
		s.caps[capability] = map[string]bool{}
	}
	for _, f := range feats {
		if !slices.Contains(known, f) {
			panic("hostcap: no feature " + capability + "." + f)
		}
		s.caps[capability][f] = true
	}
}

// EnableAll gives every capability with every feature.
func (s *Set) EnableAll() {
	for _, c := range schema {
		s.Enable(c.name, c.features...)
	}
}

// Has reports whether the capability is given (with or without features).
func (s Set) Has(capability string) bool {
	_, ok := s.caps[capability]
	return ok
}

// Feature reports whether the feature of the capability is given.
func (s Set) Feature(capability, feature string) bool {
	return s.caps[capability][feature]
}

// Empty reports whether no capability is given.
func (s Set) Empty() bool { return len(s.caps) == 0 }

// Macros are the -D macros of the set, sorted: WASMIFY_HOST_<CAPABILITY>
// for each capability given and WASMIFY_HOST_<CAPABILITY>_<FEATURE> for
// each feature, and the macros of the deprecated bridge flags
// (WASMIFY_HOST_FS, WASMIFY_HOST_SOCKETS, WASMIFY_HOST_SUBPROCESS,
// WASMIFY_HOST_THREADS) for the features they stand for, which a
// project's own sources may test.
func (s Set) Macros() []string {
	var out []string
	for _, c := range schema {
		if !s.Has(c.name) {
			continue
		}
		out = append(out, "WASMIFY_HOST_"+strings.ToUpper(c.name))
		for _, f := range c.features {
			if s.Feature(c.name, f) {
				out = append(out, "WASMIFY_HOST_"+strings.ToUpper(c.name)+"_"+strings.ToUpper(f))
			}
		}
	}
	if s.Feature(Filesystem, "attributes") {
		out = append(out, "WASMIFY_HOST_FS")
	}
	if s.Feature(Network, "sockets") {
		out = append(out, "WASMIFY_HOST_SOCKETS")
	}
	if s.Feature(Process, "spawn") {
		out = append(out, "WASMIFY_HOST_SUBPROCESS")
	}
	if s.Feature(Process, "threads") {
		out = append(out, "WASMIFY_HOST_THREADS")
	}
	sort.Strings(out)
	return out
}

// UnmarshalJSON reads wasmify.json's "host": an object whose keys are
// capabilities, each true, false or an object of features (each true or
// false). A name the schema does not have is an error.
func (s *Set) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("host: %w", err)
	}
	var out Set
	for name, value := range raw {
		known, ok := features(name)
		if !ok {
			return fmt.Errorf("host: no capability %q (the capabilities are %s)", name, capabilityNames())
		}
		var v any
		if err := json.Unmarshal(value, &v); err != nil {
			return fmt.Errorf("host.%s: %w", name, err)
		}
		switch v := v.(type) {
		case bool:
			if v {
				out.Enable(name, known...)
			}
		case map[string]any:
			out.Enable(name)
			for f, fv := range v {
				if !slices.Contains(known, f) {
					return fmt.Errorf("host.%s: no feature %q (its features are %s)", name, f, strings.Join(known, ", "))
				}
				on, ok := fv.(bool)
				if !ok {
					return fmt.Errorf("host.%s.%s: want true or false", name, f)
				}
				if on {
					out.Enable(name, f)
				}
			}
		default:
			return fmt.Errorf("host.%s: want true, false or an object of its features (%s)", name, strings.Join(known, ", "))
		}
	}
	*s = out
	return nil
}

// MarshalJSON writes a capability with all its features as true, and any
// other as the object of the features it is given.
func (s Set) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	first := true
	for _, c := range schema {
		if !s.Has(c.name) {
			continue
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		fmt.Fprintf(&b, "%q:", c.name)
		if !slices.ContainsFunc(c.features, func(f string) bool { return !s.Feature(c.name, f) }) {
			b.WriteString("true")
			continue
		}
		b.WriteByte('{')
		firstFeature := true
		for _, f := range c.features {
			if !s.Feature(c.name, f) {
				continue
			}
			if !firstFeature {
				b.WriteByte(',')
			}
			firstFeature = false
			fmt.Fprintf(&b, "%q:true", f)
		}
		b.WriteByte('}')
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// Aliases are the deprecated bridge flags of wasmify.json (HostFS,
// HostSockets, HostSubprocess, HostThreads) and their WASMIFY_HOST_FS,
// WASMIFY_HOST_SOCKETS, WASMIFY_HOST_SUBPROCESS and WASMIFY_HOST_THREADS
// environment variables. Each stands for what it covered before the tree.
type Aliases struct {
	FS, Sockets, Subprocess, Threads bool
}

// Any reports whether an alias is set.
func (a Aliases) Any() bool { return a.FS || a.Sockets || a.Subprocess || a.Threads }

// Set is what the aliases stand for: HostFS the file system with its
// attributes and locks; HostSockets the network's sockets; HostSubprocess
// process.spawn; HostThreads process.threads.
func (a Aliases) Set() Set {
	var s Set
	if a.FS {
		s.Enable(Filesystem, "attributes", "locking")
	}
	if a.Sockets {
		s.Enable(Network, "sockets")
	}
	if a.Subprocess {
		s.Enable(Process, "spawn")
	}
	if a.Threads {
		s.Enable(Process, "threads")
	}
	return s
}

// Resolve is the capabilities of a build: the tree, or what the aliases
// stand for. given is whether wasmify.json has the "host" key (an empty
// tree included); giving it and an alias is an error.
func Resolve(tree Set, given bool, aliases Aliases) (Set, error) {
	if given && aliases.Any() {
		return Set{}, fmt.Errorf("host: give the capabilities either as wasmify.json's \"host\" or by the deprecated HostFS/HostSockets/HostSubprocess/HostThreads flags (or their WASMIFY_HOST_* variables), not both")
	}
	if aliases.Any() {
		return aliases.Set(), nil
	}
	return tree, nil
}
