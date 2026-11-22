/* wasmify_posix.h — the host imports behind wasmify's POSIX shims.
 *
 * WASI preview1 has no file modes, owners, creation masks, locks,
 * processes, signals, terminals, users, System V IPC, time zones or system
 * logger, and only a few socket calls. wasmify's host-capability shims
 * (host_fs.cc, host_fcntl.cc, host_subprocess.cc, host_process.cc,
 * host_signal.cc, host_tty.cc, host_users.cc, host_ipc.cc, host_system.cc,
 * host_time.cc, host_syslog.cc and host_sockets.cc) implement
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
struct __wasmify_sock_addr;
struct __wasmify_zone;
struct __wasmify_timer_state;
struct sembuf;

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
/* Tells the host the working directory the guest changes to, an absolute
 * path with its leading slash, before the guest does: an error is the
 * embedder's refusal, and the guest's chdir then fails with it. */
WASMIFY_POSIX_IMPORT(proc_chdir)
int __wasmify_host_proc_chdir(const char *path, int len);
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
/* The process becomes the command path, as proc_spawn starts one with
 * the process's descriptors 0, 1 and 2 as its standard streams: it keeps
 * its pid, its descriptors are closed, and it ends the way the command
 * ends. Returns only when the command could not be started. */
WASMIFY_POSIX_IMPORT(proc_exec)
int __wasmify_host_proc_exec(const char *path, char *const argv[], char *const envp[],
                             const char *cwd);
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

/* ---- the process's environment, process groups and resources
 *      (host_process.cc) ------------------------------------------------- */
/* Tells the host a change of the environment before the guest makes it:
 * name is set to value, or removed when has_value is 0. An error is the
 * embedder's refusal, and the guest's call then fails with it. */
WASMIFY_POSIX_IMPORT(proc_setenv)
int __wasmify_host_proc_setenv(const char *name, int name_len, const char *value, int value_len,
                               int has_value);
/* Process groups and sessions: the group of pid (0: the caller); moving pid
 * (0: the caller, or a child of it) into group pgid (0: one of its own);
 * starting a session, whose id is written at sid_out. */
WASMIFY_POSIX_IMPORT(proc_getpgid)
int __wasmify_host_proc_getpgid(int pid, int *pgid_out);
WASMIFY_POSIX_IMPORT(proc_setpgid)
int __wasmify_host_proc_setpgid(int pid, int pgid);
WASMIFY_POSIX_IMPORT(proc_setsid)
int __wasmify_host_proc_setsid(int *sid_out);
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

/* ---- sockets (host_sockets.cc) ------------------------------------------ */
/* A socket of the family and type (host_sockets.cc's WASMIFY_SOCK_
 * numbers). Returns the fd, or a negative errno. */
WASMIFY_POSIX_IMPORT(sock_open)
int __wasmify_host_sock_open(int family, int type);
/* connect and bind take an address record (family 0: none) and, for a
 * Unix-domain address, its path at (path, path_len). */
WASMIFY_POSIX_IMPORT(sock_connect_addr)
int __wasmify_host_sock_connect(int fd, const struct __wasmify_sock_addr *addr, const char *path,
                                int path_len);
WASMIFY_POSIX_IMPORT(sock_bind_addr)
int __wasmify_host_sock_bind(int fd, const struct __wasmify_sock_addr *addr, const char *path,
                             int path_len);
/* The addresses of a host name, of one family or (family 0) of both, IPv4
 * first: at most max records, *count receives their number. An address
 * written in numbers is converted. -ENOENT when the name has none. */
WASMIFY_POSIX_IMPORT(sock_resolve)
int __wasmify_host_sock_resolve(const char *node, int len, int family,
                                struct __wasmify_sock_addr *out, int max, unsigned int *count);
/* gethostbyname's lookup: the number of IPv4 addresses (u32), the
 * addresses (4 bytes each, as in s_addr), then the official name, not
 * NUL-terminated. *needed receives the size of the record, also when buf
 * is too small (-ERANGE). -ENOENT when the name has no IPv4 address. */
WASMIFY_POSIX_IMPORT(sock_host_by_name)
int __wasmify_host_sock_host_by_name(const char *node, int len, void *buf, int buf_len,
                                     unsigned int *needed);
/* The name of the host with an IP address, not NUL-terminated: *needed
 * receives its length, also when buf is too small (-ERANGE). -ENOENT when
 * the address has no name. */
WASMIFY_POSIX_IMPORT(sock_reverse_addr)
int __wasmify_host_sock_reverse(const struct __wasmify_sock_addr *addr, char *buf, int buf_len,
                                unsigned int *needed);
/* An entry of the host's services database, by name (by_port 0) or by
 * port, for the protocol proto when it is not empty. The record is the port
 * (u32), then the name and the protocol, each a u32 length and the bytes,
 * then the number of aliases (u32) and the aliases. *needed receives the
 * size of the record, also when buf is too small (-ERANGE). -ENOENT when
 * there is no such service. */
WASMIFY_POSIX_IMPORT(sock_service)
int __wasmify_host_sock_service(const char *name, int name_len, int port, int by_port,
                                const char *proto, int proto_len, void *buf, int buf_len,
                                unsigned int *needed);
WASMIFY_POSIX_IMPORT(sock_listen)
int __wasmify_host_sock_listen(int fd, int backlog);
/* The socket's local name (peer 0) or its peer's. */
WASMIFY_POSIX_IMPORT(sock_name)
int __wasmify_host_sock_name(int fd, int peer, struct __wasmify_sock_addr *out);
/* sendto: with addr, a datagram to that address (path as for connect);
 * without, a send to the socket's peer. flags are host_sockets.cc's
 * WASMIFY_MSG_ flags. *sent receives the count. */
WASMIFY_POSIX_IMPORT(sock_sendto_addr)
int __wasmify_host_sock_sendto(int fd, const void *buf, int len, int flags,
                               const struct __wasmify_sock_addr *addr, const char *path,
                               int path_len, int *sent);
/* recvfrom: one datagram (or, on a stream socket, what there is) into buf;
 * flags are host_sockets.cc's WASMIFY_MSG_ flags. *from receives the
 * sender's address, with family 0 when there is none. */
WASMIFY_POSIX_IMPORT(sock_recvfrom_addr)
int __wasmify_host_sock_recvfrom(int fd, void *buf, int len, int flags,
                                 struct __wasmify_sock_addr *from, int *received);
/* The receive (which 1) or send (which 2) timeout of a socket: *old_nanos
 * receives the timeout before the call, in nanoseconds (0: no limit); with
 * set it is then replaced by nanos. */
WASMIFY_POSIX_IMPORT(sock_timeout)
int __wasmify_host_sock_timeout(int fd, int which, int set, int64_t nanos, int64_t *old_nanos);
/* A socket option (host_sockets.cc's WASMIFY_SOCKOPT_ numbers). */
WASMIFY_POSIX_IMPORT(sock_setopt)
int __wasmify_host_sock_setopt(int fd, int option, int value);
WASMIFY_POSIX_IMPORT(sock_getopt)
int __wasmify_host_sock_getopt(int fd, int option, int *value);
/* Two Unix-domain sockets of the type, connected to each other. */
WASMIFY_POSIX_IMPORT(sock_socketpair)
int __wasmify_host_sock_socketpair(int family, int type, int fds[2]);
/* ---- System V IPC (host_ipc.cc) ----------------------------------------- */
/* The objects are the host's, in Linux's numbers and in the structures of
 * Linux on a 64-bit architecture (sys/ipc.h). A call that passes a
 * structure (msgctl, semctl and shmctl but for the commands that take
 * none, msgsnd, msgrcv) and shmat and shmdt are -ENOSYS for a wasm32
 * program, whose long is not the structures' width. */
/* msgget, semget and shmget (kind 0 message queue, 1 semaphore set, 2
 * shared memory segment): the id of the object of the kind with the key,
 * at id; arg is the number of semaphores of a set or the size of a
 * segment. */
WASMIFY_POSIX_IMPORT(ipc_get)
int __wasmify_host_ipc_get(int kind, int key, unsigned long long arg, int flags, int *id);
/* msgctl, semctl and shmctl: the command cmd on the object id (num: the
 * semaphore of a set). For IPC_STAT and IPC_SET buf is the kind's
 * structure; for GETALL and SETALL the values of the set's semaphores
 * (unsigned short each, as many as the set has); for SETVAL arg is the
 * value. What the call returns is written at result. */
WASMIFY_POSIX_IMPORT(ipc_ctl)
int __wasmify_host_ipc_ctl(int kind, int id, int num, int cmd, long long arg, void *buf,
                           long long *result);
/* semop: the nops operations at ops (struct sembuf each). */
WASMIFY_POSIX_IMPORT(ipc_semop)
int __wasmify_host_ipc_semop(int id, const struct sembuf *ops, unsigned long long nops);
/* msgsnd: the message at msg (its type, a long, then size bytes). */
WASMIFY_POSIX_IMPORT(ipc_msgsnd)
int __wasmify_host_ipc_msgsnd(int id, const void *msg, unsigned long long size, int flags);
/* msgrcv: a message of the type into msg (its type, then at most size
 * bytes); the number of its bytes is written at received. */
WASMIFY_POSIX_IMPORT(ipc_msgrcv)
int __wasmify_host_ipc_msgrcv(int id, void *msg, unsigned long long size, long long type,
                              int flags, long long *received);
/* shmat: the segment id is attached over the size bytes at addr, which the
 * program has set aside for it and which start at a page boundary of the
 * host, as far as the segment's whole pages go; flags are shmat's
 * (SHM_RDONLY). -ENOMEM where the program's memory is not one a segment
 * can be attached over. */
WASMIFY_POSIX_IMPORT(ipc_shmat)
int __wasmify_host_ipc_shmat(int id, void *addr, unsigned long long size, int flags);
/* shmdt: the segment ipc_shmat attached at addr is detached, and its
 * memory is the program's again, all zero. */
WASMIFY_POSIX_IMPORT(ipc_shmdt)
int __wasmify_host_ipc_shmdt(const void *addr);

/* ---- the host's numbers, its terminals and its system calls ------------- */
/* Whose numbers the program sees: 0 this C library's own, 1 those the host
 * gives (host_number_at). Returns no errno. */
WASMIFY_POSIX_IMPORT(host_numbering)
int __wasmify_host_numbering(void);
/* The index-th number of a family (1: the error numbers): its name in C
 * (at most name_cap bytes at name, not NUL-terminated, its length at
 * name_len) and its value. -ENOENT past the last one, -ERANGE for a name
 * longer than name_cap. */
WASMIFY_POSIX_IMPORT(host_number_at)
int __wasmify_host_number_at(int family, int index, char *name, int name_cap,
                             uint32_t *name_len, int64_t *value);
/* A request of a terminal, in Linux's numbers (asm-generic: host_tty.cc's
 * <sys/ioctl.h> and <termios.h>), made of the host's terminal behind fd:
 * arg is the int the request takes, or the address of what it takes.
 * Returns what the call returned (0 or more), or a negative errno: -ENOTTY
 * for a request that is not one of a terminal's and for a descriptor that
 * is no terminal. */
WASMIFY_POSIX_IMPORT(fd_ioctl)
int __wasmify_host_fd_ioctl(int fd, unsigned int request, uintptr_t arg);
/* The host's system call of the number (the Linux ABI's) with nargs (at
 * most six) arguments at args, an argument whose bit is set in addresses
 * being an address of this memory; its result is written at result.
 * -ENOSYS where the host makes no system calls for the program; 1 when the
 * host does not make the call for a forked process. The caller knows which
 * arguments are addresses; syscall(2) does not say it. */
WASMIFY_POSIX_IMPORT(sys_syscall)
int __wasmify_host_sys_syscall(long long number, const unsigned long long *args, int nargs,
                               unsigned int addresses, long long *result);

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
