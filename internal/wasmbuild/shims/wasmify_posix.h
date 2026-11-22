/* wasmify_posix.h — the host imports behind wasmify's POSIX shims.
 *
 * WASI preview1 has no file modes, owners, creation masks, locks,
 * processes, signals, users, time zones or system logger. wasmify's
 * host-capability shims (host_fs.cc, host_fcntl.cc, host_subprocess.cc,
 * host_process.cc, host_signal.cc, host_users.cc, host_system.cc,
 * host_time.cc and host_syslog.cc) implement
 * the libc functions for them on imports of the module
 * "wasip1_posix_extension", which the host implements (wasm2go's
 * WasiStubs). Every import of that module is declared here, once: the shims
 * include this header, and so may an application that calls an import
 * itself.
 *
 * Unless a comment says otherwise, an import returns 0 or a negative errno
 * of this C library. Paths are absolute, without the leading slash, and not
 * NUL-terminated (a pointer and a length). Records the host fills are
 * described where the shim that reads them is.
 *
 * This header is valid C and C++.
 */
#ifndef WASMIFY_POSIX_H
#define WASMIFY_POSIX_H

#include <stdint.h>
#include <sys/types.h>

#ifdef __cplusplus
extern "C" {
#endif

/* The structures the imports exchange. Each is defined by the shim that
 * uses it, in the layout the host reads and writes. */
struct __wasmify_filestat_ext;
struct __wasmify_record_lock;
struct __wasmify_zone;
struct __wasmify_timer_state;

#define WASMIFY_POSIX_IMPORT(name) __attribute__((import_module("wasip1_posix_extension"), import_name(#name)))

/* ---- files (host_fs.cc) ------------------------------------------------- */
WASMIFY_POSIX_IMPORT(path_chmod)
int __wasmify_host_path_chmod(const char *path, int len, unsigned int mode);
WASMIFY_POSIX_IMPORT(fd_chmod)
int __wasmify_host_fd_chmod(int fd, unsigned int mode);
/* The owner of a file, by path (follow 0: of a symbolic link itself) and by
 * descriptor; an id of -1 stays as it is. -ENOSYS where files have no
 * owners to change. */
WASMIFY_POSIX_IMPORT(path_chown)
int __wasmify_host_path_chown(const char *path, int len, int uid, int gid, int follow);
WASMIFY_POSIX_IMPORT(fd_chown)
int __wasmify_host_fd_chown(int fd, int uid, int gid);
WASMIFY_POSIX_IMPORT(path_access)
int __wasmify_host_path_access(const char *path, int len, int amode, int flags);
WASMIFY_POSIX_IMPORT(path_filestat_ext)
int __wasmify_host_path_filestat_ext(const char *path, int len, int follow,
                                     struct __wasmify_filestat_ext *out);
WASMIFY_POSIX_IMPORT(fd_filestat_ext)
int __wasmify_host_fd_filestat_ext(int fd, struct __wasmify_filestat_ext *out);
/* Sets the process's file mode creation mask and returns the mask it
 * replaced (not an errno). */
WASMIFY_POSIX_IMPORT(proc_umask)
int __wasmify_host_proc_umask(unsigned int mask);
/* path_open for a preopen-relative path, plus the mode of open(2). */
WASMIFY_POSIX_IMPORT(path_open_mode)
int __wasmify_host_path_open_mode(int lookup_flags, const char *path, int len, int oflags,
                                  unsigned long long rights_base, int fdflags, unsigned int mode,
                                  int *fd_out);
WASMIFY_POSIX_IMPORT(path_create_directory_mode)
int __wasmify_host_path_create_directory_mode(const char *path, int len, unsigned int mode);
/* Creates a FIFO. -ENOSYS where the filesystem has none. */
WASMIFY_POSIX_IMPORT(path_mkfifo)
int __wasmify_host_path_mkfifo(const char *path, int len, unsigned int mode);
/* lock: 0 unlock, 1 shared, 2 exclusive. wait: non-zero to block. */
WASMIFY_POSIX_IMPORT(fd_flock)
int __wasmify_host_fd_flock(int fd, int lock, int wait);
/* The path a directory descriptor was opened under, absolute and not
 * NUL-terminated: *length receives its size, also when buf (of buf_len
 * bytes) is too small for it, which is -ERANGE. */
WASMIFY_POSIX_IMPORT(fd_path)
int __wasmify_host_fd_path(int fd, char *buf, int buf_len, int *length);

/* ---- descriptors and processes (host_subprocess.cc, host_fcntl.cc) ------ */
WASMIFY_POSIX_IMPORT(proc_spawn)
int __wasmify_host_proc_spawn(const char *path, char *const argv[], char *const envp[],
                              int stdin_fd, int stdout_fd, int stderr_fd, const char *cwd,
                              int *pid_out);
WASMIFY_POSIX_IMPORT(proc_wait)
int __wasmify_host_proc_wait(int pid, int options, int *status_out);
WASMIFY_POSIX_IMPORT(proc_wait_any)
int __wasmify_host_proc_wait_any(int options, int *pid_out, int *status_out);
WASMIFY_POSIX_IMPORT(pipe)
int __wasmify_host_pipe(int *fds_out);
/* fd_dup, fd_dup2 and fd_dupfd return 0 or a positive errno. */
WASMIFY_POSIX_IMPORT(fd_dup)
int __wasmify_host_fd_dup(int fd, int *fd_out);
WASMIFY_POSIX_IMPORT(fd_dup2)
int __wasmify_host_fd_dup2(int from, int to);
/* fd_dup under the lowest free number that is at least min. */
WASMIFY_POSIX_IMPORT(fd_dupfd)
int __wasmify_host_fd_dupfd(int fd, int min, int *fd_out);
/* A record lock of the host's file behind fd: cmd 0 F_GETLK (the lock in
 * the way is written back, type F_UNLCK when there is none), 1 F_SETLK,
 * 2 F_SETLKW. */
WASMIFY_POSIX_IMPORT(fd_lock)
int __wasmify_host_fd_lock(int fd, int cmd, struct __wasmify_record_lock *lock);
/* The close-on-exec flag of a descriptor: *old receives it as it was (0 or
 * 1); with set non-zero it then becomes value. */
WASMIFY_POSIX_IMPORT(fd_cloexec)
int __wasmify_host_fd_cloexec(int fd, int set, int value, int *old);
/* Closes every descriptor from low_fd up, each also when an earlier close
 * failed. Returns 0, or the positive errno of the first close that
 * failed. */
WASMIFY_POSIX_IMPORT(fd_closefrom)
int __wasmify_host_fd_closefrom(int low_fd);

/* ---- the process's user and groups, the system (host_users.cc,
 *      host_system.cc) --------------------------------------------------- */
/* The pid, the parent's pid, the real and effective user ids and the real
 * and effective group ids, in that order. */
WASMIFY_POSIX_IMPORT(proc_ids)
int __wasmify_host_proc_ids(uint32_t ids[6]);
WASMIFY_POSIX_IMPORT(proc_groups)
int __wasmify_host_proc_groups(uint32_t *gids, int capacity, uint32_t *count);
/* A user or a group of the host's databases, by id or by name: a record
 * of the layout host_users.cc reads, at buf; *needed receives its size,
 * also when buf is too small (-ERANGE). -ENOENT when there is none. */
WASMIFY_POSIX_IMPORT(proc_user_by_id)
int __wasmify_host_proc_user_by_id(uint32_t uid, void *buf, int buf_len, uint32_t *needed);
WASMIFY_POSIX_IMPORT(proc_user_by_name)
int __wasmify_host_proc_user_by_name(const char *name, int name_len, void *buf, int buf_len,
                                     uint32_t *needed);
WASMIFY_POSIX_IMPORT(proc_group_by_id)
int __wasmify_host_proc_group_by_id(uint32_t gid, void *buf, int buf_len, uint32_t *needed);
WASMIFY_POSIX_IMPORT(proc_group_by_name)
int __wasmify_host_proc_group_by_name(const char *name, int name_len, void *buf, int buf_len,
                                      uint32_t *needed);
/* The index-th entry of the host's user database (getpwent) and of its
 * group database (getgrent), as the lookups above hand one back; -ENOENT
 * past the last. */
WASMIFY_POSIX_IMPORT(proc_user_at)
int __wasmify_host_proc_user_at(int index, void *buf, int len, uint32_t *needed);
WASMIFY_POSIX_IMPORT(proc_group_at)
int __wasmify_host_proc_group_at(int index, void *buf, int len, uint32_t *needed);
/* The setuid and setgid families, for a process that is the host process:
 * request names the call (host_users.cc's SET_ constants), a, b and c are
 * its arguments. -ENOSYS when the process has no ids of the host's to
 * change. */
WASMIFY_POSIX_IMPORT(proc_setids)
int __wasmify_host_proc_setids(int request, int a, int b, int c);
WASMIFY_POSIX_IMPORT(proc_setgroups)
int __wasmify_host_proc_setgroups(const uint32_t *gids, int count);
/* What uname reports, for a process that is the host process: five strings
 * (a u32 length, then the bytes). *needed receives the size of the record,
 * also when buf is too small (-ERANGE). -ENOSYS otherwise. */
WASMIFY_POSIX_IMPORT(proc_uname)
int __wasmify_host_proc_uname(void *buf, int buf_len, uint32_t *needed);

/* ---- time zones (host_time.cc) ------------------------------------------ */
WASMIFY_POSIX_IMPORT(clock_zone)
int __wasmify_host_clock_zone(const char *tz, int tz_len, int has_tz, int64_t t, int local,
                              struct __wasmify_zone *out);

/* ---- the process's resources (host_process.cc) -------------------------- */
/* The nice value: which and who as getpriority takes them. With set
 * non-zero the value is changed first; the value then in effect is written
 * at nice_out. -ENOSYS for a process that is not a process of the host. */
WASMIFY_POSIX_IMPORT(proc_priority)
int __wasmify_host_proc_priority(int which, int who, int set, int value, int *nice_out);
/* The CPU time of the process, in nanoseconds: user and system time of the
 * process itself, then of the children it has waited for. -ENOSYS where the
 * host does not account for them. */
WASMIFY_POSIX_IMPORT(proc_times)
int __wasmify_host_proc_times(uint64_t times[4]);

/* ---- signals and the interval timer (host_signal.cc) -------------------- */
/* Registers the word of this memory the host stores 1 into, atomically,
 * when it records a signal for this process: an uninterruptible guest
 * reads it with an atomic load at its safe points. The word is 4-byte
 * aligned. Returns 1 when the word was registered already, 0 when the
 * registration is new (the guest then reports its dispositions,
 * __wasmify_signal_report), -EFAULT for a word outside the memory and
 * -EINVAL for one that is not aligned.
 *
 * A signal reaches a handler only in a program that has registered its
 * word: the host gives a signal for a program that has none (raise, kill
 * of itself, a signal from outside) its default action, which ends the
 * process for most signals. The shim registers no word of its own; the
 * program registers the one its safe points look at. */
WASMIFY_POSIX_IMPORT(proc_signal_register)
int __wasmify_host_proc_signal_register(volatile uint32_t *flag);
/* The signals with a handler, the ignored ones and the blocked ones (bit
 * sig-1 each). Returns 0. */
WASMIFY_POSIX_IMPORT(proc_signal_dispositions)
int __wasmify_host_proc_signal_dispositions(uint64_t handled, uint64_t ignored, uint64_t blocked);
/* Takes the signals the host has recorded for this process. */
WASMIFY_POSIX_IMPORT(proc_signal_take)
int __wasmify_host_proc_signal_take(uint64_t *mask);
/* Sends sig to pid as kill(2) does (0 or less: a process group); a signal
 * posted to this process is taken as raise's is. */
WASMIFY_POSIX_IMPORT(proc_kill)
int __wasmify_host_proc_kill(int pid, int sig);
/* The real-time interval timer (ITIMER_REAL). */
WASMIFY_POSIX_IMPORT(proc_itimer)
int __wasmify_host_proc_itimer(int set, int64_t value_ns, int64_t interval_ns,
                               struct __wasmify_timer_state *state);
/* Ends the process as one killed by sig. */
__attribute__((noreturn)) WASMIFY_POSIX_IMPORT(proc_terminate)
void __wasmify_host_proc_terminate(int sig);

/* ---- syslog (host_syslog.cc) -------------------------------------------- */
/* -ENOSYS: the instance has no system logger. facility is the facility's
 * number (LOG_FAC), severity 0..7. */
WASMIFY_POSIX_IMPORT(proc_syslog_open)
int __wasmify_host_proc_syslog_open(const char *ident, int ident_len, int options, int facility);
WASMIFY_POSIX_IMPORT(proc_syslog)
int __wasmify_host_proc_syslog(int severity, int facility, int options, const char *ident,
                               int ident_len, const char *message, int message_len);
WASMIFY_POSIX_IMPORT(proc_syslog_close)
int __wasmify_host_proc_syslog_close(void);

#undef WASMIFY_POSIX_IMPORT

/* One of the ids proc_ids reports, by its index (0 the pid, 1 the parent's
 * pid, 2 the real and 3 the effective user id, 4 the real and 5 the
 * effective group id). proc_ids fails only for memory out of range
 * (EFAULT), which ids is not, so the ids cannot fail to be read. */
static inline uint32_t __wasmify_process_id(int which) {
    uint32_t ids[6] = {0, 0, 0, 0, 0, 0};
    (void)__wasmify_host_proc_ids(ids);
    return ids[which];
}

/* ---- building blocks the shims export ----------------------------------- */

/* host_signal.cc. The signal mask and the signal sets are the shim's (a
 * sigset_t names a set; see there), and so is delivery: a signal is
 * delivered at the moment one of these, or a libc function of the shim,
 * takes the host's recorded signals in. */

/* Takes the signals the host has recorded for this process and delivers
 * each (runs its handler or takes its default action), or holds it while
 * it is blocked. An application that runs its handlers at safe points
 * calls this from there. */
void __wasmify_signal_absorb(void);
/* Tells the host the dispositions and the mask again: for an instance that
 * is a copy of one that told it before (a snapshot, a forked child), of
 * which the host knows nothing. */
void __wasmify_signal_report(void);
/* The child side of fork: signals pending for the parent are not pending
 * for the child. */
void __wasmify_signal_fork_child(void);
/* What the host posted while a call was made, delivered as the call
 * returns: a signal the call itself raised (SIGPIPE of a write) is
 * delivered before the call returns, as the kernel delivers it. */
void __wasmify_signal_after_call(void);
/* The mask as native code changes it with sigprocmask or pthread_sigmask:
 * kind 0 blocks the signals of mask, 1 unblocks them, 2 makes mask the
 * mask, 3 only asks; a signal it unblocks that came meanwhile is delivered
 * before it returns. *old receives the mask before. Returns 0, or -1 with
 * errno set (ENOMEM when the table of sets is full, EINVAL for another
 * kind); the mask is unchanged then. */
int __wasmify_signal_native_mask(int kind, uint64_t mask, uint64_t *old);
/* The mask a sigset_t holds, and a sigset_t made to hold a mask (0, or -1
 * with errno set when the table of sets is full). */
uint64_t __wasmify_signal_set_mask(const void *set);
int __wasmify_signal_set_store(void *set, uint64_t mask);
/* A wait of native code with a signal mask of its own (epoll_pwait, ppoll,
 * pselect). begin: mask is the mask from now on, the one before is put in
 * *old, and a signal that was held and is let in is delivered at once.
 * Otherwise the wait has ended: what arrived while it ran is delivered,
 * with its mask still in place, and then mask is the mask again. Returns
 * whether a handler ran, which ends a wait with EINTR. */
int __wasmify_signal_wait_mask(int begin, uint64_t mask, uint64_t *old);

/* host_syslog.cc. Called when the host does not take a message of openlog
 * or syslog (error: ENOSYS, no system logger; otherwise why not). syslog(3)
 * returns nothing and loses such a message silently; an application that
 * must tell defines this function (the shim's is a weak definition that
 * does nothing). */
void __wasmify_syslog_undelivered(int error);

#ifdef __cplusplus
}
#endif

#endif /* WASMIFY_POSIX_H */
