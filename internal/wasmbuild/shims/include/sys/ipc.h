/* sys/ipc.h — System V IPC, which wasi-libc has no header for.
 *
 * The semaphores, message queues and shared memory segments are the host's
 * (host_ipc.cc asks the host for each call: the ipc_* imports). The
 * numbers below are Linux's, on every architecture, and so are the selectors
 * of sys/sem.h, sys/msg.h and sys/shm.h: they are the C library's own
 * numbers, which the host translates into its own where they differ. The
 * structures are laid out as Linux's are on a 64-bit architecture
 * (asm-generic, and musl's), with fields of fixed widths; the host fills
 * them from its own and reads them back field by field.
 */
#ifndef WASMIFY_SYS_IPC_H
#define WASMIFY_SYS_IPC_H

#include <sys/types.h>

#ifdef __cplusplus
extern "C" {
#endif

struct ipc_perm {
    key_t __key;
    uid_t uid;
    gid_t gid;
    uid_t cuid;
    gid_t cgid;
    mode_t mode;
    int __seq;
    long __unused1;
    long __unused2;
};

#define IPC_CREAT 01000
#define IPC_EXCL 02000
#define IPC_NOWAIT 04000

#define IPC_RMID 0
#define IPC_SET 1
#define IPC_STAT 2
#define IPC_INFO 3

#define IPC_PRIVATE ((key_t)0)

key_t ftok(const char *path, int id);

#ifdef __cplusplus
}
#endif

#endif
