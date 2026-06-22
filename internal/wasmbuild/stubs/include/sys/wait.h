/* sys/wait.h — process-wait declarations for wasm32-wasi.
 *
 * wasi-sdk ships no <sys/wait.h>. This overlay provides waitpid()/wait() and
 * the W* status macros a C library references under HAVE_SYS_WAIT_H.
 * waitpid() is implemented in the project bridge (backed by
 * a proc_wait host import) only when -DWASMIFY_HOST_SUBPROCESS is set; when
 * disabled, HAVE_SYS_WAIT_H is left unset.
 *
 * Status encoding matches the convention the host produces: a normal exit N
 * is (N & 0xff) << 8 (WIFEXITED), a signal is the low-7-bits signal number
 * (WIFSIGNALED).
 */
#ifndef _WASMIFY_SYS_WAIT_H
#define _WASMIFY_SYS_WAIT_H

#include <sys/types.h>

#ifdef __cplusplus
extern "C" {
#endif

#define WNOHANG    1
#define WUNTRACED  2
#define WCONTINUED 8

#define WEXITSTATUS(s)  (((s) >> 8) & 0xff)
#define WTERMSIG(s)     ((s) & 0x7f)
#define WSTOPSIG(s)     WEXITSTATUS(s)
#define WIFEXITED(s)    (WTERMSIG(s) == 0)
#define WIFSIGNALED(s)  (((signed char)(((s) & 0x7f) + 1) >> 1) > 0)
#define WIFSTOPPED(s)   (((s) & 0xff) == 0x7f)
#define WIFCONTINUED(s) ((s) == 0xffff)
#define WCOREDUMP(s)    ((s) & 0x80)

pid_t waitpid(pid_t, int *, int);
pid_t wait(int *);

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_SYS_WAIT_H */
