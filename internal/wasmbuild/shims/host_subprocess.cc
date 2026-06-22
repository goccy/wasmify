/* host_subprocess.cc — generic host-provided subprocess shim.
 *
 * This translation unit is deployed and compiled by wasmify itself, and only
 * when host subprocess support is opted in (bridge.HostSubprocess in
 * wasmify.json, which makes wasmify define WASMIFY_HOST_SUBPROCESS for this
 * compile). Without that opt-in the file is excluded entirely and the wasm
 * stays portable (standard wasi imports only).
 *
 * WASI preview1 cannot spawn processes and wasi-libc provides no posix_spawn
 * or waitpid. When host subprocess support is opted in, we DEFINE the
 * posix_spawn family + waitpid here, backed by proc_spawn/proc_wait host
 * imports the WASI host implements in Go (e.g. via os/exec). A consuming
 * library's subprocess implementation that routes onto posix_spawn therefore
 * runs against this path.
 *
 * Stdio remapping IS supported: posix_spawn_file_actions records, via adddup2,
 * which guest fd becomes each of the child's fds 0/1/2 (stored in
 * posix_spawn_file_actions_t.__fd[3]). This is enough to back capture_output
 * style pipes, where the guest creates host pipes via pipe() and wires their
 * ends as the child's stdout/stderr. Other file actions and the attr object
 * carry no state and are no-ops. The spawned program is a HOST binary, gated
 * by the host's exec whitelist. */
#ifdef WASMIFY_HOST_SUBPROCESS

#include <spawn.h>
#include <sys/wait.h>
#include <cerrno>
#include <sys/types.h>

extern "C" {

__attribute__((import_module("wasi_snapshot_preview1"), import_name("proc_spawn")))
extern int __wasmify_host_proc_spawn(const char *path, char *const argv[],
                                     char *const envp[], int stdin_fd,
                                     int stdout_fd, int stderr_fd, int *pid_out);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("proc_wait")))
extern int __wasmify_host_proc_wait(int pid, int options, int *status_out);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("pipe")))
extern int __wasmify_host_pipe(int *fds_out);

int posix_spawn_file_actions_init(posix_spawn_file_actions_t *fa) {
    if (fa != nullptr) { fa->__fd[0] = fa->__fd[1] = fa->__fd[2] = -1; }
    return 0;
}
int posix_spawn_file_actions_destroy(posix_spawn_file_actions_t *fa) { (void)fa; return 0; }
int posix_spawn_file_actions_addopen(posix_spawn_file_actions_t *fa, int fd,
                                     const char *path, int oflag, mode_t mode) {
    (void)fa; (void)fd; (void)path; (void)oflag; (void)mode; return 0;
}
int posix_spawn_file_actions_addclose(posix_spawn_file_actions_t *fa, int fd) {
    /* Closing the parent's pipe ends inside the child is implicit: the host
     * child has its own fd space and only receives the fds we wire as stdio. */
    (void)fa; (void)fd; return 0;
}
int posix_spawn_file_actions_adddup2(posix_spawn_file_actions_t *fa, int fd, int newfd) {
    /* Record which guest fd becomes the child's stdin/stdout/stderr. */
    if (fa != nullptr && newfd >= 0 && newfd < 3) { fa->__fd[newfd] = fd; }
    return 0;
}
int posix_spawn_file_actions_addclosefrom_np(posix_spawn_file_actions_t *fa, int from) {
    (void)fa; (void)from; return 0;
}

int posix_spawnattr_init(posix_spawnattr_t *a) { (void)a; return 0; }
int posix_spawnattr_destroy(posix_spawnattr_t *a) { (void)a; return 0; }
int posix_spawnattr_setflags(posix_spawnattr_t *a, short f) { (void)a; (void)f; return 0; }
int posix_spawnattr_setpgroup(posix_spawnattr_t *a, pid_t p) { (void)a; (void)p; return 0; }
int posix_spawnattr_setsigmask(posix_spawnattr_t *a, const sigset_t *s) { (void)a; (void)s; return 0; }
int posix_spawnattr_setsigdefault(posix_spawnattr_t *a, const sigset_t *s) { (void)a; (void)s; return 0; }
int posix_spawnattr_setschedparam(posix_spawnattr_t *a, const struct sched_param *s) { (void)a; (void)s; return 0; }
int posix_spawnattr_setschedpolicy(posix_spawnattr_t *a, int p) { (void)a; (void)p; return 0; }

static int __wasmify_host_spawn(pid_t *pid, const char *path,
                                char *const argv[], char *const envp[],
                                const posix_spawn_file_actions_t *fa) {
    int in_fd = -1, out_fd = -1, err_fd = -1;
    if (fa != nullptr) {
        in_fd = fa->__fd[0];
        out_fd = fa->__fd[1];
        err_fd = fa->__fd[2];
    }
    int p = 0;
    int rc = __wasmify_host_proc_spawn(path, argv, envp, in_fd, out_fd, err_fd, &p);
    if (rc != 0) {
        /* posix_spawn reports failure as a positive errno return value. */
        return (rc < 0) ? -rc : rc;
    }
    if (pid != nullptr) *pid = static_cast<pid_t>(p);
    return 0;
}

int posix_spawn(pid_t *pid, const char *path,
                const posix_spawn_file_actions_t *fa,
                const posix_spawnattr_t *attr,
                char *const argv[], char *const envp[]) {
    (void)attr;
    return __wasmify_host_spawn(pid, path, argv, envp, fa);
}

int posix_spawnp(pid_t *pid, const char *file,
                 const posix_spawn_file_actions_t *fa,
                 const posix_spawnattr_t *attr,
                 char *const argv[], char *const envp[]) {
    (void)attr;
    return __wasmify_host_spawn(pid, file, argv, envp, fa);
}

/* pipe()/pipe2(): host-backed pipe for subprocess capture_output. The host
 * creates an OS pipe and returns [readFd, writeFd]; the guest reads the read
 * end via fd_read while a spawned child writes the write end (wired as its
 * stdout/stderr by posix_spawn). pipe2 flags (O_CLOEXEC/O_NONBLOCK) are
 * accepted but not enforced — the host child fd space is separate and the
 * guest drives blocking. */
int pipe(int fildes[2]) {
    if (fildes == nullptr) { errno = EFAULT; return -1; }
    int rc = __wasmify_host_pipe(fildes);
    if (rc != 0) { errno = (rc < 0) ? -rc : rc; return -1; }
    return 0;
}

int pipe2(int fildes[2], int flags) {
    (void)flags;
    return pipe(fildes);
}

pid_t waitpid(pid_t pid, int *wstatus, int options) {
    int st = 0;
    int rc = __wasmify_host_proc_wait(static_cast<int>(pid), options, &st);
    if (rc == 0) {
        if (wstatus != nullptr) *wstatus = st;
        return pid;
    }
    int e = (rc < 0) ? -rc : rc;
    if (e == EAGAIN) {
        return 0; /* WNOHANG: child still running */
    }
    errno = e;
    return static_cast<pid_t>(-1);
}

pid_t wait(int *wstatus) {
    /* Reaping an arbitrary child is unsupported; the consumer always waits on a
     * specific pid via waitpid(). */
    (void)wstatus;
    errno = ECHILD;
    return static_cast<pid_t>(-1);
}

} /* extern "C" */

#endif /* WASMIFY_HOST_SUBPROCESS */
