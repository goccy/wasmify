/* unistd.h of the host capabilities: the C library's, and the functions
 * the host shims define that it does not declare. */
#include_next <unistd.h>

#ifndef _WASMIFY_HOST_UNISTD_H
#define _WASMIFY_HOST_UNISTD_H

#ifdef __cplusplus
extern "C" {
#endif

#ifdef WASMIFY_HOST_PROCESS_SPAWN
/* host_subprocess.cc */
int pipe(int fildes[2]);
int pipe2(int fildes[2], int flags);
int dup(int fd);
int dup2(int from, int to);
int dup3(int from, int to, int flags);
void closefrom(int low_fd);
int execve(const char *path, char *const argv[], char *const envp[]);
int execv(const char *path, char *const argv[]);
int execvp(const char *file, char *const argv[]);
int execl(const char *path, const char *arg0, ...) __attribute__((__sentinel__));
int execlp(const char *file, const char *arg0, ...) __attribute__((__sentinel__));
int execle(const char *path, const char *arg0, ...) __attribute__((__sentinel__(1)));
#endif

#ifdef WASMIFY_HOST_PROCESS_JOB_CONTROL
/* host_process.cc */
pid_t getpgid(pid_t pid);
pid_t getpgrp(void);
int setpgid(pid_t pid, pid_t pgid);
pid_t setsid(void);
#endif

#ifdef WASMIFY_HOST_TERMINAL
/* host_tty.cc */
char *ttyname(int fd);
int ttyname_r(int fd, char *name, size_t size);
#ifdef WASMIFY_HOST_PROCESS_JOB_CONTROL
pid_t tcgetpgrp(int fd);
int tcsetpgrp(int fd, pid_t pgrp);
#endif
#endif

#ifdef WASMIFY_HOST_PROCESS_RESOURCES
/* host_process.cc */
pid_t getppid(void);
int nice(int increment);
#endif

#ifdef WASMIFY_HOST_SIGNALS
/* host_signal.cc */
int pause(void);
#endif

#ifdef WASMIFY_HOST_SIGNALS_TIMERS
/* host_signal.cc (ualarm is the C library's to declare, with _GNU_SOURCE
 * or _BSD_SOURCE) */
unsigned alarm(unsigned seconds);
#endif

#ifdef WASMIFY_HOST_USERS
/* host_users.cc (cuserid is the C library's to declare in stdio.h, with
 * _GNU_SOURCE or _BSD_SOURCE) */
uid_t getuid(void);
uid_t geteuid(void);
gid_t getgid(void);
gid_t getegid(void);
int getgroups(int size, gid_t list[]);
int setgroups(size_t size, const gid_t *list);
int setuid(uid_t uid);
int seteuid(uid_t uid);
int setreuid(uid_t ruid, uid_t euid);
int setresuid(uid_t ruid, uid_t euid, uid_t suid);
int setgid(gid_t gid);
int setegid(gid_t gid);
int setregid(gid_t rgid, gid_t egid);
int setresgid(gid_t rgid, gid_t egid, gid_t sgid);
#endif

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_HOST_UNISTD_H */
