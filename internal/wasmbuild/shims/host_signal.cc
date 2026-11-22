/* host_signal.cc — signals for a process that cannot be interrupted.
 *
 * A WASI guest is ordinary code on one thread of the host: nothing can make
 * it run a handler at an arbitrary instruction. A program whose handlers
 * only note a signal, for code of its own to act on at a safe point (an
 * interpreter's), does not need that. This file supplies the part of the
 * scheme the kernel and libc normally play:
 *
 *   - the disposition table (sigaction, signal), the signal mask
 *     (sigprocmask, sigsuspend, sigpending) and the signal sets;
 *   - delivery. A signal the process sends itself (kill with its own pid,
 *     raise, which WASI's proc_raise posts) is delivered before the call
 *     returns. A signal from outside — another
 *     process, the alarm timer, the embedder — is recorded by the host,
 *     which also stores 1 into the word the program registered
 *     (proc_signal_register); the program calls __wasmify_signal_absorb
 *     at its next safe point, which takes the recorded signals from the
 *     host and delivers them;
 *   - the real-time interval timer (alarm, ualarm, setitimer and getitimer
 *     with ITIMER_REAL), which the host keeps and which posts SIGALRM;
 *   - the default actions: nothing for SIGCHLD, SIGURG, SIGWINCH, SIGCONT
 *     and the stop signals (stopping has no meaning here), and the end of
 *     the process for every other signal, reported by the host as a death
 *     by that signal.
 *
 * Blocking host calls (sleep, select, waitpid, accept, reads of pipes and
 * sockets) return EINTR when a signal is recorded, so the safe point comes.
 *
 * Signal masks cross to the host as 64-bit values in which signal n is bit
 * n-1.
 *
 * Compiled when the host capability signals is given (WASMIFY_HOST_SIGNALS);
 * the interval timer with signals.timers (WASMIFY_HOST_SIGNALS_TIMERS).
 */
#ifdef WASMIFY_HOST_SIGNALS
#include <signal.h>
#include <sys/time.h>
#include <cerrno>
#include <climits>
#include <cstdint>
#include <cstring>
#include <unistd.h>
#include <sys/uio.h>
#include <wasi/api.h>

#include "wasmify_posix.h"

extern "C" {

/* WASI's proc_raise, which wasi-libc no longer declares: a signal for this
 * process (a WASI number), posted by the host. Its argument is as wide as a
 * pointer, as every argument of wasi_snapshot_preview1's imports is on
 * wasm64. Returns 0 or a WASI errno. */
__attribute__((import_module("wasi_snapshot_preview1"), import_name("proc_raise")))
int __wasmify_host_proc_raise(long sig);

#ifdef WASMIFY_HOST_SIGNALS_TIMERS
/* The real-time interval timer. state receives what the timer was before
 * the call: the nanoseconds until it fires next (0: not set) and its reload
 * interval (0: it fires once). When set is non-zero the timer is replaced
 * by one that fires value_ns from now and every interval_ns after that; a
 * value of 0 disarms it. */
struct __wasmify_timer_state {
    uint64_t left_ns;
    uint64_t interval_ns;
};
#endif
} /* extern "C" */

namespace {

typedef void (*handler_t)(int);

int fail(int error) {
    errno = error;
    return -1;
}

inline uint64_t sig_bit(int sig) { return (uint64_t)1 << (sig - 1); }
inline bool sig_valid(int sig) { return sig >= 1 && sig < NSIG; }

/* The WASI number (__wasi_signal_t) of a signal of this C library, whose
 * numbers are Linux's: WASI has no SIGSTKFLT, so from SIGCHLD on its
 * numbers are one lower. -1 for a signal WASI has no number for. */
int wasi_signal_of(int sig) {
    if (sig >= 1 && sig < SIGSTKFLT) return sig;
    if (sig > SIGSTKFLT && sig <= SIGSYS) return sig - 1;
    return -1;
}

const uint64_t kDefaultIgnored =
    sig_bit(SIGCHLD) | sig_bit(SIGCONT) | sig_bit(SIGSTOP) | sig_bit(SIGTSTP) |
    sig_bit(SIGTTIN) | sig_bit(SIGTTOU) | sig_bit(SIGURG) | sig_bit(SIGWINCH);
/* SIGKILL and SIGSTOP can be neither caught, ignored nor blocked. */
const uint64_t kUnmaskable = sig_bit(SIGKILL) | sig_bit(SIGSTOP);

/* ---- signal sets ----------------------------------------------------------
 * wasi-libc's sigset_t is a placeholder: one unsigned char. It cannot hold
 * a set, and programs are compiled against it (struct sigaction embeds
 * one), so its size is fixed. The byte therefore NAMES
 * a set: it is an index into this table of the distinct sets the process
 * has made, 0 being the empty set. A sigset_t is still a plain value —
 * copying it copies the set, since table entries never change. The table
 * has an entry for each value the byte can take; when every one names a
 * set, the function that needed a new entry fails with ENOMEM. */
uint64_t g_sets[UCHAR_MAX + 1]; /* g_sets[0] == 0: the empty set */
int g_set_count = 1;

uint64_t set_mask(const sigset_t *set) {
    const unsigned idx = (unsigned char)*set;
    return idx < (unsigned)g_set_count ? g_sets[idx] : 0;
}

/* Stores mask in *set. Returns 0, or -1 with errno set. */
int set_assign(sigset_t *set, uint64_t mask) {
    for (int i = 0; i < g_set_count; i++) {
        if (g_sets[i] == mask) {
            *set = (sigset_t)i;
            return 0;
        }
    }
    if (g_set_count == UCHAR_MAX + 1) {
        errno = ENOMEM;
        return -1;
    }
    g_sets[g_set_count] = mask;
    *set = (sigset_t)g_set_count++;
    return 0;
}

/* ---- dispositions and mask ------------------------------------------------ */
typedef void (*info_handler_t)(int, siginfo_t *, void *);

/* The handler field of an action whose handler takes the siginfo_t
 * (SA_SIGINFO, which info holds): neither SIG_DFL nor SIG_IGN. It is never
 * called. */
void info_handler_mark(int) {}

struct action {
    handler_t handler;   /* SIG_DFL, SIG_IGN, the handler, or info_handler_mark */
    info_handler_t info; /* the handler of SA_SIGINFO */
    uint64_t mask;       /* blocked while the handler runs */
    int flags;
};
action g_actions[NSIG]; /* zero-initialised: every signal at SIG_DFL */
uint64_t g_blocked = 0; /* the signal mask */
uint64_t g_held = 0;    /* delivered while blocked: pending until unblocked */
/* Counts handler runs, so that pause and sigsuspend know one happened. */
unsigned long g_handler_runs = 0;

/* Tells the host which signals have a handler and which are ignored, so
 * that it neither keeps nor interrupts a blocking call for a signal nobody
 * would notice (it knows the default actions), and so that it knows a
 * process that ignores SIGCHLD: the children of one leave nothing to wait
 * for. The host always takes them (proc_signal_dispositions returns 0). */
void report_dispositions() {
    uint64_t handled = 0, ignored = 0;
    for (int sig = 1; sig < NSIG; sig++) {
        const handler_t h = g_actions[sig].handler;
        if (h == SIG_IGN)
            ignored |= sig_bit(sig);
        else if (h != SIG_DFL)
            handled |= sig_bit(sig);
    }
    (void)__wasmify_host_proc_signal_dispositions(handled, ignored, g_blocked);
}

void release_held();

/* Delivers a signal that is not blocked: runs its handler, or takes its
 * default action. */
void deliver(int sig) {
    const action act = g_actions[sig];
    if (act.handler == SIG_IGN) return;
    if (act.handler == SIG_DFL) {
        if (kDefaultIgnored & sig_bit(sig)) return;
        __wasmify_host_proc_terminate(sig);
    }
    if (act.flags & SA_RESETHAND) {
        g_actions[sig].handler = SIG_DFL;
        g_actions[sig].info = NULL;
        g_actions[sig].flags &= ~SA_SIGINFO;
        report_dispositions();
    }
    /* The handler runs with its own mask added, as sigaction specifies. The
     * host is not told about this short-lived mask: it only matters to
     * signals the handler sends to its own process, which are delivered
     * right here. */
    const uint64_t saved = g_blocked;
    g_blocked |= act.mask;
    if (!(act.flags & SA_NODEFER)) g_blocked |= sig_bit(sig);
    g_blocked &= ~kUnmaskable;
    g_handler_runs++;
    if (act.info) {
        siginfo_t info;
        memset(&info, 0, sizeof(info));
        info.si_signo = sig;
        act.info(sig, &info, NULL);
    } else {
        act.handler(sig);
    }
    g_blocked = saved;
    release_held();
}

/* A signal has arrived for this process: deliver it, or hold it while it is
 * blocked. */
void arrive(int sig) {
    if (g_blocked & sig_bit(sig)) {
        const handler_t h = g_actions[sig].handler;
        /* An ignored signal is discarded even while blocked. */
        if (h == SIG_IGN || (h == SIG_DFL && (kDefaultIgnored & sig_bit(sig)))) return;
        g_held |= sig_bit(sig);
        return;
    }
    deliver(sig);
}

/* Delivers the held signals that are no longer blocked, lowest first. */
void release_held() {
    for (;;) {
        const uint64_t ready = g_held & ~g_blocked;
        if (!ready) return;
        const int sig = __builtin_ctzll(ready) + 1;
        g_held &= ~sig_bit(sig);
        deliver(sig);
    }
}

/* Takes the signals the host has recorded for this process and lets each
 * arrive. proc_signal_take fails only for memory out of range (EFAULT),
 * which posted is not; nothing has been taken then. */
void absorb_posted() {
    uint64_t posted = 0;
    if (__wasmify_host_proc_signal_take(&posted) != 0) return;
    while (posted) {
        const int sig = __builtin_ctzll(posted) + 1;
        posted &= posted - 1;
        if (sig_valid(sig)) arrive(sig);
    }
}

/* Blocks until the host reports a signal for this process. The wait is a
 * poll on the monotonic clock reaching its last value, which it never
 * does: the host ends the poll with EINTR when it records a signal.
 * A host whose clock does reach it ends the poll with the clock's event,
 * and the wait goes on. Returns 0, or the errno of a poll that failed. */
int wait_for_signal() {
    for (;;) {
        __wasi_subscription_t sub;
        memset(&sub, 0, sizeof(sub));
        sub.u.tag = __WASI_EVENTTYPE_CLOCK;
        sub.u.u.clock.id = __WASI_CLOCKID_MONOTONIC;
        sub.u.u.clock.timeout = UINT64_MAX;
        sub.u.u.clock.flags = __WASI_SUBCLOCKFLAGS_SUBSCRIPTION_CLOCK_ABSTIME;
        __wasi_event_t event;
        __wasi_size_t nevents = 0;
        const __wasi_errno_t rc = __wasi_poll_oneoff(&sub, &event, 1, &nevents);
        if (rc == __WASI_ERRNO_INTR) return 0;
        if (rc != __WASI_ERRNO_SUCCESS) return rc;
    }
}

#ifdef WASMIFY_HOST_SIGNALS_TIMERS
/* ---- interval timer times -------------------------------------------------- */
const uint64_t kNanosPerSecond = 1000000000ull;

bool timeval_valid(const struct timeval &tv) {
    return tv.tv_sec >= 0 && tv.tv_usec >= 0 && tv.tv_usec < 1000000;
}

/* A valid timeval in nanoseconds; a time too far off to count is the
 * farthest one there is. */
int64_t nanos_of(const struct timeval &tv) {
    const int64_t limit = INT64_MAX / (int64_t)kNanosPerSecond - 1;
    if ((int64_t)tv.tv_sec > limit) return INT64_MAX;
    return (int64_t)tv.tv_sec * (int64_t)kNanosPerSecond + (int64_t)tv.tv_usec * 1000;
}

/* Nanoseconds as a timeval, rounded up to the microsecond. */
struct timeval timeval_of(uint64_t nanos) {
    struct timeval tv;
    uint64_t seconds = nanos / kNanosPerSecond;
    uint64_t micros = (nanos % kNanosPerSecond + 999) / 1000;
    if (micros == 1000000) {
        seconds++;
        micros = 0;
    }
    tv.tv_sec = (time_t)seconds;
    tv.tv_usec = (suseconds_t)micros;
    return tv;
}
#endif

} /* namespace */

extern "C" {

/* SIG_IGN and SIG_ERR are the addresses of these two under
 * _WASI_EMULATED_SIGNAL (1 and -1 are not usable function pointers in
 * wasm). wasi-libc's emulation defines them next to its own signal() and
 * raise(), which the definitions below replace; defining them here keeps
 * that object file out of the link. Neither is ever called. */
void __SIG_IGN(int) {}
void __SIG_ERR(int) {}

int sigemptyset(sigset_t *set) { return set_assign(set, 0); }

int sigfillset(sigset_t *set) {
    return set_assign(set, sig_bit(NSIG - 1) | (sig_bit(NSIG - 1) - 1));
}

int sigaddset(sigset_t *set, int sig) {
    if (!sig_valid(sig)) {
        errno = EINVAL;
        return -1;
    }
    return set_assign(set, set_mask(set) | sig_bit(sig));
}

int sigdelset(sigset_t *set, int sig) {
    if (!sig_valid(sig)) {
        errno = EINVAL;
        return -1;
    }
    return set_assign(set, set_mask(set) & ~sig_bit(sig));
}

int sigismember(const sigset_t *set, int sig) {
    if (!sig_valid(sig)) {
        errno = EINVAL;
        return -1;
    }
    return (set_mask(set) & sig_bit(sig)) != 0;
}

int sigaction(int sig, const struct sigaction *act, struct sigaction *old) {
    if (!sig_valid(sig)) {
        errno = EINVAL;
        return -1;
    }
    if (act && (sig == SIGKILL || sig == SIGSTOP)) {
        errno = EINVAL;
        return -1;
    }
    /* The old action is written out first: a set table that has no room
     * for its mask fails the call with nothing changed. */
    const action prev = g_actions[sig];
    if (old) {
        sigset_t prev_mask;
        if (set_assign(&prev_mask, prev.mask) != 0) return -1;
        memset(old, 0, sizeof(*old));
        if (prev.info)
            old->sa_sigaction = prev.info;
        else
            old->sa_handler = prev.handler;
        old->sa_flags = prev.flags;
        old->sa_mask = prev_mask;
    }
    if (act) {
        action next;
        /* SIG_DFL and SIG_IGN given through sa_sigaction are SIG_DFL and
         * SIG_IGN (the two members share the storage). */
        const bool info = (act->sa_flags & SA_SIGINFO) != 0 && act->sa_handler != SIG_DFL &&
                          act->sa_handler != SIG_IGN;
        next.handler = info ? info_handler_mark : act->sa_handler;
        next.info = info ? act->sa_sigaction : NULL;
        next.mask = set_mask(&act->sa_mask);
        next.flags = act->sa_flags;
        g_actions[sig] = next;
        const handler_t h = g_actions[sig].handler;
        /* Setting a signal to be ignored discards it if it is pending. */
        if (h == SIG_IGN || (h == SIG_DFL && (kDefaultIgnored & sig_bit(sig))))
            g_held &= ~sig_bit(sig);
        report_dispositions();
    }
    return 0;
}

handler_t signal(int sig, handler_t handler) {
    struct sigaction act, old;
    memset(&act, 0, sizeof(act));
    act.sa_handler = handler;
    act.sa_flags = SA_RESTART;
    if (sigaction(sig, &act, &old) != 0) return SIG_ERR;
    return old.sa_handler;
}

int sigprocmask(int how, const sigset_t *set, sigset_t *old) {
    const uint64_t prev = g_blocked;
    if (set) {
        const uint64_t mask = set_mask(set);
        switch (how) {
        case SIG_BLOCK:
            g_blocked = prev | mask;
            break;
        case SIG_UNBLOCK:
            g_blocked = prev & ~mask;
            break;
        case SIG_SETMASK:
            g_blocked = mask;
            break;
        default:
            errno = EINVAL;
            return -1;
        }
        g_blocked &= ~kUnmaskable;
    }
    if (old && set_assign(old, prev) != 0) {
        g_blocked = prev;
        return -1;
    }
    if (set && g_blocked != prev) {
        report_dispositions();
        /* What the host held back while it was blocked, and what was held
         * here, is delivered before sigprocmask returns. */
        absorb_posted();
        release_held();
    }
    return 0;
}

/* The program's mask as native code changes it with sigprocmask or
 * pthread_sigmask: kind 0 blocks the signals
 * of mask, 1 unblocks them, 2 makes mask the mask, 3 only asks; a signal it
 * unblocks that came meanwhile is delivered before it returns, as
 * sigprocmask delivers it. *old receives the mask before. Returns 0, or -1
 * with errno set and the mask unchanged. */
int __wasmify_signal_native_mask(int kind, uint64_t mask, uint64_t *old) {
    if (kind < 0 || kind > 3) return fail(EINVAL);
    /* the set in one entry of the table, not one for each signal added */
    sigset_t set = 0, prev = 0;
    if (kind != 3 && set_assign(&set, mask & (sig_bit(NSIG - 1) | (sig_bit(NSIG - 1) - 1))) != 0)
        return -1;
    const int how = kind == 0 ? SIG_BLOCK : kind == 1 ? SIG_UNBLOCK : SIG_SETMASK;
    if (sigprocmask(how, kind == 3 ? NULL : &set, &prev) != 0) return -1;
    *old = set_mask(&prev);
    return 0;
}

/* The mask a sigset_t of the program's holds, and a sigset_t made to hold
 * a mask, in one entry of the table (the sets native code hands
 * sigprocmask). __wasmify_signal_set_store returns 0, or -1 with errno set
 * when the table is full. */
uint64_t __wasmify_signal_set_mask(const void *set) { return set_mask((const sigset_t *)set); }

int __wasmify_signal_set_store(void *set, uint64_t mask) {
    return set_assign((sigset_t *)set, mask & (sig_bit(NSIG - 1) | (sig_bit(NSIG - 1) - 1)));
}

/* What the host posted while a call of the program's was made, delivered
 * as the call returns: a signal the call itself raised (SIGPIPE, which a
 * write into a pipe nobody reads raises) is delivered before the call
 * returns, as the kernel delivers it to a process of its own. */
void __wasmify_signal_after_call(void) {
    absorb_posted();
}

int sigpending(sigset_t *set) {
    absorb_posted(); /* the host holds the blocked signals it recorded */
    return set_assign(set, g_held);
}

/* Waits, with the signal mask replaced by *mask, until a handler has run. */
int sigsuspend(const sigset_t *mask) {
    const uint64_t saved = g_blocked;
    const unsigned long runs = g_handler_runs;
    g_blocked = set_mask(mask) & ~kUnmaskable;
    report_dispositions();
    absorb_posted();
    release_held();
    int error = EINTR;
    while (g_handler_runs == runs) {
        const int rc = wait_for_signal();
        if (rc != 0) {
            error = rc;
            break;
        }
        absorb_posted();
    }
    g_blocked = saved;
    report_dispositions();
    /* What came while the wait's mask held it, and what the restored mask
     * lets in, is delivered before sigsuspend returns. */
    absorb_posted();
    release_held();
    errno = error;
    return -1;
}

int pause(void) {
    const unsigned long runs = g_handler_runs;
    absorb_posted();
    while (g_handler_runs == runs) {
        const int rc = wait_for_signal();
        if (rc != 0) return fail(rc);
        absorb_posted();
    }
    errno = EINTR;
    return -1;
}

int raise(int sig) {
    if (sig == 0) return 0;
    const int wasi_sig = wasi_signal_of(sig);
    if (wasi_sig < 0) {
        errno = EINVAL;
        return -1;
    }
    const int rc = __wasmify_host_proc_raise(wasi_sig);
    if (rc != 0) {
        errno = rc;
        return -1;
    }
    /* The host posts the signal; it is delivered before raise returns. */
    absorb_posted();
    return 0;
}

/* abort: the process ends by SIGABRT. A handler for the signal runs first;
 * when it returns, or the signal is ignored or blocked, the process ends
 * by the signal all the same. (wasi-libc's abort is a trap, which reaches
 * the host as a crash of the instance and not as a process that ended.) */
void abort(void) {
    if (!(g_blocked & sig_bit(SIGABRT))) {
        const handler_t h = g_actions[SIGABRT].handler;
        if (h != SIG_IGN && h != SIG_DFL) deliver(SIGABRT);
    }
    __wasmify_host_proc_terminate(SIGABRT);
}

#ifdef WASMIFY_HOST_SIGNALS_TIMERS
/* ---- the interval timer ---------------------------------------------------
 * One timer, kept by the host, behind all four functions: alarm and ualarm
 * are setitimer(ITIMER_REAL) with their own units, as POSIX describes them.
 * Times that were left are rounded up, so a timer that is set never reads
 * as zero. alarm and ualarm cannot fail: proc_itimer refuses only a
 * negative time (EINVAL), which their unsigned arguments cannot give. */
unsigned int alarm(unsigned int seconds) {
    __wasmify_timer_state old = {0, 0};
    (void)__wasmify_host_proc_itimer(1, (int64_t)seconds * kNanosPerSecond, 0, &old);
    return (unsigned int)((old.left_ns + kNanosPerSecond - 1) / kNanosPerSecond);
}

unsigned ualarm(unsigned value, unsigned interval) {
    __wasmify_timer_state old = {0, 0};
    (void)__wasmify_host_proc_itimer(1, (int64_t)value * 1000, (int64_t)interval * 1000, &old);
    const uint64_t left = (old.left_ns + 999) / 1000;
    return left > UINT_MAX ? UINT_MAX : (unsigned)left;
}

int getitimer(int which, struct itimerval *current) {
    if (which != ITIMER_REAL) {
        errno = EINVAL;
        return -1;
    }
    __wasmify_timer_state state = {0, 0};
    const int rc = __wasmify_host_proc_itimer(0, 0, 0, &state);
    if (rc != 0) {
        errno = -rc;
        return -1;
    }
    current->it_value = timeval_of(state.left_ns);
    current->it_interval = timeval_of(state.interval_ns);
    return 0;
}

int setitimer(int which, const struct itimerval *next, struct itimerval *old) {
    if (which != ITIMER_REAL) {
        errno = EINVAL;
        return -1;
    }
    if (next && (!timeval_valid(next->it_value) || !timeval_valid(next->it_interval))) {
        errno = EINVAL;
        return -1;
    }
    __wasmify_timer_state state = {0, 0};
    const int rc = next ? __wasmify_host_proc_itimer(1, nanos_of(next->it_value),
                                                nanos_of(next->it_interval), &state)
                        : __wasmify_host_proc_itimer(0, 0, 0, &state);
    if (rc != 0) {
        errno = -rc;
        return -1;
    }
    if (old) {
        old->it_value = timeval_of(state.left_ns);
        old->it_interval = timeval_of(state.interval_ns);
    }
    return 0;
}
#endif

/* What the host is to know of this process's signals, told again: an
 * instance may be a copy of one that told it before — made from a
 * snapshot of its memory, or by fork — for which the host knows none of it.
 * A program calls it once it has registered its word with the host
 * (proc_signal_register), whenever it is entered; the host answers at once
 * when it has nothing to learn. */
void __wasmify_signal_report(void) { report_dispositions(); }

/* The signals the host recorded for this process, delivered: a program
 * that runs its handlers at safe points calls this from there. */
void __wasmify_signal_absorb(void) { absorb_posted(); }

/* The child side of fork: signals pending for the parent are not pending
 * for the child (the interval timer is not inherited either; the host
 * starts the child without one). */
void __wasmify_signal_fork_child(void) { g_held = 0; }

/* A wait of native code with a signal mask of its own (epoll_pwait, ppoll,
 * pselect), which the kernel puts in place for the wait alone. begin:
 * mask is the signal mask from now on, and the one before is put in *old;
 * a signal that was held and is let in is delivered at once. Otherwise the
 * wait has ended: what arrived while it ran is delivered, with its mask
 * still in place, and then mask is the signal mask again, and what it lets
 * in is delivered. Returns whether a handler ran, which ends a wait with
 * EINTR. */
int __wasmify_signal_wait_mask(int begin, uint64_t mask, uint64_t *old) {
    const unsigned long runs = g_handler_runs;
    if (begin) {
        *old = g_blocked;
    } else {
        absorb_posted();
        release_held();
    }
    if ((mask & ~kUnmaskable) != g_blocked) {
        g_blocked = mask & ~kUnmaskable;
        report_dispositions();
        absorb_posted();
        release_held();
    }
    return g_handler_runs != runs;
}

/* ---- kill ------------------------------------------------------------------ */
int kill(pid_t pid, int sig) {
    if (sig < 0 || sig >= NSIG) {
        errno = EINVAL;
        return -1;
    }
    /* The host finds the processes pid names. A signal it posts to this
     * process (its own pid, its group, or every process it may signal) is
     * delivered before kill returns. */
    const int rc = __wasmify_host_proc_kill((int)pid, sig);
    if (rc != 0) return fail(-rc);
    absorb_posted();
    return 0;
}

/* killpg(pgrp) is kill(-pgrp): group 0 is the caller's own. kill(-1) is
 * not the group 1 but every process, so group 1 is the caller's own when
 * the caller is pid 1 (the group its pid names), and names no group
 * otherwise. */
int killpg(pid_t pgrp, int sig) {
    if (pgrp < 0) return fail(EINVAL);
    if (pgrp == 1) {
        if (__wasmify_process_id(0) != 1) return fail(ESRCH);
        return kill(0, sig);
    }
    return kill(-pgrp, sig);
}

/* ---- the SIGPIPE of a write ----------------------------------------------
 * A write into a pipe or socket nobody reads any more fails with EPIPE, and
 * the host posts the program SIGPIPE as well. The kernel delivers that
 * signal before the call returns: its default action ends the process
 * there, and a handler that only marks it (an interpreter's, which runs
 * its handlers at its next safe point) has marked it by then. So the posted
 * signals are delivered as such a call returns. The C library's calls are
 * wrapped at link time (the link wraps write, writev and send with
 * signals). */
extern "C" ssize_t __real_write(int fd, const void *buf, size_t count);
extern "C" ssize_t __wrap_write(int fd, const void *buf, size_t count) {
    const ssize_t r = __real_write(fd, buf, count);
    if (r < 0 && errno == EPIPE) {
        __wasmify_signal_after_call();
        errno = EPIPE;
    }
    return r;
}
extern "C" ssize_t __real_writev(int fd, const struct iovec *iov, int count);
extern "C" ssize_t __wrap_writev(int fd, const struct iovec *iov, int count) {
    const ssize_t r = __real_writev(fd, iov, count);
    if (r < 0 && errno == EPIPE) {
        __wasmify_signal_after_call();
        errno = EPIPE;
    }
    return r;
}
extern "C" ssize_t __real_send(int fd, const void *buf, size_t len, int flags);
extern "C" ssize_t __wrap_send(int fd, const void *buf, size_t len, int flags) {
    const ssize_t r = __real_send(fd, buf, len, flags);
    if (r < 0 && errno == EPIPE) {
        __wasmify_signal_after_call();
        errno = EPIPE;
    }
    return r;
}

} /* extern "C" */

#endif /* WASMIFY_HOST_SIGNALS */
