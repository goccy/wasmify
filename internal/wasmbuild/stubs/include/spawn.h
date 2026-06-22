/* spawn.h — POSIX process-spawn declarations for wasm32-wasi.
 *
 * wasi-sdk ships no <spawn.h>. This overlay provides the types and prototypes
 * a C library references under HAVE_POSIX_SPAWN. The actual implementations
 * live in the project bridge, compiled only when host subprocess support is
 * enabled (-DWASMIFY_HOST_SUBPROCESS); when disabled, HAVE_POSIX_SPAWN is left
 * unset so nothing references these symbols.
 *
 * Only stdio remapping is modelled: posix_spawn_file_actions records, via
 * adddup2, which guest fd becomes each of the child's fds 0/1/2 (enough for
 * subprocess pipes). Other file actions and the attr object carry no state and
 * are no-ops.
 */
#ifndef _WASMIFY_SPAWN_H
#define _WASMIFY_SPAWN_H

#include <sys/types.h>
#include <signal.h>

#ifdef __cplusplus
extern "C" {
#endif

struct sched_param; /* pointer-only; full type not needed on wasi */

/* Carries the child stdio source fds set by adddup2: __fd[k] is the guest fd
 * that becomes the child's fd k (0/1/2), or -1 to inherit. Only stdio
 * remapping is modelled (enough for subprocess pipes); other actions are
 * no-ops. */
typedef struct {
	int __fd[3];
} posix_spawn_file_actions_t;

typedef struct {
	int __wasmify_unused;
} posix_spawnattr_t;

#define POSIX_SPAWN_RESETIDS      0x01
#define POSIX_SPAWN_SETPGROUP     0x02
#define POSIX_SPAWN_SETSIGDEF     0x04
#define POSIX_SPAWN_SETSIGMASK    0x08
#define POSIX_SPAWN_SETSCHEDPARAM 0x10
#define POSIX_SPAWN_SETSCHEDULER  0x20
#define POSIX_SPAWN_SETSID        0x80

int posix_spawn(pid_t *__restrict, const char *__restrict,
                const posix_spawn_file_actions_t *,
                const posix_spawnattr_t *__restrict,
                char *const[], char *const[]);
int posix_spawnp(pid_t *__restrict, const char *__restrict,
                 const posix_spawn_file_actions_t *,
                 const posix_spawnattr_t *__restrict,
                 char *const[], char *const[]);

int posix_spawn_file_actions_init(posix_spawn_file_actions_t *);
int posix_spawn_file_actions_destroy(posix_spawn_file_actions_t *);
int posix_spawn_file_actions_addopen(posix_spawn_file_actions_t *__restrict,
                                     int, const char *__restrict, int, mode_t);
int posix_spawn_file_actions_addclose(posix_spawn_file_actions_t *, int);
int posix_spawn_file_actions_adddup2(posix_spawn_file_actions_t *, int, int);
int posix_spawn_file_actions_addclosefrom_np(posix_spawn_file_actions_t *, int);

int posix_spawnattr_init(posix_spawnattr_t *);
int posix_spawnattr_destroy(posix_spawnattr_t *);
int posix_spawnattr_setflags(posix_spawnattr_t *, short);
int posix_spawnattr_setpgroup(posix_spawnattr_t *, pid_t);
int posix_spawnattr_setsigmask(posix_spawnattr_t *__restrict,
                               const sigset_t *__restrict);
int posix_spawnattr_setsigdefault(posix_spawnattr_t *__restrict,
                                  const sigset_t *__restrict);
int posix_spawnattr_setschedparam(posix_spawnattr_t *__restrict,
                                  const struct sched_param *__restrict);
int posix_spawnattr_setschedpolicy(posix_spawnattr_t *, int);

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_SPAWN_H */
