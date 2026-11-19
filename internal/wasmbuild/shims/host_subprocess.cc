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
#ifdef WASMIFY_HOST_PROCESS_SPAWN

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

#include "wasmify_posix.h"

extern "C" {

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

/* ---- exec, closefrom ------------------------------------------------------ */
extern char **environ;

extern "C" {

/* closefrom(2): closes every descriptor from low_fd up. */
void closefrom(int low_fd) { (void)__wasmify_host_fd_closefrom(low_fd); }

/* The second half of exec. A process image cannot be replaced, so exec
 * starts the command with this process's current fds and environment
 * (posix_spawn, after __wasmify_host_proc_exec_next(1)) and then this
 * process stands in for it until it ends:
 *
 *   - It gives up every descriptor it holds, as exec does for the
 *     close-on-exec ones — the command has its own
 *     stdio. A peer waiting for end-of-file on one of them (a status pipe
 *     its parent reads to learn that exec succeeded) must not wait until the
 *     command ends.
 *   - It waits in the host, which passes every signal this process is sent
 *     on to the command: the pid others know is this one's, and the program
 *     that had the handlers is gone. SIGKILL included.
 *   - It ends the way the command ended: with its exit status, or killed by
 *     the signal that killed it, so that whoever waits for this process
 *     sees what happened to the command.
 *
 * All of it without running anything of the program's own (_exit, not
 * exit): a replaced program runs none of its exit handlers. */
void __wasmify_exec_become(pid_t pid) {
    closefrom(0);
    int status = 0;
    if (__wasmify_host_proc_exec((int)pid, &status) != 0) _exit(255);
    if (WIFSIGNALED(status)) __wasmify_host_proc_terminate(WTERMSIG(status));
    _exit(WEXITSTATUS(status));
}

/* execv and execvp: they return only when the command could not be
 * started, with errno set. */
int execv(const char *path, char *const argv[]) {
    posix_spawn_file_actions_t fa;
    posix_spawn_file_actions_init(&fa);
    posix_spawn_file_actions_adddup2(&fa, 0, 0);
    posix_spawn_file_actions_adddup2(&fa, 1, 1);
    posix_spawn_file_actions_adddup2(&fa, 2, 2);
    pid_t pid = -1;
    __wasmify_host_proc_exec_next(1);
    const int rc = posix_spawn(&pid, path, &fa, NULL, argv, environ);
    posix_spawn_file_actions_destroy(&fa);
    if (rc != 0) {
        __wasmify_host_proc_exec_next(0);
        errno = rc;
        return -1;
    }
    __wasmify_exec_become(pid);
}

int execvp(const char *file, char *const argv[]) {
    posix_spawn_file_actions_t fa;
    posix_spawn_file_actions_init(&fa);
    posix_spawn_file_actions_adddup2(&fa, 0, 0);
    posix_spawn_file_actions_adddup2(&fa, 1, 1);
    posix_spawn_file_actions_adddup2(&fa, 2, 2);
    pid_t pid = -1;
    __wasmify_host_proc_exec_next(1);
    /* a bare name is looked up on PATH */
    const int rc = posix_spawnp(&pid, file, &fa, NULL, argv, environ);
    posix_spawn_file_actions_destroy(&fa);
    if (rc != 0) {
        __wasmify_host_proc_exec_next(0);
        errno = rc;
        return -1;
    }
    __wasmify_exec_become(pid);
}

} /* extern "C" */

#endif /* WASMIFY_HOST_PROCESS_SPAWN */
