# Host capabilities

WASI preview1 has no processes, signals, users, terminals, System V IPC,
syslog, time zones, file modes or owners, and only a few socket calls. A
wasm that needs them is given them by the host it runs on: wasmify's host
shims implement the libc functions on imports of the module
`wasmify_posix`, and the host implements those imports (the
[wasm2go](https://github.com/goccy/wasm2go) runtime does). Without any
capability the wasm imports standard WASI only and is portable to any WASI
runtime.

The capabilities are declared under `host` in `wasmify.json`. They follow
the option groups of POSIX.1-2017 (Annex E), each with the features that
belong inside it:

```jsonc
{
  "host": {
    "filesystem": true,                      // all its features
    "network": { "sockets": true },          // the base and the features set true
    "process": { "spawn": true, "resources": true },
    "signals": {},                           // the base alone
    "terminal": true
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
| `network` | `inet_ntop` as glibc words an address | `sockets` | `socket`, `connect`, `bind`, `listen`, `accept`, the options, `send`/`recv` and `sendto`/`recvfrom`, `socketpair`, `shutdown` with the usual numbers, `poll` that watches a descriptor asked for nothing for its hang-up |
| | | `resolver` | `getaddrinfo`, `getnameinfo`, `gethostbyname`/`gethostbyaddr`, the services, protocols and networks databases |
| `process` | `getpid`, `getppid`, the environment (changes told to the host) | `spawn` | the `posix_spawn` family, `waitpid`/`wait`, `pipe`, `dup`/`dup2`/`dup3`, the close-on-exec flag and F_DUPFD of `fcntl`, `execv`/`execvp`, `closefrom` |
| | | `fork` | the `proc_fork` import (`wasmify_posix.h`) |
| | | `job_control` | `getpgid`, `getpgrp`, `setpgid`, `setsid`, `tcgetpgrp`, `tcsetpgrp` |
| | | `resources` | `getpriority`, `setpriority`, `nice`, `times` |
| | | `threads` | the wasi-threads target: `-pthread`, a shared memory, atomics (wasm2go runs each guest thread on a goroutine) |
| `signals` | `sigaction`, `signal`, the signal mask and sets, `sigpending`, `sigsuspend`, `pause`, `raise`, `kill`, `killpg`, `abort`, the default actions, the SIGPIPE of a write | `timers` | `alarm`, `ualarm`, `setitimer`, `getitimer` (ITIMER_REAL) |
| `terminal` | `tcgetattr`, `tcsetattr`, the speeds, `tcdrain`, `tcflow`, `tcflush`, `tcsendbreak`, `ttyname`, `ctermid` | `pseudo` | nothing yet |
| `users` | `getuid`, `geteuid`, `getgid`, `getegid`, the setuid and setgid families, `getgroups`, `setgroups` | `database` | `getpwnam`, `getpwuid`, `getpwent`, `getgrnam`, `getgrgid`, `getgrent`, `getlogin`, `cuserid` |
| `ipc` | nothing | `sysv` | `msgget`, `msgctl`, `msgsnd`, `msgrcv`, `semget`, `semctl`, `semop`, `shmget`, `shmctl`, and `shm_copy` for the bytes of a segment (`shmat` cannot map the host's memory) |
| `system` | nothing | `logging` | `openlog`, `syslog`, `vsyslog`, `closelog`, `setlogmask` |
| | | `info` | `uname` (and `gethostname`, which libc answers from it) |
| | | `time_zones` | `localtime`, `mktime`, `strftime`'s `%z`/`%Z` and `tzset` after `TZ`, as the host knows the zone |

A process that is not the host process (an isolated guest) is answered as
one of its own: a fixed identity (pid 42, parent 1, user and group 0),
empty databases, no system logger (`openlog`/`syslog` report it to
`__wasmify_syslog_undelivered`), no System V IPC (ENOSYS).

### What needs what

A feature that cannot work without another part says so, and wasmify
refuses a tree that lacks it instead of enabling it silently:

- `process.job_control` needs `terminal` (`tcgetpgrp` and `tcsetpgrp` are
  terminal functions);
- `process.fork` needs `process.spawn` (a forked child is waited for with
  `waitpid`);
- `signals` needs `process` (`kill` tells a signal to the process itself by
  its pid);
- `system.logging` needs `process` (`LOG_PID` writes the pid).

## What a capability changes in the build

For each part given (a capability's base or a feature), wasmify

- compiles and links the shims that implement it (sources under
  `<build>/host-shims/`, each guarded by the part's macro);
- puts the headers wasi-libc does not have into `<build>/host-include/`,
  which every compile gets on its include path: `wasmify_posix.h` always,
  `spawn.h` and `sys/wait.h` with `process.spawn`, `netdb.h` with
  `network`, `termios.h` with `terminal`, `syslog.h` with
  `system.logging`, `sys/ipc.h`, `sys/msg.h`, `sys/sem.h`, `sys/shm.h` with
  `ipc.sysv`;
- defines `WASMIFY_HOST_<CAPABILITY>` and `WASMIFY_HOST_<CAPABILITY>_<FEATURE>`
  (e.g. `WASMIFY_HOST_NETWORK_SOCKETS`) for every compile, the capture build
  included, so that the project's own sources can test them;
- wraps at link the libc functions the shims replace (`chdir` and libc's
  path resolution with `filesystem`; `chmod`, `stat`, `open`, `mkdir`,
  `access`, ... with `filesystem.attributes`; `fcntl` with `process.spawn`
  or `filesystem.locking`; the `setenv` family with `process`; `write`,
  `writev` and `send` with `signals`).

## `wasmify_posix.h`

Every import of the module `wasmify_posix` is declared once in
`wasmify_posix.h`. An application includes it to call an import no libc
function expresses (for example to resume a forked guest at a point of its
own), and to hook into the shims:

- a program that runs its signal handlers at safe points (an interpreter)
  registers its pending word with `proc_signal_register` and calls
  `__wasmify_signal_absorb` there; `__wasmify_signal_report` tells the host
  again for a copied instance, `__wasmify_signal_fork_child` clears a forked
  child's pending signals, and `__wasmify_signal_native_mask`,
  `__wasmify_signal_set_mask`, `__wasmify_signal_set_store` and
  `__wasmify_signal_wait_mask` serve native code's masks and sets;
- an exec of the application's own finishes with `__wasmify_exec_become`;
- a program that must know of a syslog message no logger took defines
  `__wasmify_syslog_undelivered` (the shim's is weak and does nothing).

## Deprecated flags

The bridge flags of earlier versions are still read, as aliases of parts of
the tree. Giving one of them (or its environment variable) together with
`host` is an error.

| Flag (`bridge`) | Environment | Stands for |
|---|---|---|
| `HostFS` | `WASMIFY_HOST_FS` | `"filesystem": {"attributes": true, "locking": true}` |
| `HostSockets` | `WASMIFY_HOST_SOCKETS` | `"network": {"sockets": true, "resolver": true}` |
| `HostSubprocess` | `WASMIFY_HOST_SUBPROCESS` | `"process": {"spawn": true}` |
| `HostThreads` | `WASMIFY_HOST_THREADS` | `"process": {"threads": true}` |

The macros these flags defined (`WASMIFY_HOST_FS`, `WASMIFY_HOST_SOCKETS`,
`WASMIFY_HOST_SUBPROCESS`, `WASMIFY_HOST_THREADS`) are still defined for the
features they stand for, for sources that test them; new code tests the
`WASMIFY_HOST_<CAPABILITY>_<FEATURE>` macros.
