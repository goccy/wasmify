/* host_ipc.cc — System V IPC for a libc that has none.
 *
 * The semaphore sets, message queues and shared memory segments are the
 * host's, which other processes of the system share: each call goes to the
 * host through an import, in the C library's numbers and with its
 * structures (<sys/ipc.h>, <sys/sem.h>, <sys/msg.h>, <sys/shm.h>), and the
 * host translates them into its own where they differ. Every import
 * returns 0 or a negative errno.
 *
 * A shared memory segment is the host's memory, which cannot be mapped into
 * the program's: shmat fails with EINVAL, and a program copies between a
 * segment and its own memory with shm_copy instead.
 *
 * Compiled when the host capability ipc.sysv is given (WASMIFY_HOST_IPC_SYSV).
 */
#ifdef WASMIFY_HOST_IPC_SYSV

#include <sys/types.h>
#include <sys/stat.h>
#include <cerrno>
#include <cstdarg>
#include <cstddef>
#include <cstdint>

#include <sys/ipc.h>
#include <sys/msg.h>
#include <sys/sem.h>
#include <sys/shm.h>

#include "wasmify_posix.h"

/* errno = error, and -1 */
static int fail(int error) {
    errno = error;
    return -1;
}

extern "C" {

/* The kinds of object, for ipc_get and ipc_ctl. */
enum { IPC_KIND_MSG = 0, IPC_KIND_SEM = 1, IPC_KIND_SHM = 2 };

static int ipc_get(int kind, key_t key, unsigned long long arg, int flags) {
    int id = -1;
    const int rc = __wasmify_host_ipc_get(kind, key, arg, flags, &id);
    return rc != 0 ? fail(-rc) : id;
}

static int ipc_ctl(int kind, int id, int num, int cmd, long long arg, void *buf) {
    long long result = 0;
    const int rc = __wasmify_host_ipc_ctl(kind, id, num, cmd, arg, buf, &result);
    return rc != 0 ? fail(-rc) : (int)result;
}

int msgget(key_t key, int flags) {
    return ipc_get(IPC_KIND_MSG, key, 0, flags);
}

int semget(key_t key, int nsems, int flags) {
    if (nsems < 0) return fail(EINVAL);
    return ipc_get(IPC_KIND_SEM, key, (unsigned long long)nsems, flags);
}

int shmget(key_t key, size_t size, int flags) {
    return ipc_get(IPC_KIND_SHM, key, (unsigned long long)size, flags);
}

int msgctl(int id, int cmd, struct msqid_ds *buf) {
    return ipc_ctl(IPC_KIND_MSG, id, 0, cmd, 0, buf);
}

int shmctl(int id, int cmd, struct shmid_ds *buf) {
    return ipc_ctl(IPC_KIND_SHM, id, 0, cmd, 0, buf);
}

/* The fourth argument of semctl, as the program passes it: its union semun
 * (the program's own: the C library defines none), a word that is the
 * value or an address. */
union ipc_semun {
    int val;
    struct semid_ds *buf;
    unsigned short *array;
};

int semctl(int id, int num, int cmd, ...) {
    union ipc_semun arg;
    arg.buf = 0;
    switch (cmd) {
    case IPC_STAT:
    case IPC_SET:
    case GETALL:
    case SETALL:
    case SETVAL: {
        va_list ap;
        va_start(ap, cmd);
        arg = va_arg(ap, union ipc_semun);
        va_end(ap);
        break;
    }
    }
    if (cmd == SETVAL) return ipc_ctl(IPC_KIND_SEM, id, num, cmd, arg.val, 0);
    return ipc_ctl(IPC_KIND_SEM, id, num, cmd, 0, arg.buf);
}

int semop(int id, struct sembuf *ops, size_t nops) {
    const int rc = __wasmify_host_ipc_semop(id, ops, (unsigned long long)nops);
    return rc != 0 ? fail(-rc) : 0;
}

int msgsnd(int id, const void *msg, size_t size, int flags) {
    const int rc = __wasmify_host_ipc_msgsnd(id, msg, (unsigned long long)size, flags);
    return rc != 0 ? fail(-rc) : 0;
}

ssize_t msgrcv(int id, void *msg, size_t size, long type, int flags) {
    long long received = 0;
    const int rc =
        __wasmify_host_ipc_msgrcv(id, msg, (unsigned long long)size, (long long)type, flags, &received);
    return rc != 0 ? fail(-rc) : (ssize_t)received;
}

void *shmat(int id, const void *addr, int flags) {
    (void)id;
    (void)addr;
    (void)flags;
    errno = EINVAL;
    return (void *)-1;
}

int shmdt(const void *addr) {
    (void)addr;
    return fail(EINVAL);
}

int shm_copy(int id, void *buf, size_t pos, size_t size, int write) {
    const int rc = __wasmify_host_ipc_shmcopy(id, buf, (unsigned long long)pos,
                                         (unsigned long long)size, write);
    return rc != 0 ? fail(-rc) : 0;
}

/* The key of the file's inode with the low byte of id, as the C libraries
 * of Linux and of the BSDs make it, so that a program here and a process of
 * the host's that names the same file get the same object. */
key_t ftok(const char *path, int id) {
    struct stat st;
    if (stat(path, &st) < 0) return -1;
    return (key_t)((st.st_ino & 0xffff) | ((st.st_dev & 0xff) << 16) |
                   ((unsigned)(id & 0xff) << 24));
}

} /* extern "C" */

#endif /* WASMIFY_HOST_IPC_SYSV */
