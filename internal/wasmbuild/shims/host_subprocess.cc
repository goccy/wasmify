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

#include <fcntl.h>
#include <spawn.h>
#include <sys/wait.h>
#include <unistd.h>
#include <cerrno>
#include <cstdarg>
#include <cstdint>
#include <cstdlib>
#include <cstring>
#include <string>
#include <sys/types.h>

extern "C" {

__attribute__((import_module("wasi_snapshot_preview1"), import_name("proc_spawn")))
extern int __wasmify_host_proc_spawn(const char *path, char *const argv[],
                                     char *const envp[], int stdin_fd,
                                     int stdout_fd, int stderr_fd,
                                     const char *cwd, int *pid_out);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("proc_wait")))
extern int __wasmify_host_proc_wait(int pid, int options, int *status_out);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("proc_wait_any")))
extern int __wasmify_host_proc_wait_any(int options, int *pid_out, int *status_out);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("pipe")))
extern int __wasmify_host_pipe(int *fds_out);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("fd_dup")))
extern int __wasmify_host_fd_dup(int fd, int *fd_out);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("fd_dup2")))
extern int __wasmify_host_fd_dup2(int from, int to);

/* fd_dup under the lowest free number that is at least min. 0 or a negative
 * errno. */
__attribute__((import_module("wasi_snapshot_preview1"), import_name("fd_dupfd")))
extern int __wasmify_host_fd_dupfd(int fd, int min, int *fd_out);

/* A POSIX record lock as fd_lock passes it: type 0 F_RDLCK, 1 F_WRLCK,
 * 2 F_UNLCK; whence 0 SEEK_SET, 1 SEEK_CUR, 2 SEEK_END. */
struct __wasmify_record_lock {
    int32_t type;
    int32_t whence;
    int64_t start;
    int64_t len;
    int32_t pid;
    int32_t pad;
};

/* A record lock of the host's file behind fd: cmd 0 F_GETLK (the lock in
 * the way is written back, type F_UNLCK when there is none), 1 F_SETLK,
 * 2 F_SETLKW. 0 or a negative errno. */
__attribute__((import_module("wasi_snapshot_preview1"), import_name("fd_lock")))
extern int __wasmify_host_fd_lock(int fd, int cmd, struct __wasmify_record_lock *lock);

/* The close-on-exec flag of a descriptor: *old receives it as it was (0 or
 * 1); with set non-zero it then becomes value. 0 or a negative errno. */
__attribute__((import_module("wasi_snapshot_preview1"), import_name("fd_cloexec")))
extern int __wasmify_host_fd_cloexec(int fd, int set, int value, int *old);

extern int __real_fcntl(int fd, int cmd, ...);

/* fcntl's record locks (F_GETLK, F_SETLK, F_SETLKW) on the host's file
 * behind fd, which wasi-libc refuses (ENOTSUP). */
static int __wasmify_fcntl_lock(int fd, int cmd, struct flock *fl) {
    if (fl == nullptr) { errno = EFAULT; return -1; }
    struct __wasmify_record_lock lk;
    memset(&lk, 0, sizeof(lk));
    switch (fl->l_type) {
    case F_RDLCK: lk.type = 0; break;
    case F_WRLCK: lk.type = 1; break;
    case F_UNLCK: lk.type = 2; break;
    default: errno = EINVAL; return -1;
    }
    switch (fl->l_whence) {
    case SEEK_SET: lk.whence = 0; break;
    case SEEK_CUR: lk.whence = 1; break;
    case SEEK_END: lk.whence = 2; break;
    default: errno = EINVAL; return -1;
    }
    lk.start = fl->l_start;
    lk.len = fl->l_len;
    const int rc = __wasmify_host_fd_lock(fd, cmd == F_GETLK ? 0 : cmd == F_SETLK ? 1 : 2, &lk);
    if (rc != 0) { errno = (rc < 0) ? -rc : rc; return -1; }
    if (cmd == F_GETLK) {
        static const short types[3] = {F_RDLCK, F_WRLCK, F_UNLCK};
        static const short whences[3] = {SEEK_SET, SEEK_CUR, SEEK_END};
        fl->l_type = types[lk.type >= 0 && lk.type <= 2 ? lk.type : 2];
        fl->l_whence = whences[lk.whence >= 0 && lk.whence <= 2 ? lk.whence : 0];
        fl->l_start = lk.start;
        fl->l_len = lk.len;
        fl->l_pid = lk.pid;
    }
    return 0;
}

/* fcntl: wasi-libc answers F_GETFD with FD_CLOEXEC and drops F_SETFD — a
 * WASI process starts no commands, so nothing is ever inherited. With host
 * subprocesses a command does inherit the descriptors that are not marked
 * (the host hands them over under their numbers), so the flag is real and
 * the host keeps it. F_DUPFD and F_DUPFD_CLOEXEC duplicate the descriptor
 * under the lowest free number from the one given, and the record locks are
 * the host's (both of which wasi-libc refuses). Every other request is
 * libc's. (The link routes fcntl here: -Wl,--wrap=fcntl.) */
int __wrap_fcntl(int fd, int cmd, ...) {
    va_list ap;
    va_start(ap, cmd);
    if (cmd == F_GETLK || cmd == F_SETLK || cmd == F_SETLKW) {
        struct flock *fl = va_arg(ap, struct flock *);
        va_end(ap);
        return __wasmify_fcntl_lock(fd, cmd, fl);
    }
    const int arg = va_arg(ap, int);
    va_end(ap);
    if (cmd == F_DUPFD || cmd == F_DUPFD_CLOEXEC) {
        if (arg < 0) { errno = EINVAL; return -1; }
        int nfd = -1;
        const int rc = __wasmify_host_fd_dupfd(fd, arg, &nfd);
        if (rc != 0) { errno = (rc < 0) ? -rc : rc; return -1; }
        if (cmd == F_DUPFD_CLOEXEC) {
            int old = 0;
            (void)__wasmify_host_fd_cloexec(nfd, 1, 1, &old);
        }
        return nfd;
    }
    if (cmd != F_GETFD && cmd != F_SETFD) return __real_fcntl(fd, cmd, arg);
    int old = 0;
    const int rc = __wasmify_host_fd_cloexec(fd, cmd == F_SETFD, (arg & FD_CLOEXEC) != 0, &old);
    if (rc != 0) { errno = (rc < 0) ? -rc : rc; return -1; }
    return cmd == F_GETFD ? (old ? FD_CLOEXEC : 0) : 0;
}

/* dup()/dup2()/dup3(): wasi-libc has neither (WASI preview1 has only
 * fd_renumber, whose move semantics close the source). The host imports
 * implement true sharing semantics: the descriptor closes only when the
 * last fd referencing it does. The subprocess redirection dance
 * (save stdio, point it at a pipe or log, spawn, restore) depends on
 * them. */
int dup(int fd) {
    int nfd = -1;
    int rc = __wasmify_host_fd_dup(fd, &nfd);
    if (rc != 0) { errno = (rc < 0) ? -rc : rc; return -1; }
    return nfd;
}

int dup2(int from, int to) {
    int rc = __wasmify_host_fd_dup2(from, to);
    if (rc != 0) { errno = (rc < 0) ? -rc : rc; return -1; }
    return to;
}

int dup3(int from, int to, int flags) {
    if (from == to) { errno = EINVAL; return -1; }
    if (dup2(from, to) < 0) return -1;
    if (flags & O_CLOEXEC) {
        int old = 0;
        __wasmify_host_fd_cloexec(to, 1, 1, &old);
    }
    return to;
}

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
    /* The guest tracks its working directory in libc (WASI has no chdir
     * syscall), so the host cannot see it; hand it over so the child runs
     * where the guest believes it is. */
    char cwdbuf[4096];
    const char *cwd = getcwd(cwdbuf, sizeof(cwdbuf));
    int p = 0;
    int rc = __wasmify_host_proc_spawn(path, argv, envp, in_fd, out_fd, err_fd,
                                       cwd != nullptr ? cwd : "", &p);
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

/* posix_spawnp: a name without a slash is searched for in the directories
 * of the CALLING process's PATH — the guest's, which the host cannot see —
 * the way execvp does it: each directory is tried in turn, a missing file
 * moves on to the next, and a file that exists but cannot be executed is
 * remembered so that the search reports EACCES rather than ENOENT. */
int posix_spawnp(pid_t *pid, const char *file,
                 const posix_spawn_file_actions_t *fa,
                 const posix_spawnattr_t *attr,
                 char *const argv[], char *const envp[]) {
    (void)attr;
    if (file == nullptr || *file == '\0') return ENOENT;
    if (strchr(file, '/') != nullptr)
        return __wasmify_host_spawn(pid, file, argv, envp, fa);

    const char *path = getenv("PATH");
    if (path == nullptr) path = "/usr/local/bin:/bin:/usr/bin";
    const size_t file_len = strlen(file);
    bool seen_eacces = false;
    for (const char *dir = path;;) {
        const char *end = dir;
        while (*end != '\0' && *end != ':') end++;
        const size_t dir_len = static_cast<size_t>(end - dir);
        /* An empty entry names the current directory. */
        std::string candidate;
        candidate.reserve(dir_len + 1 + file_len);
        if (dir_len > 0) {
            candidate.append(dir, dir_len);
            candidate.push_back('/');
        }
        candidate.append(file, file_len);
        const int rc = __wasmify_host_spawn(pid, candidate.c_str(), argv, envp, fa);
        switch (rc) {
        case 0:
            return 0;
        case EACCES:
            seen_eacces = true;
            break;
        case ENOENT:
        case ENOTDIR:
            break;
        default:
            return rc;
        }
        if (*end == '\0') break;
        dir = end + 1;
    }
    return seen_eacces ? EACCES : ENOENT;
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
    int reaped = static_cast<int>(pid);
    /* pid -1 (and the process-group forms, which have no meaning here: the
     * host tracks children, not groups) waits for any child. */
    int rc = pid > 0
        ? __wasmify_host_proc_wait(static_cast<int>(pid), options, &st)
        : __wasmify_host_proc_wait_any(options, &reaped, &st);
    if (rc == 0) {
        if (wstatus != nullptr) *wstatus = st;
        return static_cast<pid_t>(reaped);
    }
    int e = (rc < 0) ? -rc : rc;
    if (e == EAGAIN) {
        return 0; /* WNOHANG: no child has finished yet */
    }
    errno = e;
    return static_cast<pid_t>(-1);
}

pid_t wait(int *wstatus) {
    return waitpid(static_cast<pid_t>(-1), wstatus, 0);
}

} /* extern "C" */

#endif /* WASMIFY_HOST_SUBPROCESS */
