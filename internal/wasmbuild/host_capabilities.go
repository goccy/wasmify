package wasmbuild

import (
	"slices"

	"github.com/goccy/wasmify/internal/hostcap"
)

// hostPart is what one part of the host capabilities brings into a build:
// a capability's base (feature "") or one of its features. Its shims are
// the sources that implement it (each guarded inside by a macro of
// hostcap.Set.Macros, see hostShimFS), its headers go into the host-include
// directory, and its wraps are the libc functions the link routes through a
// shim's __wrap_ definition.
type hostPart struct {
	capability, feature string
	shims               []string
	headers             []string
	wraps               []string
}

// hostParts lists every part of the capability tree, in the order of the
// schema. A part with nothing is one that changes the target
// (process.threads: effectiveTarget, wasmCompileFlags and the link), a
// base whose features bring all there is, or one that has nothing yet
// (terminal.pseudo).
var hostParts = []hostPart{
	{capability: hostcap.Filesystem, shims: []string{"host_fs.cc"}, headers: []string{"wasmify_posix.h"},
		wraps: []string{"chdir", "__wasilibc_find_relpath_alloc"}},
	{capability: hostcap.Filesystem, feature: "attributes", shims: []string{"host_fs.cc"},
		wraps: []string{"chmod", "fchmod", "stat", "lstat", "fstat", "open", "mkdir", "access", "faccessat"}},
	{capability: hostcap.Filesystem, feature: "fifo", shims: []string{"host_fs.cc"}},
	{capability: hostcap.Filesystem, feature: "locking", shims: []string{"host_fs.cc", "host_fcntl.cc"},
		wraps: []string{"fcntl"}},

	{capability: hostcap.Network, shims: []string{"host_sockets.cc"}, headers: []string{"wasmify_posix.h", "netdb.h"},
		wraps: []string{"inet_ntop"}},
	{capability: hostcap.Network, feature: "sockets", shims: []string{"host_sockets.cc"}},
	{capability: hostcap.Network, feature: "resolver", shims: []string{"host_sockets.cc"}},

	{capability: hostcap.Process},
	{capability: hostcap.Process, feature: "spawn", shims: []string{"host_subprocess.cc", "host_fcntl.cc"},
		headers: []string{"wasmify_posix.h", "spawn.h", "sys/wait.h", "unistd.h"}, wraps: []string{"fcntl"}},
	{capability: hostcap.Process, feature: "environment", shims: []string{"host_process.cc"}, headers: []string{"wasmify_posix.h"},
		wraps: []string{"setenv", "unsetenv", "putenv", "clearenv"}},
	{capability: hostcap.Process, feature: "job_control", shims: []string{"host_process.cc", "host_tty.cc"},
		headers: []string{"wasmify_posix.h", "unistd.h"}},
	{capability: hostcap.Process, feature: "resources", shims: []string{"host_process.cc"},
		headers: []string{"wasmify_posix.h", "unistd.h"}},
	{capability: hostcap.Process, feature: "threads"},

	{capability: hostcap.Signals, shims: []string{"host_signal.cc"},
		headers: []string{"wasmify_posix.h", "signal.h", "unistd.h"}, wraps: []string{"write", "writev", "send"}},
	{capability: hostcap.Signals, feature: "timers", shims: []string{"host_signal.cc"}, headers: []string{"sys/time.h"}},

	{capability: hostcap.Terminal, shims: []string{"host_tty.cc"},
		headers: []string{"wasmify_posix.h", "termios.h", "sys/ioctl.h", "unistd.h"}, wraps: []string{"ioctl"}},
	{capability: hostcap.Terminal, feature: "pseudo"},

	{capability: hostcap.Users, shims: []string{"host_users.cc"}, headers: []string{"wasmify_posix.h"}},
	{capability: hostcap.Users, feature: "database", shims: []string{"host_users.cc"}, headers: []string{"pwd.h", "grp.h"}},

	{capability: hostcap.IPC},
	{capability: hostcap.IPC, feature: "sysv", shims: []string{"host_ipc.cc"},
		headers: []string{"wasmify_posix.h", "sys/ipc.h", "sys/msg.h", "sys/sem.h", "sys/shm.h"}},

	{capability: hostcap.System},
	{capability: hostcap.System, feature: "info", shims: []string{"host_system.cc"}, headers: []string{"wasmify_posix.h"}},
	{capability: hostcap.System, feature: "logging", shims: []string{"host_syslog.cc"},
		headers: []string{"wasmify_posix.h", "syslog.h"}},
	{capability: hostcap.System, feature: "time_zones", shims: []string{"host_time.cc"},
		headers: []string{"wasmify_posix.h", "time.h"}},
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
	for _, p := range hostParts {
		if !p.given(s) {
			continue
		}
		for _, name := range list(p) {
			if !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	return out
}

// hostShimNames returns the shims of the given parts, in hostShimOrder.
func hostShimNames(s hostcap.Set) []string {
	needed := hostGiven(s, func(p hostPart) []string { return p.shims })
	return slices.DeleteFunc(slices.Clone(hostShimOrder), func(name string) bool { return !slices.Contains(needed, name) })
}

// hostHeaderNames returns the host-include headers of the given parts.
func hostHeaderNames(s hostcap.Set) []string {
	return hostGiven(s, func(p hostPart) []string { return p.headers })
}

// hostWraps returns the libc functions the link wraps for the given parts.
func hostWraps(s hostcap.Set) []string {
	return hostGiven(s, func(p hostPart) []string { return p.wraps })
}
