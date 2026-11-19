package wasmbuild

import "github.com/goccy/wasmify/internal/hostcap"

// hostPart is what one part of the host capabilities brings into a build:
// a capability's base (feature "") or one of its features. Its shims are
// the sources that implement it (each guarded inside by the macros of the
// parts it serves, hostcap.Set.Macros), its headers go into the
// host-include directory, its private headers beside the shims, and its
// wraps are the libc functions the link routes through a shim's __wrap_
// definition.
type hostPart struct {
	capability, feature string
	shims               []string
	headers             []string
	privateHeaders      []string
	wraps               []string
}

// hostParts lists every part of the capability tree, in the order of the
// schema. A part with nothing is one whose functions the host offers only
// as imports (wasmify_posix.h) or that changes the target (process.threads:
// effectiveTarget, wasmCompileFlags and the link).
var hostParts = []hostPart{
	{capability: hostcap.Filesystem, shims: []string{"host_fs.cc"},
		wraps: []string{"chdir", "__wasilibc_find_relpath_alloc"}},
	{capability: hostcap.Filesystem, feature: "attributes", shims: []string{"host_fs.cc"},
		wraps: []string{"chmod", "fchmod", "stat", "lstat", "fstat", "open", "mkdir", "access", "faccessat"}},
	{capability: hostcap.Filesystem, feature: "fifo", shims: []string{"host_fs.cc"}},
	{capability: hostcap.Filesystem, feature: "locking", shims: []string{"host_fs.cc", "host_fcntl.cc"},
		wraps: []string{"fcntl"}},

	{capability: hostcap.Network, shims: []string{"host_sockets.cc"}, headers: []string{"netdb.h"}},
	{capability: hostcap.Network, feature: "sockets", shims: []string{"host_sockets.cc"}},
	{capability: hostcap.Network, feature: "resolver", shims: []string{"host_sockets.cc"}},

	{capability: hostcap.Process, shims: []string{"host_process.cc"},
		wraps: []string{"setenv", "unsetenv", "putenv", "clearenv"}},
	{capability: hostcap.Process, feature: "spawn", shims: []string{"host_subprocess.cc", "host_fcntl.cc"},
		headers: []string{"spawn.h", "sys/wait.h"}, wraps: []string{"fcntl"}},
	{capability: hostcap.Process, feature: "fork"},
	{capability: hostcap.Process, feature: "job_control", shims: []string{"host_process.cc", "host_tty.cc"}},
	{capability: hostcap.Process, feature: "resources", shims: []string{"host_process.cc"}},
	{capability: hostcap.Process, feature: "threads"},

	{capability: hostcap.Signals, shims: []string{"host_signal.cc"}, wraps: []string{"write", "writev", "send"}},
	{capability: hostcap.Signals, feature: "timers", shims: []string{"host_signal.cc"}},

	{capability: hostcap.Terminal, shims: []string{"host_tty.cc"}, headers: []string{"termios.h"}},
	{capability: hostcap.Terminal, feature: "pseudo"},

	{capability: hostcap.Users, shims: []string{"host_users.cc"}, privateHeaders: []string{"pwd.h", "grp.h"}},
	{capability: hostcap.Users, feature: "database", shims: []string{"host_users.cc"}},

	{capability: hostcap.IPC},
	{capability: hostcap.IPC, feature: "sysv", shims: []string{"host_ipc.cc"},
		headers: []string{"sys/ipc.h", "sys/msg.h", "sys/sem.h", "sys/shm.h"}},

	{capability: hostcap.System},
	{capability: hostcap.System, feature: "logging", shims: []string{"host_syslog.cc"}, headers: []string{"syslog.h"}},
	{capability: hostcap.System, feature: "info", shims: []string{"host_system.cc"}},
	{capability: hostcap.System, feature: "time_zones", shims: []string{"host_time.cc"}},
}

// hostShimOrder is the order the shims are deployed and compiled in.
var hostShimOrder = []string{
	"host_sockets.cc",
	"host_subprocess.cc",
	"host_fcntl.cc",
	"host_process.cc",
	"host_users.cc",
	"host_signal.cc",
	"host_tty.cc",
	"host_time.cc",
	"host_system.cc",
	"host_ipc.cc",
	"host_syslog.cc",
	"host_fs.cc",
}

func (p hostPart) given(s hostcap.Set) bool {
	if p.feature == "" {
		return s.Has(p.capability)
	}
	return s.Feature(p.capability, p.feature)
}

// hostGiven collects one list of every given part, without repeats, in
// the order the parts list them.
func hostGiven(s hostcap.Set, list func(hostPart) []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range hostParts {
		if !p.given(s) {
			continue
		}
		for _, name := range list(p) {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}

// hostShimNames returns the shims of the given parts, in hostShimOrder.
func hostShimNames(s hostcap.Set) []string {
	needed := map[string]bool{}
	for _, name := range hostGiven(s, func(p hostPart) []string { return p.shims }) {
		needed[name] = true
	}
	var out []string
	for _, name := range hostShimOrder {
		if needed[name] {
			out = append(out, name)
		}
	}
	return out
}

// hostHeaderNames returns the host-include headers of the given parts.
func hostHeaderNames(s hostcap.Set) []string {
	return hostGiven(s, func(p hostPart) []string { return p.headers })
}

// hostPrivateHeaderNames returns the headers the given parts' shims
// include from beside them.
func hostPrivateHeaderNames(s hostcap.Set) []string {
	return hostGiven(s, func(p hostPart) []string { return p.privateHeaders })
}

// hostWraps returns the libc functions the link wraps for the given parts.
func hostWraps(s hostcap.Set) []string {
	return hostGiven(s, func(p hostPart) []string { return p.wraps })
}
