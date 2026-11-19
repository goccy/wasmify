// Package hostcap is what a wasm build is given of the host it runs on:
// the capabilities of a POSIX system that WASI preview1 does not have,
// which wasmify's host shims implement on imports of the module
// "wasmify_posix" (wasmify.json's "host").
//
// The capabilities follow the option groups of POSIX.1-2017 (Annex E),
// each with the features that belong inside it:
//
//	filesystem  {attributes, fifo, locking}
//	network     {sockets, resolver}
//	process     {spawn, fork, job_control, resources, threads}
//	signals     {timers}
//	terminal    {pseudo}
//	users       {database}
//	ipc         {sysv}
//	system      {logging, info, time_zones}
//
// A capability is given as true (with all its features) or as an object of
// the features it is given (a feature left out is not). A capability that
// is given at all also brings what it is without any feature (its base:
// for signals the signal layer, for terminal termios, ...). A feature that
// needs another capability or feature says so (Validate).
package hostcap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Capability names.
const (
	Filesystem = "filesystem"
	Network    = "network"
	Process    = "process"
	Signals    = "signals"
	Terminal   = "terminal"
	Users      = "users"
	IPC        = "ipc"
	System     = "system"
)

// The features of each capability, in the order of the schema.
var schema = []struct {
	name     string
	features []string
}{
	{Filesystem, []string{"attributes", "fifo", "locking"}},
	{Network, []string{"sockets", "resolver"}},
	{Process, []string{"spawn", "fork", "job_control", "resources", "threads"}},
	{Signals, []string{"timers"}},
	{Terminal, []string{"pseudo"}},
	{Users, []string{"database"}},
	{IPC, []string{"sysv"}},
	{System, []string{"logging", "info", "time_zones"}},
}

func features(capability string) ([]string, bool) {
	for _, c := range schema {
		if c.name == capability {
			return c.features, true
		}
	}
	return nil, false
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
		if !contains(known, f) {
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

// Validate refuses a set in which a feature lacks what it needs.
func (s Set) Validate() error {
	var errs []string
	if s.Feature(Process, "job_control") && !s.Has(Terminal) {
		errs = append(errs, "process.job_control needs terminal (tcgetpgrp and tcsetpgrp are functions of a terminal)")
	}
	if s.Feature(Process, "fork") && !s.Feature(Process, "spawn") {
		errs = append(errs, "process.fork needs process.spawn (a forked child is waited for with waitpid)")
	}
	if s.Has(Signals) && !s.Has(Process) {
		errs = append(errs, "signals needs process (kill tells a signal to the process itself by the process's pid)")
	}
	if s.Feature(System, "logging") && !s.Has(Process) {
		errs = append(errs, "system.logging needs process (LOG_PID writes the process's pid)")
	}
	if len(errs) > 0 {
		return fmt.Errorf("host: %s", strings.Join(errs, "; "))
	}
	return nil
}

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
		var on bool
		if err := json.Unmarshal(value, &on); err == nil {
			if on {
				out.Enable(name, known...)
			}
			continue
		}
		var feats map[string]json.RawMessage
		if err := json.Unmarshal(value, &feats); err != nil {
			return fmt.Errorf("host.%s: want true, false or an object of its features (%s)", name, strings.Join(known, ", "))
		}
		out.Enable(name)
		for f, fv := range feats {
			if !contains(known, f) {
				return fmt.Errorf("host.%s: no feature %q (its features are %s)", name, f, strings.Join(known, ", "))
			}
			var fon bool
			if err := json.Unmarshal(fv, &fon); err != nil {
				return fmt.Errorf("host.%s.%s: want true or false", name, f)
			}
			if fon {
				out.Enable(name, f)
			}
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
		all := true
		for _, f := range c.features {
			all = all && s.Feature(c.name, f)
		}
		if all {
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
// attributes (modes, owners, umask, timestamps) and locks (flock), as the
// file system shim had them; HostSockets the network's sockets and
// resolver; HostSubprocess process.spawn; HostThreads process.threads.
func (a Aliases) Set() Set {
	var s Set
	if a.FS {
		s.Enable(Filesystem, "attributes", "locking")
	}
	if a.Sockets {
		s.Enable(Network, "sockets", "resolver")
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
// stand for, validated. Giving both is an error.
func Resolve(tree Set, aliases Aliases) (Set, error) {
	if !tree.Empty() && aliases.Any() {
		return Set{}, fmt.Errorf("host: give the capabilities either as wasmify.json's \"host\" or by the deprecated HostFS/HostSockets/HostSubprocess/HostThreads flags (or their WASMIFY_HOST_* variables), not both")
	}
	s := tree
	if aliases.Any() {
		s = aliases.Set()
	}
	if err := s.Validate(); err != nil {
		return Set{}, err
	}
	return s, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
