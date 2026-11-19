/*
 * POSIX sys/time.h compatibility overlay for wasip1.
 *
 * wasi-libc guards setitimer, getitimer and their types out of <sys/time.h>
 * ("WASI has no getitimer"). With host subprocess support, wasmify's signal
 * shim (host_signal.cc) implements both on the real-time interval timer the
 * host keeps, the one alarm() sets. ITIMER_REAL is the only timer there is:
 * the process CPU-time timers (ITIMER_VIRTUAL, ITIMER_PROF) have nothing to
 * count, and stay undefined so that nothing offers them.
 */
#include_next <sys/time.h>

#ifndef _WASMIFY_SYS_TIME_OVERLAY_H
#define _WASMIFY_SYS_TIME_OVERLAY_H

#ifdef __cplusplus
extern "C" {
#endif

#ifndef ITIMER_REAL
#define ITIMER_REAL 0
struct itimerval {
    struct timeval it_interval;
    struct timeval it_value;
};
#endif
int getitimer(int, struct itimerval *);
int setitimer(int, const struct itimerval *, struct itimerval *);

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_SYS_TIME_OVERLAY_H */
