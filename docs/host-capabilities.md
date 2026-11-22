# Host capabilities

WASI preview1 has no processes, file modes or owners, and only a few socket
calls. A wasm that needs them is given them by the host it runs on:
wasmify's host shims implement the libc functions on imports of the module
`wasip1_posix_extension` (the file system, processes, users and the system)
and on non-standard imports of `wasi_snapshot_preview1` (the network), and
the host
implements those imports (the [wasm2go](https://github.com/goccy/wasm2go) runtime
does). Without any capability the wasm imports standard WASI only and is
portable to any WASI runtime.

The capabilities are declared under `host` in `wasmify.json`. They follow
the option groups of POSIX.1-2017 (Annex E), each with the features that
belong inside it:

```jsonc
{
  "host": {
    "filesystem": true,                      // all its features
    "network": { "sockets": true },          // the base and the features set true
    "process": {}                            // the base alone
  }
}
```

A capability is `true` (with every feature), `false` or absent (nothing),
or an object of its features (a feature left out is not given). A
capability given at all also brings its base: what it is without any
feature. A name the tree does not have is an error.

## The tree

| Capability | Base | Feature | Brings |
|---|---|---|---|
| `filesystem` | the working directory as the kernel keeps it (`chdir` follows links before `..`), the empty path refused, `fchdir`, `chroot` (EPERM), `mkstemp` | `attributes` | `chmod`, `fchmod`, the modes, owners and block counts of `stat`/`lstat`/`fstat`, the mode of a creating `open` and of `mkdir`, `umask`, `access`/`faccessat` by the host's rules, `futimes`, `chown`/`lchown`/`fchown` |
| | | `fifo` | `mkfifo` |
| | | `locking` | `flock`, the record locks of `fcntl` (F_GETLK, F_SETLK, F_SETLKW) |
| `network` | `netdb.h` | `sockets` | `socket`, `connect`, `getpeername`, `getsockname`, `getaddrinfo`, `getnameinfo` |
| `process` | nothing | `spawn` | the `posix_spawn` family (`posix_spawnp` searching the guest's `PATH`), `waitpid`/`wait` for one child or any, `pipe`/`pipe2`, `dup`/`dup2`/`dup3`, `closefrom`, the close-on-exec flag of `fcntl` (F_GETFD, F_SETFD: wasi-libc's `O_CLOEXEC` is 0, so `open`, `pipe2` and `dup3` cannot set it), and its `F_DUPFD`/`F_DUPFD_CLOEXEC` where the C library names them (wasm64) |
| | | `resources` | `getpid`/`getppid`, `getpriority`/`setpriority`/`nice`, `times` with the CPU time the host accounts |
| | | `threads` | the wasi-threads target: `-pthread`, a shared memory, atomics (wasm2go runs each guest thread on a goroutine) |
| `users` | `getuid`/`geteuid`/`getgid`/`getegid`, the `setuid` and `setgid` families, `getgroups`/`setgroups` | `database` | `pwd.h` and `grp.h`: `getpwuid`/`getpwnam`/`getpwent` and their `_r` forms, `getgrgid`/`getgrnam`/`getgrent` and their `_r` forms; `getlogin`, `cuserid` |
| `signals` | the disposition table (`sigaction`, `signal`), the mask (`sigprocmask`, `sigsuspend`, `sigpending`), the signal sets, `raise`, `kill`/`killpg`, `pause`, `abort`; delivery at the calls and at the application's safe points (`__wasmify_signal_absorb`), and the SIGPIPE of a `write`, `writev` or `send` before it returns | `timers` | `alarm`, `ualarm`, `setitimer`/`getitimer` (ITIMER_REAL) |
| `system` | nothing | `info` | `uname` (and `gethostname`) of the host's system |
| | | `logging` | `syslog.h`: `openlog`, `syslog`, `vsyslog`, `closelog`, `setlogmask` |
| | | `time_zones` | `localtime`, `mktime` and `strftime` in the zone `TZ` names, `tzset`, `tzname`, `timezone`, `daylight` |

## What a capability changes in the build

For each part given (a capability's base or a feature), wasmify

- compiles and links the shims that implement it (sources under
  `<build>/host-shims/`; each guarded by the macros of its parts, the
  network's by the macro of the deprecated flag it served:
  `WASMIFY_HOST_SOCKETS`);
- puts the headers wasi-libc does not have into `<build>/host-include/`,
  which every compile gets on its include path: `wasmify_posix.h` with
  `filesystem`, `process.spawn`, `process.resources`, `signals`, `users`,
  `system.info`, `system.logging` and `system.time_zones`; `spawn.h`,
  `sys/wait.h` and `unistd.h` (the C library's, declaring what the shims
  define that it leaves out: `closefrom`, `getppid`, `nice`, `pause`) with
  `process.spawn`, `process.resources` and `signals`; `netdb.h` with
  `network`; `signal.h` (the POSIX-compat overlay's, with the C library's
  one-byte `sigset_t`) with `signals`; `sys/time.h` (the C
  library's, declaring the interval timer) with `signals.timers`;
  `syslog.h` with `system.logging`; `pwd.h` and `grp.h` with
  `users.database`; `time.h` (the C library's, declaring `tzset`, `tzname`,
  `timezone` and `daylight`) with `system.time_zones`;
- defines `WASMIFY_HOST_<CAPABILITY>` and `WASMIFY_HOST_<CAPABILITY>_<FEATURE>`
  (e.g. `WASMIFY_HOST_NETWORK_SOCKETS`) for every compile, the capture build
  included, so that the project's own sources can test them;
- wraps at link the libc functions the shims replace (`chdir` and libc's
  path resolution with `filesystem`; `chmod`, `stat`, `open`, `mkdir`,
  `access`, ... with `filesystem.attributes`; `fcntl` with
  `filesystem.locking` and `process.spawn`; `write`, `writev` and `send`
  with `signals`).

## `wasmify_posix.h`

Every import of the module `wasip1_posix_extension` is declared once in
`wasmify_posix.h`. An application includes it to call an import no libc
function expresses.

## Signals

A WASI guest cannot be interrupted, so a signal reaches a handler at a
point the program chooses. The program registers a flag word with
`__wasmify_host_proc_signal_register`: a 4-byte aligned `uint32_t` the host
sets to 1, atomically, when it records a signal for the process. The
program reads it with an atomic load at its safe points and calls
`__wasmify_signal_absorb` there, which delivers the recorded signals. The
shim delivers the signals of its own calls (`raise`, `kill` of the process
itself, `sigprocmask`, `sigsuspend`, `pause`, the SIGPIPE of a write) before
they return. A program that has registered no word has no handlers run:
the host gives every signal for it its default action, which ends the
process for most signals. The shim registers no word of its own.

## Deprecated flags

The bridge flags of earlier versions are still read, as aliases of parts of
the tree. Giving one of them (or its environment variable) together with
`host` is an error.

| Flag (`bridge`) | Environment | Stands for |
|---|---|---|
| `HostFS` | `WASMIFY_HOST_FS` | `"filesystem": {"attributes": true, "locking": true}` |
| `HostSockets` | `WASMIFY_HOST_SOCKETS` | `"network": {"sockets": true}` |
| `HostSubprocess` | `WASMIFY_HOST_SUBPROCESS` | `"process": {"spawn": true}` |
| `HostThreads` | `WASMIFY_HOST_THREADS` | `"process": {"threads": true}` |

The macros these flags defined (`WASMIFY_HOST_FS`, `WASMIFY_HOST_SOCKETS`,
`WASMIFY_HOST_SUBPROCESS`, `WASMIFY_HOST_THREADS`) are still defined for the
features they stand for, for sources that test them; new code tests the
`WASMIFY_HOST_<CAPABILITY>_<FEATURE>` macros.
