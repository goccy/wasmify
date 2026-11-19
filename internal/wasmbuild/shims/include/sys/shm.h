/* sys/shm.h — System V shared memory (see sys/ipc.h). A segment is the
 * host's memory, which cannot be mapped into the program's: shmat and
 * shmdt fail with EINVAL, and a program copies between a segment and its
 * own memory through shm_copy, which this C library adds. */
#ifndef WASMIFY_SYS_SHM_H
#define WASMIFY_SYS_SHM_H

#include "ipc.h"
#include <time.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef unsigned long shmatt_t;

struct shmid_ds {
    struct ipc_perm shm_perm;
    size_t shm_segsz;
    time_t shm_atime;
    time_t shm_dtime;
    time_t shm_ctime;
    pid_t shm_cpid;
    pid_t shm_lpid;
    shmatt_t shm_nattch;
    unsigned long __unused1;
    unsigned long __unused2;
};

#define SHM_RDONLY 010000
#define SHM_RND 020000

int shmget(key_t key, size_t size, int flags);
int shmctl(int id, int cmd, struct shmid_ds *buf);
void *shmat(int id, const void *addr, int flags);
int shmdt(const void *addr);
/* Copies size bytes between the segment id, from its byte pos, and buf:
 * into buf when write is 0, out of it otherwise. 0, or -1 with errno set. */
int shm_copy(int id, void *buf, size_t pos, size_t size, int write);

#ifdef __cplusplus
}
#endif

#endif
