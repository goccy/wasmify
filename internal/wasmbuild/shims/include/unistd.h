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

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_HOST_UNISTD_H */
