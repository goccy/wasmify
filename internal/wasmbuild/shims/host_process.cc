/* host_process.cc — the process: its id and its parent's, its
 * environment, its process group and session, its resources, and fork.
 *
 * WASI has none of this but the environment, which libc keeps, and
 * wasi-libc declares none of the rest. The host answers through imports: a guest that runs as the host process (a command
 * line tool) gets the host's ids, and an isolated one the fixed identity its
 * host gives it.
 *
 * Nothing is cached here: an instance can be a copy (a memory snapshot) of
 * one that asked a different host.
 *
 * Compiled when a host capability it serves is given: the ids, the
 * scheduling priority and the CPU times with process.resources
 * (WASMIFY_HOST_PROCESS_RESOURCES); the changes of the environment, which
 * the host is told about (the link wraps setenv, unsetenv, putenv and
 * clearenv), with process.environment (WASMIFY_HOST_PROCESS_ENVIRONMENT);
 * process groups and sessions with process.job_control
 * (WASMIFY_HOST_PROCESS_JOB_CONTROL); __wasmify_fork and the child's entry
 * with process.fork (WASMIFY_HOST_PROCESS_FORK).
 */
#if defined(WASMIFY_HOST_PROCESS_RESOURCES) || defined(WASMIFY_HOST_PROCESS_ENVIRONMENT) || \
    defined(WASMIFY_HOST_PROCESS_JOB_CONTROL) || defined(WASMIFY_HOST_PROCESS_FORK)

#include <sys/times.h>
#include <sys/types.h>
#include <cerrno>
#include <cstdint>
#include <cstdlib>
#include <cstring>
#include <time.h>
#include <unistd.h>
#if defined(WASMIFY_HOST_PROCESS_FORK) && defined(WASMIFY_HOST_PROCESS_THREADS)
#include <pthread.h>
#endif

#include "wasmify_posix.h"

#ifdef WASMIFY_HOST_PROCESS_ENVIRONMENT
extern "C" {
int __real_setenv(const char *name, const char *value, int overwrite);
int __real_unsetenv(const char *name);
int __real_putenv(char *string);
int __real_clearenv(void);
extern char **environ;
} /* extern "C" */
#endif

namespace {

int fail(int error) {
    errno = error;
    return -1;
}


#ifdef WASMIFY_HOST_PROCESS_ENVIRONMENT
/* A name setenv and unsetenv take: not empty, and without '='. */
bool is_variable_name(const char *name) {
    return name != nullptr && name[0] != '\0' && strchr(name, '=') == nullptr;
}

/* Tells the host that the variable of the name_len bytes at name is set to
 * value, or removed (value null). 0, or -1 with errno set to the host's
 * refusal. */
int report_env(const char *name, size_t name_len, const char *value) {
    const int rc = __wasmify_host_proc_setenv(name, (int)name_len, value != nullptr ? value : "",
                                              value != nullptr ? (int)strlen(value) : 0,
                                              value != nullptr);
    return rc != 0 ? fail(-rc) : 0;
}
#endif

} /* namespace */

extern "C" {

#ifdef WASMIFY_HOST_PROCESS_RESOURCES
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
#endif

#ifdef WASMIFY_HOST_PROCESS_ENVIRONMENT
/* ---- the environment --------------------------------------------------------
 * The environment is libc's own table. Every change to it goes through
 * these (the link wraps the libc functions), which tell the host before
 * libc makes it: a change the host refuses fails with the host's errno and
 * leaves the table as it was. The C library's checks come first, so that
 * the host is told only of changes it makes. A C library that cannot make a
 * change the host has taken (ENOMEM) leaves the host told of it. */
int __wrap_setenv(const char *name, const char *value, int overwrite) {
    if (!is_variable_name(name) || value == nullptr) return fail(EINVAL);
    if (!overwrite && getenv(name) != nullptr) return 0;
    if (report_env(name, strlen(name), value) != 0) return -1;
    return __real_setenv(name, value, overwrite);
}

int __wrap_unsetenv(const char *name) {
    if (!is_variable_name(name)) return fail(EINVAL);
    if (report_env(name, strlen(name), nullptr) != 0) return -1;
    return __real_unsetenv(name);
}

/* A string without a value, or without a name, is unsetenv's, as musl's
 * putenv has it. */
int __wrap_putenv(char *string) {
    const char *eq = strchr(string, '=');
    if (eq == nullptr || eq == string) return __wrap_unsetenv(string);
    if (report_env(string, (size_t)(eq - string), eq + 1) != 0) return -1;
    return __real_putenv(string);
}

/* The variables are removed one at a time, each told to the host first:
 * when the host refuses one, the variables before it are gone on both
 * sides and the rest are kept on both. An entry unsetenv cannot remove
 * (one without a name, or without '=') is left to the final clearenv of
 * the C library. */
int __wrap_clearenv(void) {
    size_t i = 0;
    while (environ != nullptr && environ[i] != nullptr) {
        const size_t len = strcspn(environ[i], "=");
        if (len == 0 || environ[i][len] != '=') {
            i++;
            continue;
        }
        char *name = strndup(environ[i], len);
        if (name == nullptr) return fail(ENOMEM);
        const int rc = report_env(name, strlen(name), nullptr);
        const int removed = rc == 0 ? __real_unsetenv(name) : -1;
        const int error = errno;
        free(name);
        errno = error;
        if (removed != 0) return -1;
    }
    return __real_clearenv();
}
#endif

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

#ifdef WASMIFY_HOST_PROCESS_FORK
/* ---- fork ----------------------------------------------------------------------
 * What the child runs is kept here, where the child's copy of the memory
 * has it too. Where the program has threads (process.threads), a lock keeps
 * two forks from setting it at once; the child's copy is taken with the
 * lock held, and the child starts its own lock afresh. (A default mutex
 * fails only when it is misused, which these pairs do not.) */
static int (*g_fork_child)(void *arg);
static void *g_fork_arg;
#ifdef WASMIFY_HOST_PROCESS_THREADS
static pthread_mutex_t g_fork_lock = PTHREAD_MUTEX_INITIALIZER;
#endif

pid_t __wasmify_fork(int (*child)(void *arg), void *arg) {
#ifdef WASMIFY_HOST_PROCESS_THREADS
    pthread_mutex_lock(&g_fork_lock);
#endif
    g_fork_child = child;
    g_fork_arg = arg;
    int pid = -1;
    const int rc = __wasmify_host_proc_fork(&pid);
#ifdef WASMIFY_HOST_PROCESS_THREADS
    pthread_mutex_unlock(&g_fork_lock);
#endif
    if (rc != 0) return (pid_t)fail(-rc);
    return (pid_t)pid;
}

/* The child's entry: the host runs this export once in the copy of the
 * memory, and the child ends with its result as its exit status, as _exit
 * ends a process: the atexit handlers do not run and stdio is not flushed
 * (a child that wants them calls exit itself). */
__attribute__((export_name("__wasip1_posix_extension_fork_child"))) int __wasmify_fork_child(void) {
#ifdef WASMIFY_HOST_PROCESS_THREADS
    g_fork_lock = PTHREAD_MUTEX_INITIALIZER;
#endif
#ifdef WASMIFY_HOST_SIGNALS
    __wasmify_signal_fork_child();
#endif
    return g_fork_child(g_fork_arg);
}
#endif

} /* extern "C" */

#endif /* WASMIFY_HOST_PROCESS_RESOURCES || WASMIFY_HOST_PROCESS_ENVIRONMENT ||
          WASMIFY_HOST_PROCESS_JOB_CONTROL || WASMIFY_HOST_PROCESS_FORK */
