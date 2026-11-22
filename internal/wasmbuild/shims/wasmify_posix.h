/* wasmify_posix.h — the host imports behind wasmify's POSIX shims.
 *
 * WASI preview1 has no file modes, owners, creation masks, locks,
 * processes, users or time zones. wasmify's host-capability shims
 * (host_fs.cc, host_fcntl.cc, host_subprocess.cc, host_users.cc,
 * host_system.cc and host_time.cc) implement
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

#undef WASMIFY_POSIX_IMPORT

#ifdef __cplusplus
}
#endif

#endif /* WASMIFY_POSIX_H */
