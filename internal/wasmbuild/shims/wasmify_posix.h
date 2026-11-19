/* wasmify_posix.h — the host imports behind wasmify's POSIX shims, and the
 * building blocks the shims export.
 *
 * WASI preview1 has no processes, users, signals, terminals, System V IPC,
 * syslog or time zones. wasmify's host-capability shims (host_fs.cc,
 * host_subprocess.cc, host_sockets.cc and the files beside them) implement
 * the libc functions for them on imports of the module "wasmify_posix",
 * which the host implements (wasm2go's WasiStubs). Every import of that
 * module is declared here, once: the shims include this header, and so may
 * an application that calls an import itself (for what no libc function
 * expresses, such as resuming a forked guest at a point of its own).
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
struct termios;
struct sembuf;

#define WASMIFY_POSIX_IMPORT(name) __attribute__((import_module("wasmify_posix"), import_name(#name)))

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
/* Reports the working directory the guest has changed to (an absolute
 * path): the host process follows when the guest is it. */
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

/* ---- descriptors and processes (host_subprocess.cc, host_fcntl.cc,
 *      host_signal.cc for kill, host_process.cc for times) -------------- */
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
/* Closes every descriptor from low_fd up. */
WASMIFY_POSIX_IMPORT(fd_closefrom)
int __wasmify_host_fd_closefrom(int low_fd);
/* fork(2)'s host side: the host makes a new instance from a copy of this
 * one's memory (with a copy of the descriptor table whose slots share this
 * process's open descriptors) and returns the child's pid here. The child
 * has no native call stack; where it resumes is the application's to
 * arrange, through the copied memory. */
WASMIFY_POSIX_IMPORT(proc_fork)
int __wasmify_host_proc_fork(int *pid_out);
/* Sends sig to pid as kill(2) does (0 or less: a process group). */
WASMIFY_POSIX_IMPORT(proc_kill)
int __wasmify_host_proc_kill(int pid, int sig);
/* Waits for the command pid in this process's name: from here on every
 * signal sent to this process goes to the command. 0 with the command's
 * wait status at status_out. */
WASMIFY_POSIX_IMPORT(proc_exec)
int __wasmify_host_proc_exec(int pid, int *status_out);
/* Says (on 1) that the next command this process starts is the one it
 * becomes: the host then starts it with what the process made of its
 * session (a process that leads a session of its own is replaced by a
 * command that leads one, with its controlling terminal). on 0 takes it
 * back, for a command that could not be started. */
WASMIFY_POSIX_IMPORT(proc_exec_next)
int __wasmify_host_proc_exec_next(int on);
/* Ends the process as one killed by sig. */
__attribute__((noreturn)) WASMIFY_POSIX_IMPORT(proc_terminate)
void __wasmify_host_proc_terminate(int sig);
/* The CPU time of the process, in nanoseconds: user and system time of the
 * process itself, then of the children it has waited for. */
WASMIFY_POSIX_IMPORT(proc_times)
int __wasmify_host_proc_times(uint64_t times[4]);

/* ---- the process, its user and groups, the system (host_process.cc,
 *      host_users.cc, host_system.cc) ------------------------------------ */
WASMIFY_POSIX_IMPORT(proc_ids)
int __wasmify_host_proc_ids(uint32_t ids[6]);
WASMIFY_POSIX_IMPORT(proc_groups)
int __wasmify_host_proc_groups(uint32_t *gids, int capacity, uint32_t *count);
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
 * request names the call (host_process.cc's SET_ constants), a, b and c
 * are its arguments. -ENOSYS when the process has no ids of the host's to
 * change. */
WASMIFY_POSIX_IMPORT(proc_setids)
int __wasmify_host_proc_setids(int request, int a, int b, int c);
WASMIFY_POSIX_IMPORT(proc_setgroups)
int __wasmify_host_proc_setgroups(const uint32_t *gids, int count);
/* Process groups and sessions: the group of pid (0: the caller); moving pid
 * (0: the caller, or a child of it) into group pgid (0: one of its own);
 * starting a session. */
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
/* What uname reports, for a process that is the host process: five strings
 * (a u32 length, then the bytes). *needed receives the size of the record,
 * also when buf is too small (-ERANGE). -ENOSYS otherwise. */
WASMIFY_POSIX_IMPORT(proc_uname)
int __wasmify_host_proc_uname(void *buf, int buf_len, uint32_t *needed);
/* Reports a change of the environment: name is set to value, or removed
 * when has_value is 0. The host process follows when the program is it. */
WASMIFY_POSIX_IMPORT(proc_setenv)
int __wasmify_host_proc_setenv(const char *name, int name_len, const char *value, int value_len,
                               int has_value);

/* ---- signals and the interval timer (host_signal.cc) -------------------- */
/* Registers the word of this memory the host stores 1 into when it records
 * a signal for this process: an uninterruptible guest looks at it at its
 * safe points. */
WASMIFY_POSIX_IMPORT(proc_signal_register)
int __wasmify_host_proc_signal_register(void *flag);
/* The signals with a handler, the ignored ones and the blocked ones (bit
 * sig-1 each). */
WASMIFY_POSIX_IMPORT(proc_signal_dispositions)
int __wasmify_host_proc_signal_dispositions(uint64_t handled, uint64_t ignored, uint64_t blocked);
/* Takes the signals the host has recorded for this process. */
WASMIFY_POSIX_IMPORT(proc_signal_take)
int __wasmify_host_proc_signal_take(uint64_t *mask);
/* 1 when the process ignores sig by an action code outside the guest set
 * (a native library's sigaction), 0 otherwise. */
WASMIFY_POSIX_IMPORT(proc_signal_ignored)
int __wasmify_host_proc_signal_ignored(int sig);
/* The real-time interval timer (ITIMER_REAL). */
WASMIFY_POSIX_IMPORT(proc_itimer)
int __wasmify_host_proc_itimer(int set, int64_t value_ns, int64_t interval_ns,
                               struct __wasmify_timer_state *state);

/* ---- time zones (host_time.cc) ------------------------------------------ */
WASMIFY_POSIX_IMPORT(clock_zone)
int __wasmify_host_clock_zone(const char *tz, int tz_len, int has_tz, int64_t t, int local,
                              struct __wasmify_zone *out);

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

/* ---- terminals (host_tty.cc) -------------------------------------------- */
/* In the host's numbers; -ENOTTY for a descriptor that is no terminal of
 * the host's. */
WASMIFY_POSIX_IMPORT(tty_getattr)
int __wasmify_host_tty_getattr(int fd, struct termios *settings);
WASMIFY_POSIX_IMPORT(tty_setattr)
int __wasmify_host_tty_setattr(int fd, int actions, const struct termios *settings);
WASMIFY_POSIX_IMPORT(tty_speed)
int __wasmify_host_tty_speed(struct termios *settings, int output, int set, unsigned int speed,
                             unsigned int *was);
WASMIFY_POSIX_IMPORT(tty_control)
int __wasmify_host_tty_control(int fd, int what, int arg);
WASMIFY_POSIX_IMPORT(tty_pgrp)
int __wasmify_host_tty_pgrp(int fd, int set, int pgrp, int *was);
WASMIFY_POSIX_IMPORT(tty_name)
int __wasmify_host_tty_name(int fd, char *buf, int cap, int *len);
/* A request of ioctl this C library does not answer, made of the host:
 * request is the host's number, and the argument is a number (buffer 0:
 * arg is it) or memory the request reads and writes (arg_len bytes at arg,
 * in the host's layout). What the call returned is written at result.
 * -EINVAL for a request that is not one of a terminal's. The caller knows
 * the size of the argument; ioctl(2) does not say it. */
WASMIFY_POSIX_IMPORT(fd_ioctl)
int __wasmify_host_fd_ioctl(int fd, unsigned int request, int buffer, uintptr_t arg,
                            unsigned int arg_len, int *result);

/* ---- System V IPC (host_ipc.cc) ----------------------------------------- */
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
/* shm_copy: size bytes between the segment, from its byte pos, and buf;
 * into buf when write is 0, out of it otherwise. */
WASMIFY_POSIX_IMPORT(ipc_shmcopy)
int __wasmify_host_ipc_shmcopy(int id, void *buf, unsigned long long pos,
                               unsigned long long size, int write);

/* ---- the host's numbers and system calls -------------------------------- */
/* Whose numbers the program sees: 0 this C library's own, otherwise the
 * host's (the program is the host process). */
WASMIFY_POSIX_IMPORT(host_numbering)
int __wasmify_host_numbering(void);
/* The index-th number of a family of the host's: its name in C (name_len
 * bytes at name, which holds name_cap) and its value. -ENOENT past the
 * last one. */
WASMIFY_POSIX_IMPORT(host_number_at)
int __wasmify_host_number_at(int family, int index, char *name, int name_cap, int *name_len,
                             long long *value);
/* The host's system call of the number with nargs (at most six) arguments
 * at args, an argument whose bit is set in addresses being an address of
 * this memory, which the host makes the process's; its result is written
 * at result. -ENOSYS for a program that is not the host process; 1 when
 * the host does not make the call for a forked process. The caller knows
 * which arguments are addresses; syscall(2) does not say it. */
WASMIFY_POSIX_IMPORT(sys_syscall)
int __wasmify_host_sys_syscall(long long number, const unsigned long long *args, int nargs,
                               unsigned int addresses, long long *result);

/* ---- sockets (host_sockets.cc) ------------------------------------------ */
WASMIFY_POSIX_IMPORT(sock_open)
int __wasmify_host_sock_open(int family, int type);
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
/* gethostbyname's lookup, as the host's gethostbyname does it where the
 * host can (it is not getaddrinfo's: darwin's getaddrinfo keeps a socket
 * open that gethostbyname does not, which every command started afterwards
 * would inherit). The record is the number of IPv4 addresses (u32), the
 * addresses (4 bytes each, as in s_addr), then the official name, not
 * NUL-terminated: *needed receives its size, also when buf is too small
 * (-ERANGE). -ENOENT when the name has no IPv4 address. */
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
 * then the aliases. -ENOENT when there is no such service. */
WASMIFY_POSIX_IMPORT(sock_service)
int __wasmify_host_sock_service(const char *name, int name_len, int port, int by_port,
                                const char *proto, int proto_len, void *buf, int buf_len,
                                unsigned int *needed);
WASMIFY_POSIX_IMPORT(sock_listen)
int __wasmify_host_sock_listen(int fd, int backlog);
/* The socket's local name (peer 0) or its peer's. */
WASMIFY_POSIX_IMPORT(sock_name)
int __wasmify_host_sock_name(int fd, int peer, struct __wasmify_sock_addr *out);
/* The address of a record as the host's C library has it in a struct
 * sockaddr (cap bytes of room at out, the length at *len), and the
 * reverse. */
WASMIFY_POSIX_IMPORT(sock_addr_to_host)
int __wasmify_host_sock_addr_to_host(const struct __wasmify_sock_addr *addr, void *out, int cap,
                                     int *len);
WASMIFY_POSIX_IMPORT(sock_addr_from_host)
int __wasmify_host_sock_addr_from_host(const void *raw, int len, struct __wasmify_sock_addr *addr);
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
WASMIFY_POSIX_IMPORT(sock_setopt)
int __wasmify_host_sock_setopt(int fd, int option, int value);
WASMIFY_POSIX_IMPORT(sock_getopt)
int __wasmify_host_sock_getopt(int fd, int option, int *value);
WASMIFY_POSIX_IMPORT(sock_socketpair)
int __wasmify_host_sock_socketpair(int family, int type, int fds[2]);

#undef WASMIFY_POSIX_IMPORT

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
 * before it returns. Returns the mask before. */
uint64_t __wasmify_signal_native_mask(int kind, uint64_t mask);
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

/* host_subprocess.cc. The second half of an exec: pid is the command the
 * process started after __wasmify_host_proc_exec_next(1). The process
 * closes its descriptors, waits for the command in its name and ends the
 * way the command ended, without running anything of its own (_exit). */
__attribute__((noreturn)) void __wasmify_exec_become(pid_t pid);

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
