/* sys/time.h of the host capabilities: the C library's, and the real-time
 * interval timer host_signal.cc implements with signals.timers. wasi-libc
 * leaves setitimer, getitimer and their types out. ITIMER_REAL is the only
 * timer there is: the process CPU-time timers (ITIMER_VIRTUAL, ITIMER_PROF)
 * have nothing to count, and stay undefined so that nothing offers them. */
#include_next <sys/time.h>

#ifndef _WASMIFY_HOST_SYS_TIME_H
#define _WASMIFY_HOST_SYS_TIME_H

#ifdef WASMIFY_HOST_SIGNALS_TIMERS
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
int getitimer(int which, struct itimerval *current);
int setitimer(int which, const struct itimerval *next, struct itimerval *old);

#ifdef __cplusplus
}
#endif
#endif

#endif /* _WASMIFY_HOST_SYS_TIME_H */
