/* host_process.cc — the process: its id and its parent's, its environment,
 * its process group and session, and its resources.
 *
 * WASI has none of this, and wasi-libc declares none of it. The host
 * answers through imports: a guest that runs as the host process (a command
 * line tool) gets the host's ids and databases, and an isolated one gets a
 * fixed identity — pid 42, parent 1, user and group 0 — and empty
 * databases. A process made by fork has the pid the host forked it under,
 * and for a parent the process that forked it, or 1 once that one is gone.
 *
 * Nothing is cached here: an instance can be a copy (a memory snapshot) of
 * one that asked a different host.
 *
 * Compiled when the host capability process is given (WASMIFY_HOST_PROCESS):
 * the ids and the environment, whose changes the host is told about (the
 * link wraps setenv, unsetenv, putenv and clearenv); process groups and
 * sessions with process.job_control; the scheduling priority and the CPU
 * times with process.resources.
 */
#ifdef WASMIFY_HOST_PROCESS

#include <sys/times.h>
#include <sys/types.h>
#include <cerrno>
#include <cstdint>
#include <cstdlib>
#include <cstring>
#include <time.h>
#include <unistd.h>

#include "wasmify_posix.h"

extern "C" {
int __real_setenv(const char *name, const char *value, int overwrite);
int __real_unsetenv(const char *name);
int __real_putenv(char *string);
int __real_clearenv(void);
extern char **environ;
} /* extern "C" */

namespace {

enum { ID_PID, ID_PPID, ID_UID, ID_EUID, ID_GID, ID_EGID };

uint32_t process_id(int which) {
    uint32_t ids[6] = {0, 0, 0, 0, 0, 0};
    __wasmify_host_proc_ids(ids);
    return ids[which];
}

#if defined(WASMIFY_HOST_PROCESS_JOB_CONTROL) || defined(WASMIFY_HOST_PROCESS_RESOURCES)
int fail(int error) {
    errno = error;
    return -1;
}
#endif

void report_env(const char *name, size_t name_len, const char *value) {
    __wasmify_host_proc_setenv(name, (int)name_len, value ? value : "",
                          value ? (int)strlen(value) : 0, value != NULL);
}

} /* namespace */

extern "C" {

pid_t getpid(void) { return (pid_t)process_id(ID_PID); }
pid_t getppid(void) { return (pid_t)process_id(ID_PPID); }

/* ---- the environment --------------------------------------------------------
 * The environment is libc's own table. Every change to it goes through
 * these (the link wraps the libc functions), which pass it on to the host
 * once libc has made it. */
int __wrap_setenv(const char *name, const char *value, int overwrite) {
    const int rc = __real_setenv(name, value, overwrite);
    if (rc == 0) report_env(name, strlen(name), getenv(name));
    return rc;
}

int __wrap_unsetenv(const char *name) {
    const int rc = __real_unsetenv(name);
    if (rc == 0) report_env(name, strlen(name), NULL);
    return rc;
}

int __wrap_putenv(char *string) {
    const int rc = __real_putenv(string);
    if (rc == 0) {
        const char *eq = strchr(string, '=');
        if (eq)
            report_env(string, (size_t)(eq - string), eq + 1);
        else
            report_env(string, strlen(string), NULL);
    }
    return rc;
}

int __wrap_clearenv(void) {
    /* the names first: they are gone once libc has cleared the table */
    if (environ) {
        for (char **e = environ; *e; e++) {
            const char *eq = strchr(*e, '=');
            report_env(*e, eq ? (size_t)(eq - *e) : strlen(*e), NULL);
        }
    }
    return __real_clearenv();
}

#ifdef WASMIFY_HOST_PROCESS_JOB_CONTROL
/* ---- process groups and sessions ---------------------------------------------
 * The host keeps them: the processes of a program are its to know. */
pid_t getpgid(pid_t pid) {
    int pgid = 0;
    const int rc = __wasmify_host_proc_getpgid((int)pid, &pgid);
    if (rc != 0) return (pid_t)fail(-rc);
    return (pid_t)pgid;
}

pid_t getpgrp(void) { return getpgid(0); }

int setpgid(pid_t pid, pid_t pgid) {
    const int rc = __wasmify_host_proc_setpgid((int)pid, (int)pgid);
    if (rc != 0) return fail(-rc);
    return 0;
}

pid_t setsid(void) {
    int sid = 0;
    const int rc = __wasmify_host_proc_setsid(&sid);
    if (rc != 0) return (pid_t)fail(-rc);
    return (pid_t)sid;
}
#endif

#ifdef WASMIFY_HOST_PROCESS_RESOURCES
/* ---- scheduling priority ---------------------------------------------------
 * A process of the host has the host process's; an isolated one has none
 * (ENOSYS). */
int getpriority(int which, id_t who) {
    int nice_value = 0;
    const int rc = __wasmify_host_proc_priority(which, (int)who, 0, 0, &nice_value);
    if (rc != 0) return fail(-rc);
    errno = 0; /* -1 is a legitimate value */
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
    errno = 0; /* -1 is a legitimate value */
    return nice_value;
}

/* times(2). wasi-libc's emulation reports process time in clock()'s unit,
 * nanoseconds, while times reports it in clock ticks (sysconf(_SC_CLK_TCK),
 * the unit a caller divides by). This one reports in that unit, and what
 * the host accounts to the process: user and system time, its own and its
 * children's. Where the host keeps no such account the process time is the
 * emulated clock's: time elapsed since the instance started, all of it user
 * time. */
clock_t times(struct tms *buf) {
    const long ticks_per_sec = sysconf(_SC_CLK_TCK);
    if (ticks_per_sec <= 0) {
        errno = ENOSYS; /* the C library has no unit */
        return (clock_t)-1;
    }
    const clock_t elapsed = (clock_t)((long long)clock() * ticks_per_sec / CLOCKS_PER_SEC);
    uint64_t nanos[4] = {0, 0, 0, 0};
    const bool accounted = __wasmify_host_proc_times(nanos) == 0;
    if (buf) {
        if (accounted) {
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
#endif

} /* extern "C" */

#endif /* WASMIFY_HOST_PROCESS */
