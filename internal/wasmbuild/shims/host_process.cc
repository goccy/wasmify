/* host_process.cc — the process: its id and its parent's, and its
 * resources.
 *
 * WASI has none of this, and wasi-libc declares none of it. The host
 * answers through imports: a guest that runs as the host process (a command
 * line tool) gets the host's ids, and an isolated one the fixed identity its
 * host gives it.
 *
 * Nothing is cached here: an instance can be a copy (a memory snapshot) of
 * one that asked a different host.
 *
 * Compiled when the host capability process.resources is given
 * (WASMIFY_HOST_PROCESS_RESOURCES): the ids, the scheduling priority and
 * the CPU times.
 */
#ifdef WASMIFY_HOST_PROCESS_RESOURCES

#include <sys/times.h>
#include <sys/types.h>
#include <cerrno>
#include <cstdint>
#include <time.h>
#include <unistd.h>

#include "wasmify_posix.h"

namespace {

int fail(int error) {
    errno = error;
    return -1;
}

} /* namespace */

extern "C" {

pid_t getpid(void) { return (pid_t)__wasmify_process_id(0); }
pid_t getppid(void) { return (pid_t)__wasmify_process_id(1); }

/* ---- scheduling priority ---------------------------------------------------
 * A process of the host has the host process's; an isolated one has none
 * (ENOSYS). -1 is a nice value as well as the error return: a caller tells
 * them apart by errno, which it clears before the call. */
int getpriority(int which, id_t who) {
    int nice_value = 0;
    const int rc = __wasmify_host_proc_priority(which, (int)who, 0, 0, &nice_value);
    if (rc != 0) return fail(-rc);
    return nice_value;
}

int setpriority(int which, id_t who, int value) {
    int nice_value = 0;
    const int rc = __wasmify_host_proc_priority(which, (int)who, 1, value, &nice_value);
    if (rc != 0) return fail(-rc);
    return 0;
}

int nice(int increment) {
    const int process = 0; /* PRIO_PROCESS, which wasi-libc does not define */
    int nice_value = 0;
    int rc = __wasmify_host_proc_priority(process, 0, 0, 0, &nice_value);
    if (rc == 0)
        rc = __wasmify_host_proc_priority(process, 0, 1, nice_value + increment, &nice_value);
    if (rc != 0) return fail(rc == -EACCES ? EPERM : -rc);
    return nice_value;
}

/* times(2). wasi-libc's emulation reports process time in clock()'s unit,
 * nanoseconds, while times reports it in clock ticks (sysconf(_SC_CLK_TCK),
 * the unit a caller divides by). This one reports in that unit, and what
 * the host accounts to the process: user and system time, its own and its
 * children's. Where the host keeps no such account (ENOSYS) the process
 * time is the emulated clock's: time elapsed since the instance started,
 * all of it user time. */
clock_t times(struct tms *buf) {
    const long ticks_per_sec = sysconf(_SC_CLK_TCK);
    if (ticks_per_sec <= 0) {
        errno = ENOSYS; /* the C library has no unit */
        return (clock_t)-1;
    }
    const clock_t now = clock();
    if (now == (clock_t)-1) {
        errno = ENOSYS; /* the processor time is not available */
        return (clock_t)-1;
    }
    const clock_t elapsed = (clock_t)((long long)now * ticks_per_sec / CLOCKS_PER_SEC);
    uint64_t nanos[4] = {0, 0, 0, 0};
    const int rc = __wasmify_host_proc_times(nanos);
    if (rc != 0 && rc != -ENOSYS) {
        errno = -rc;
        return (clock_t)-1;
    }
    if (buf) {
        if (rc == 0) {
            const uint64_t per_tick = 1000000000ull / (uint64_t)ticks_per_sec;
            buf->tms_utime = (clock_t)(nanos[0] / per_tick);
            buf->tms_stime = (clock_t)(nanos[1] / per_tick);
            buf->tms_cutime = (clock_t)(nanos[2] / per_tick);
            buf->tms_cstime = (clock_t)(nanos[3] / per_tick);
        } else {
            buf->tms_utime = elapsed;
            buf->tms_stime = 0;
            buf->tms_cutime = 0;
            buf->tms_cstime = 0;
        }
    }
    return elapsed;
}

} /* extern "C" */

#endif /* WASMIFY_HOST_PROCESS_RESOURCES */
