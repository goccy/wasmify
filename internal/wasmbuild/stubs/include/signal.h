/*
 * POSIX signal compatibility overlay for wasip1.
 *
 * wasi-sdk's signal.h guards struct sigaction, sigaction(), SA_* constants,
 * siginfo_t, and even the sigset_t __NEED definition behind
 * __wasilibc_unmodified_upstream. This overlay provides them.
 */
#include_next <signal.h>

#ifndef _WASMIFY_SIGNAL_OVERLAY_H
#define _WASMIFY_SIGNAL_OVERLAY_H

#include <sys/types.h>

#ifdef __cplusplus
extern "C" {
#endif

/* sigset_t — wasi-sdk guards the __NEED_sigset_t define, so define it here */
#ifndef __DEFINED_sigset_t
typedef struct __sigset_t { unsigned long __bits[128/sizeof(long)]; } sigset_t;
#define __DEFINED_sigset_t
#endif

/* SA_* flags */
#ifndef SA_NOCLDSTOP
#define SA_NOCLDSTOP  1
#define SA_NOCLDWAIT  2
#define SA_SIGINFO    4
#define SA_RESTART    0x10000000
#define SA_NODEFER    0x40000000
#define SA_RESETHAND  0x80000000
#define SA_RESTORER   0x04000000
#endif

/* siginfo_t — minimal definition */
#ifndef __siginfo_t_defined
#define __siginfo_t_defined
typedef struct {
    int si_signo;
    int si_errno;
    int si_code;
    pid_t si_pid;
    uid_t si_uid;
    void *si_addr;
    int si_status;
    union {
        int sival_int;
        void *sival_ptr;
    } si_value;
} siginfo_t;
#endif

/* struct sigaction */
#ifndef sa_handler
struct sigaction {
    union {
        void (*sa_handler)(int);
        void (*sa_sigaction)(int, siginfo_t *, void *);
    } __sa_handler;
    sigset_t sa_mask;
    int sa_flags;
    void (*sa_restorer)(void);
};
#define sa_handler   __sa_handler.sa_handler
#define sa_sigaction __sa_handler.sa_sigaction
#endif

/* Signal set manipulation */
int sigemptyset(sigset_t *);
int sigfillset(sigset_t *);
int sigaddset(sigset_t *, int);
int sigdelset(sigset_t *, int);
int sigismember(const sigset_t *, int);
int sigprocmask(int, const sigset_t *__restrict, sigset_t *__restrict);
int sigsuspend(const sigset_t *);
int sigpending(sigset_t *);

/* sigaction and kill */
int sigaction(int, const struct sigaction *__restrict, struct sigaction *__restrict);
int kill(pid_t, int);

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_SIGNAL_OVERLAY_H */
