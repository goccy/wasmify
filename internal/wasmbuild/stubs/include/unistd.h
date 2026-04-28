/*
 * POSIX unistd compatibility overlay for wasip1.
 *
 * wasi-sdk's unistd.h guards uid/gid functions and process management
 * behind __wasilibc_unmodified_upstream. This overlay adds them.
 */
#include_next <unistd.h>

#ifndef _WASMIFY_UNISTD_OVERLAY_H
#define _WASMIFY_UNISTD_OVERLAY_H

#include <sys/types.h>

#ifdef __cplusplus
extern "C" {
#endif

/* User/group identity */
uid_t getuid(void);
uid_t geteuid(void);
gid_t getgid(void);
gid_t getegid(void);
int setuid(uid_t);
int seteuid(uid_t);
int setgid(gid_t);
int setegid(gid_t);
int getgroups(int, gid_t []);
int setgroups(size_t, const gid_t *);

/* File descriptor duplication */
int dup(int);
int dup2(int, int);
int dup3(int, int, int);

/* Process management */
pid_t fork(void);
int execv(const char *, char *const []);
int execve(const char *, char *const [], char *const []);
int execvp(const char *, char *const []);
int pipe(int [2]);
unsigned int alarm(unsigned int);
int daemon(int, int);
char *getlogin(void);
int sethostname(const char *, size_t);
int gethostname(char *, size_t);

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_UNISTD_OVERLAY_H */
