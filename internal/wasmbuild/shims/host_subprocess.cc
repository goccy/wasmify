/* host_subprocess.cc — generic host-provided subprocess shim.
 *
 * This translation unit is deployed and compiled by wasmify itself, and only
 * when the host capability process.spawn is given (wasmify.json's "host",
 * which makes wasmify define WASMIFY_HOST_PROCESS_SPAWN for this compile).
 * Without it the file is excluded entirely and the wasm stays portable
 * (standard wasi imports only).
 *
 * WASI preview1 cannot spawn processes and wasi-libc provides no posix_spawn
 * or waitpid. With process.spawn, we DEFINE the
 * posix_spawn family + waitpid here, and execv/execvp, backed by
 * proc_spawn/proc_wait/proc_exec host imports the WASI host implements in
 * Go (e.g. via os/exec). A consuming library's subprocess implementation
 * that routes onto posix_spawn therefore runs against this path.
 *
 * Stdio remapping IS supported: posix_spawn_file_actions records, via adddup2,
 * which guest fd becomes each of the child's fds 0/1/2 (stored in
 * posix_spawn_file_actions_t.__fd[3]). This is enough to back capture_output
 * style pipes, where the guest creates host pipes via pipe() and wires their
 * ends as the child's stdout/stderr. Other file actions and the attr object
 * carry no state and are no-ops. The spawned program is a HOST binary, gated
 * by the host's exec whitelist. */
#ifdef WASMIFY_HOST_PROCESS_SPAWN

#include <fcntl.h>
#include <spawn.h>
#include <sys/wait.h>
#include <unistd.h>
#include <cerrno>
#include <climits>
#include <cstdarg>
#include <cstdlib>
#include <cstring>
#include <string>
#include <sys/types.h>

#include "wasmify_posix.h"

extern "C" {

/* ---- close-on-exec at creation ---------------------------------------------
 * O_CLOEXEC is a flag of its own (host-include's fcntl.h; wasi-libc's is
 * 0). open, openat, pipe2 and dup3 take it off and mark the new descriptor
 * close-on-exec with the host, which hands a command every descriptor that
 * is not marked. __wasmify_mark_cloexec returns fd, or closes it (the
 * caller does not get it) and returns -1 with the flag's errno; an error
 * of that close has no way out. */
int __wasmify_mark_cloexec(int fd) {
    int old = 0;
    const int rc = __wasmify_host_fd_cloexec(fd, 1, 1, &old);
    if (rc == 0) return fd;
    (void)close(fd);
    errno = (rc < 0) ? -rc : rc;
    return -1;
}

int __real_openat(int dirfd, const char *path, int oflag, ...);
int __wrap_openat(int dirfd, const char *path, int oflag, ...) {
    mode_t mode = 0;
    if (oflag & O_CREAT) {
        va_list ap;
        va_start(ap, oflag);
        mode = va_arg(ap, mode_t);
        va_end(ap);
    }
    const int fd = __real_openat(dirfd, path, oflag & ~O_CLOEXEC, mode);
    if (fd < 0 || !(oflag & O_CLOEXEC)) return fd;
    return __wasmify_mark_cloexec(fd);
}

#ifndef WASMIFY_HOST_FILESYSTEM_ATTRIBUTES
/* (With filesystem.attributes, host_fs.cc's open is the wrapper.) */
int __real_open(const char *path, int oflag, ...);
int __wrap_open(const char *path, int oflag, ...) {
    mode_t mode = 0;
    if (oflag & O_CREAT) {
        va_list ap;
        va_start(ap, oflag);
        mode = va_arg(ap, mode_t);
        va_end(ap);
    }
    const int fd = __real_open(path, oflag & ~O_CLOEXEC, mode);
    if (fd < 0 || !(oflag & O_CLOEXEC)) return fd;
    return __wasmify_mark_cloexec(fd);
}
#endif

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
    if (from == to || (flags & ~O_CLOEXEC) != 0) { errno = EINVAL; return -1; }
    if (dup2(from, to) < 0) return -1;
    if (!(flags & O_CLOEXEC)) return to;
    return __wasmify_mark_cloexec(to);
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

/* posix_spawnp and execvp: a name without a slash is searched for in the
 * directories of the CALLING process's PATH — the guest's, which the host
 * cannot see — the way execvp does it: run(path) starts the command at
 * each candidate path in turn and returns 0 or the errno it failed with; a
 * missing file moves on to the next, and a file that exists but cannot be
 * executed is remembered so that the search reports EACCES rather than
 * ENOENT; a directory whose candidate path would be PATH_MAX bytes or more
 * is passed over, as musl's execvp passes it. Without PATH the directories
 * are the C library's default (musl's execvp). Returns 0 or the errno of
 * the search. */
extern "C++" {
template <typename Run>
static int __wasmify_search_path(const char *file, Run run) {
    if (file == nullptr || *file == '\0') return ENOENT;
    if (strchr(file, '/') != nullptr) return run(file);

    const char *path = getenv("PATH");
    if (path == nullptr) path = "/usr/local/bin:/bin:/usr/bin";
    const size_t file_len = strlen(file);
    bool seen_eacces = false;
    for (const char *dir = path;;) {
        const char *end = dir;
        while (*end != '\0' && *end != ':') end++;
        const size_t dir_len = static_cast<size_t>(end - dir);
        if (dir_len + 1 + file_len >= PATH_MAX) {
            if (*end == '\0') break;
            dir = end + 1;
            continue;
        }
        /* An empty entry names the current directory. */
        std::string candidate;
        if (dir_len > 0) {
            candidate.append(dir, dir_len);
        } else {
            candidate.push_back('.');
        }
        candidate.push_back('/');
        candidate.append(file, file_len);
        const int rc = run(candidate.c_str());
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
} /* extern "C++" */

int posix_spawnp(pid_t *pid, const char *file,
                 const posix_spawn_file_actions_t *fa,
                 const posix_spawnattr_t *attr,
                 char *const argv[], char *const envp[]) {
    (void)attr;
    return __wasmify_search_path(file, [&](const char *path) {
        return __wasmify_host_spawn(pid, path, argv, envp, fa);
    });
}

/* ---- exec -------------------------------------------------------------------
 * A process image cannot be replaced: the host starts the command with this
 * process's environment, working directory and descriptors 0, 1 and 2, and
 * the process becomes it (proc_exec). It keeps its pid, every signal sent
 * to it goes to the command, its descriptors are closed, and it ends the
 * way the command ends, running nothing of the program's own. So the exec
 * functions return only when the command could not be started, with errno
 * set. */

/* Starts the command in the process's working directory; returns the errno
 * of a command that could not be started, or getcwd's when the directory
 * cannot be told. */
static int __wasmify_exec(const char *path, char *const argv[], char *const envp[]) {
    char *cwd = getcwd(nullptr, 0);
    if (cwd == nullptr) return errno;
    const int error = -__wasmify_host_proc_exec(path, argv, envp, cwd);
    free(cwd);
    return error;
}

int execve(const char *path, char *const argv[], char *const envp[]) {
    errno = __wasmify_exec(path, argv, envp);
    return -1;
}

int execv(const char *path, char *const argv[]) { return execve(path, argv, environ); }

int execvp(const char *file, char *const argv[]) {
    errno = __wasmify_search_path(
        file, [&](const char *path) { return __wasmify_exec(path, argv, environ); });
    return -1;
}

/* The arguments of execl, execlp and execle, arg0 and those after it up to
 * the null pointer (which is consumed), as an argv to free; NULL with errno
 * ENOMEM. */
static char **__wasmify_exec_args(const char *arg0, va_list *ap) {
    size_t n = 0;
    if (arg0 != nullptr) {
        va_list count;
        va_copy(count, *ap);
        for (n = 1; va_arg(count, const char *) != nullptr; n++) {
        }
        va_end(count);
    }
    char **argv = static_cast<char **>(malloc((n + 1) * sizeof(char *)));
    if (argv == nullptr) {
        errno = ENOMEM;
        return nullptr;
    }
    if (n > 0) {
        argv[0] = const_cast<char *>(arg0);
        for (size_t i = 1; i < n; i++) argv[i] = va_arg(*ap, char *);
        (void)va_arg(*ap, char *); /* the null pointer that ends the list */
    }
    argv[n] = nullptr;
    return argv;
}

/* Frees argv and fails with the errno the exec left. */
static int __wasmify_exec_failed(char **argv) {
    const int error = errno;
    free(argv);
    errno = error;
    return -1;
}

int execl(const char *path, const char *arg0, ...) {
    va_list ap;
    va_start(ap, arg0);
    char **argv = __wasmify_exec_args(arg0, &ap);
    va_end(ap);
    if (argv == nullptr) return -1;
    execv(path, argv);
    return __wasmify_exec_failed(argv);
}

int execlp(const char *file, const char *arg0, ...) {
    va_list ap;
    va_start(ap, arg0);
    char **argv = __wasmify_exec_args(arg0, &ap);
    va_end(ap);
    if (argv == nullptr) return -1;
    execvp(file, argv);
    return __wasmify_exec_failed(argv);
}

int execle(const char *path, const char *arg0, ...) {
    va_list ap;
    va_start(ap, arg0);
    char **argv = __wasmify_exec_args(arg0, &ap);
    char *const *envp = argv != nullptr ? va_arg(ap, char *const *) : nullptr;
    va_end(ap);
    if (argv == nullptr) return -1;
    execve(path, argv, envp);
    return __wasmify_exec_failed(argv);
}

/* pipe()/pipe2(): host-backed pipe for subprocess capture_output. The host
 * creates an OS pipe and returns [readFd, writeFd]; the guest reads the read
 * end via fd_read while a spawned child writes the write end (wired as its
 * stdout/stderr by posix_spawn). pipe2 makes both ends non-blocking with
 * O_NONBLOCK and marks them close-on-exec with O_CLOEXEC; on a failure of
 * either the ends are closed (an error of those closes has no way out). */
int pipe(int fildes[2]) {
    if (fildes == nullptr) { errno = EFAULT; return -1; }
    int rc = __wasmify_host_pipe(fildes);
    if (rc != 0) { errno = (rc < 0) ? -rc : rc; return -1; }
    return 0;
}

int pipe2(int fildes[2], int flags) {
    if ((flags & ~(O_CLOEXEC | O_NONBLOCK)) != 0) { errno = EINVAL; return -1; }
    int fds[2] = {-1, -1};
    if (pipe(fds) != 0) return -1;
    for (int i = 0; i < 2; i++) {
        int e = 0;
        if ((flags & O_NONBLOCK) && fcntl(fds[i], F_SETFL, O_NONBLOCK) != 0) e = errno;
        int old = 0;
        if (e == 0 && (flags & O_CLOEXEC)) {
            const int rc = __wasmify_host_fd_cloexec(fds[i], 1, 1, &old);
            if (rc != 0) e = (rc < 0) ? -rc : rc;
        }
        if (e != 0) {
            (void)close(fds[0]);
            (void)close(fds[1]);
            errno = e;
            return -1;
        }
    }
    fildes[0] = fds[0];
    fildes[1] = fds[1];
    return 0;
}

/* waitpid: pid > 0 waits for that child (proc_wait), -1 for any child
 * (proc_wait_any), 0 for a child in the caller's process group and pid < -1
 * for a child in group -pid (proc_wait_group). */
pid_t waitpid(pid_t pid, int *wstatus, int options) {
    int st = 0;
    int reaped = static_cast<int>(pid);
    int rc;
    if (pid > 0) {
        rc = __wasmify_host_proc_wait(static_cast<int>(pid), options, &st);
    } else if (pid == -1) {
        rc = __wasmify_host_proc_wait_any(options, &reaped, &st);
    } else if (pid == INT_MIN) {
        rc = -ESRCH; /* a group -pid cannot name, as Linux reports it */
    } else {
        int group = -static_cast<int>(pid);
        rc = pid == 0 ? __wasmify_host_proc_getpgid(0, &group) : 0;
        if (rc == 0) rc = __wasmify_host_proc_wait_group(group, options, &reaped, &st);
    }
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

/* closefrom(2): closes every descriptor from low_fd up. It returns nothing,
 * so an error of the host's has no way to the caller. */
void closefrom(int low_fd) { (void)__wasmify_host_fd_closefrom(low_fd); }

} /* extern "C" */

#endif /* WASMIFY_HOST_PROCESS_SPAWN */
