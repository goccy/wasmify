/* A program that exercises host shims at run time; host_shims_run_test.go
 * builds it with every host capability, transpiles it with wasm2go and runs
 * it once per mode (argv[1]). Each mode prints what it found. */
#define _GNU_SOURCE
#include <poll.h>
#include <errno.h>
#include <fcntl.h>
#include <signal.h>
#include <spawn.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/wait.h>
#include <unistd.h>

#include "wasmify_posix.h"

extern char **environ;

static volatile uint32_t word;
static volatile int runs;

static void count(int sig) {
    (void)sig;
    runs++;
}

/* A signal that comes while blocked is delivered once when it is let in:
 * by sigprocmask, by sigsuspend and by a wait with a mask of its own. Any
 * number of distinct signal sets holds. */
static int signals_once(void) {
    if (__wasmify_host_proc_signal_register(&word) != 0) return 1;
    struct sigaction sa;
    memset(&sa, 0, sizeof(sa));
    sa.sa_handler = count;
    if (sigaction(SIGUSR1, &sa, NULL) != 0) return 2;
    sigset_t usr1, none, pending;
    if (sigemptyset(&usr1) != 0 || sigaddset(&usr1, SIGUSR1) != 0 || sigemptyset(&none) != 0) return 3;

    if (sigprocmask(SIG_BLOCK, &usr1, NULL) != 0) return 4;
    if (kill(getpid(), SIGUSR1) != 0) return 5;
    if (sigpending(&pending) != 0) return 6;
    printf("blocked: %d %d\n", runs, sigismember(&pending, SIGUSR1));
    if (sigprocmask(SIG_UNBLOCK, &usr1, NULL) != 0) return 7;
    printf("unblocked: %d\n", runs);

    runs = 0;
    if (sigprocmask(SIG_BLOCK, &usr1, NULL) != 0 || raise(SIGUSR1) != 0) return 8;
    const int rc = sigsuspend(&none);
    const int e = errno;
    if (sigprocmask(SIG_UNBLOCK, &usr1, NULL) != 0) return 9;
    printf("sigsuspend: %d %d %d\n", rc, e == EINTR, runs);

    runs = 0;
    if (sigprocmask(SIG_BLOCK, &usr1, NULL) != 0 || raise(SIGUSR1) != 0) return 10;
    uint64_t old = 0, ignored = 0;
    const int began = __wasmify_signal_wait_mask(1, 0, &old);
    const int ended = __wasmify_signal_wait_mask(0, old, &ignored);
    if (sigprocmask(SIG_UNBLOCK, &usr1, NULL) != 0) return 11;
    printf("wait_mask: %d %d %d\n", began, ended, runs);

    int wrong = 0;
    for (int i = 0; i < 1024; i++) {
        sigset_t s;
        if (sigemptyset(&s) != 0) return 12;
        for (int sig = 1; sig < 32; sig++)
            if (((i >> (sig % 10)) & 1) && sigaddset(&s, sig) != 0) return 13;
        for (int sig = 1; sig < 32; sig++)
            if (sigismember(&s, sig) != ((i >> (sig % 10)) & 1)) wrong++;
    }
    printf("sets: %d %d\n", wrong, (int)sizeof(sigset_t));
    return 0;
}

/* A send with MSG_NOSIGNAL into a connection the peer has closed is EPIPE
 * alone; without it, the SIGPIPE is delivered before send returns. */
static int nosignal(void) {
    if (__wasmify_host_proc_signal_register(&word) != 0) return 1;
    int sv[2];
    if (socketpair(AF_UNIX, SOCK_STREAM, 0, sv) != 0) return 2;
    if (close(sv[1]) != 0) return 3;
    errno = 0;
    ssize_t n = send(sv[0], "x", 1, MSG_NOSIGNAL);
    printf("nosignal: %d %d\n", (int)n, errno == EPIPE);
    if (signal(SIGPIPE, count) == SIG_ERR) return 4;
    runs = 0;
    errno = 0;
    n = sendto(sv[0], "x", 1, 0, NULL, 0);
    printf("sigpipe: %d %d %d\n", (int)n, errno == EPIPE, runs);
    return 0;
}

/* A command inherits the descriptors that are not close-on-exec, and none
 * of the ones O_CLOEXEC (open, openat, pipe2, dup3) or SOCK_CLOEXEC made.
 * Run where the guest's descriptor numbers are the host's. The command is
 * a shell that tries each number; the numbers are kept under 10, from which
 * a shell keeps descriptors of its own, by closing what is not tried. */
static int cloexec(void) {
    int keep[2], drop[2];
    if (pipe(keep) != 0 || close(keep[1]) != 0) return 1;
    if (pipe2(drop, O_CLOEXEC) != 0 || close(drop[1]) != 0) return 2;
    const int file = open("/dev/null", O_RDONLY | O_CLOEXEC);
    const int at = openat(AT_FDCWD, "/dev/null", O_RDONLY | O_CLOEXEC);
    const int sock = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0);
    const int copy = dup(keep[0]);
    if (file < 0 || at < 0 || sock < 0 || copy < 0) return 3;
    if (dup3(keep[0], copy, O_CLOEXEC) != copy) return 4;
    const int fds[] = {keep[0], drop[0], file, at, sock, copy};
    char script[256];
    int n = snprintf(script, sizeof(script), "for fd in");
    for (size_t i = 0; i < sizeof(fds) / sizeof(fds[0]); i++) {
        if (fds[i] >= 10) return 5;
        n += snprintf(script + n, sizeof(script) - n, " %d", fds[i]);
    }
    snprintf(script + n, sizeof(script) - n,
             "; do if { : >&$fd; } 2>/dev/null; then echo open; else echo closed; fi; done");
    char *argv[] = {"sh", "-c", script, NULL};
    pid_t pid;
    if (posix_spawn(&pid, "/bin/sh", NULL, NULL, argv, environ) != 0) return 6;
    int status = 0;
    if (waitpid(pid, &status, 0) != pid) return 7;
    return WIFEXITED(status) && WEXITSTATUS(status) == 0 ? 0 : 8;
}

/* waitpid(0) waits for a child of the caller's group (a command stays in
 * it), and a group with no child, or none left, is ECHILD. */
static int waitgroup(void) {
    char *argv[] = {"sh", "-c", "exit 3", NULL};
    pid_t pid;
    if (posix_spawn(&pid, "/bin/sh", NULL, NULL, argv, environ) != 0) return 1;
    int status = 0;
    const pid_t reaped = waitpid(0, &status, 0);
    printf("group: %d %d\n", reaped == pid, WIFEXITED(status) ? WEXITSTATUS(status) : -1);
    errno = 0;
    printf("none left: %d %d\n", (int)waitpid(0, &status, 0), errno == ECHILD);
    errno = 0;
    printf("other group: %d %d\n", (int)waitpid(-999999, &status, WNOHANG), errno == ECHILD);
    return 0;
}

int main(int argc, char **argv) {
    if (argc != 2) return 100;
    if (strcmp(argv[1], "signals") == 0) return signals_once();
    if (strcmp(argv[1], "nosignal") == 0) return nosignal();
    if (strcmp(argv[1], "cloexec") == 0) return cloexec();
    if (strcmp(argv[1], "waitgroup") == 0) return waitgroup();
    return 101;
}
