# Host capabilities

WASI preview1 has no processes, file modes or owners, and only a few socket
calls. A wasm that needs them is given them by the host it runs on:
wasmify's host shims implement the libc functions on imports of the module
`wasip1_posix_extension` (the file system) and on non-standard imports of
`wasi_snapshot_preview1` (the network and processes), and the host
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
| `process` | nothing | `spawn` | the `posix_spawn` family, `waitpid`/`wait`, `pipe`/`pipe2`, `dup`/`dup2`/`dup3` |
| | | `threads` | the wasi-threads target: `-pthread`, a shared memory, atomics (wasm2go runs each guest thread on a goroutine) |

## What a capability changes in the build

For each part given (a capability's base or a feature), wasmify

- compiles and links the shims that implement it (sources under
  `<build>/host-shims/`; the file system's guarded by the macros of its
  parts, the network's and the processes' by the macro of the deprecated
  flag they served: `WASMIFY_HOST_SOCKETS`, `WASMIFY_HOST_SUBPROCESS`);
- puts the headers wasi-libc does not have into `<build>/host-include/`,
  which every compile gets on its include path: `wasmify_posix.h` with
  `filesystem`, `spawn.h` and `sys/wait.h` with `process.spawn`, `netdb.h`
  with `network`;
- defines `WASMIFY_HOST_<CAPABILITY>` and `WASMIFY_HOST_<CAPABILITY>_<FEATURE>`
  (e.g. `WASMIFY_HOST_NETWORK_SOCKETS`) for every compile, the capture build
  included, so that the project's own sources can test them;
- wraps at link the libc functions the shims replace (`chdir` and libc's
  path resolution with `filesystem`; `chmod`, `stat`, `open`, `mkdir`,
  `access`, ... with `filesystem.attributes`; `fcntl` with
  `filesystem.locking`).

## `wasmify_posix.h`

Every import of the module `wasip1_posix_extension` is declared once in
`wasmify_posix.h`. An application includes it to call an import no libc
function expresses.

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
