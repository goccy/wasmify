/* host_ipc.cc — System V IPC for a libc that has none.
 *
 * The semaphore sets, message queues and shared memory segments are the
 * host's, which other processes of the system share: each call goes to the
 * host through an import, in Linux's numbers and with the structures of
 * Linux on a 64-bit architecture (<sys/ipc.h>, <sys/sem.h>, <sys/msg.h>,
 * <sys/shm.h>), and the host makes it in its own. Every import returns 0 or
 * a negative errno.
 *
 * shmat takes memory of the program's for the segment, from the program's
 * allocator, and the host attaches the segment over it; shmdt detaches it
 * and gives the memory back.
 *
 * Compiled when the host capability ipc.sysv is given (WASMIFY_HOST_IPC_SYSV).
 */
#ifdef WASMIFY_HOST_IPC_SYSV

#include <sys/types.h>
#include <sys/stat.h>
#include <unistd.h>
#include <cerrno>
#include <cstdarg>
#include <cstddef>
#include <cstdint>
#include <cstdlib>

#include <sys/ipc.h>
#include <sys/msg.h>
#include <sys/sem.h>
#include <sys/shm.h>

#include "wasmify_posix.h"

/* A wasm64 program's structures are the kernel's (struct msqid64_ds,
 * semid64_ds, shmid64_ds), which the host reads and writes. */
static_assert(sizeof(void *) != 8 || sizeof(struct msqid_ds) == 120, "struct msqid_ds is the kernel's");
static_assert(sizeof(void *) != 8 || sizeof(struct semid_ds) == 88, "struct semid_ds is the kernel's");
static_assert(sizeof(void *) != 8 || sizeof(struct shmid_ds) == 112, "struct shmid_ds is the kernel's");
static_assert(sizeof(void *) != 8 || offsetof(struct shmid_ds, shm_segsz) == 48, "struct shmid_ds is the kernel's");
static_assert(sizeof(struct sembuf) == 6, "struct sembuf is the kernel's");

namespace {

int fail(int error) {
    errno = error;
    return -1;
}

/* The kinds of object, for ipc_get and ipc_ctl. */
enum { IPC_KIND_MSG = 0, IPC_KIND_SEM = 1, IPC_KIND_SHM = 2 };

int ipc_get(int kind, key_t key, unsigned long long arg, int flags) {
    int id = -1;
    const int rc = __wasmify_host_ipc_get(kind, key, arg, flags, &id);
    return rc != 0 ? fail(-rc) : id;
}

int ipc_ctl(int kind, int id, int num, int cmd, long long arg, void *buf) {
    long long result = 0;
    const int rc = __wasmify_host_ipc_ctl(kind, id, num, cmd, arg, buf, &result);
    return rc != 0 ? fail(-rc) : (int)result;
}

/* The fourth argument of semctl, as the program passes it: its union semun
 * (the program's own: the C library defines none), a word that is the
 * value or an address. */
union ipc_semun {
    int val;
    struct semid_ds *buf;
    unsigned short *array;
};

} /* namespace */

extern "C" {

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

int semctl(int id, int num, int cmd, ...) {
    union ipc_semun arg;
    arg.buf = nullptr;
    /* the commands that take the fourth argument, as Linux has them */
    switch (cmd) {
    case IPC_STAT:
    case IPC_SET:
    case IPC_INFO:
    case GETALL:
    case SETALL:
    case SETVAL:
    case 18: /* SEM_STAT */
    case 19: /* SEM_INFO */
    case 20: /* SEM_STAT_ANY */ {
        va_list ap;
        va_start(ap, cmd);
        arg = va_arg(ap, union ipc_semun);
        va_end(ap);
        break;
    }
    }
    if (cmd == SETVAL) return ipc_ctl(IPC_KIND_SEM, id, num, cmd, arg.val, nullptr);
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

/* The segment is attached over memory shmat takes for it: whole pages of
 * this memory (sysconf(_SC_PAGESIZE)), as many as the segment's size
 * needs, at a page boundary. Every address of the program's own is in use,
 * so an address the program asks for is refused (EINVAL), as Linux refuses
 * one that is mapped already; SHM_RND, which rounds such an address, has
 * nothing to do. */
void *shmat(int id, const void *addr, int flags) {
    void *const failed = (void *)-1;
    if (addr != nullptr) {
        errno = EINVAL;
        return failed;
    }
    struct shmid_ds st;
    if (shmctl(id, IPC_STAT, &st) < 0) return failed;
    /* the size of a page of this memory, which the C library always knows */
    const size_t page_size = (size_t)sysconf(_SC_PAGESIZE);
    if (st.shm_segsz > SIZE_MAX - (page_size - 1)) {
        errno = ENOMEM;
        return failed;
    }
    const size_t size = (st.shm_segsz + page_size - 1) / page_size * page_size;
    void *at = aligned_alloc(page_size, size);
    if (at == nullptr) {
        errno = ENOMEM;
        return failed;
    }
    const int rc = __wasmify_host_ipc_shmat(id, at, (unsigned long long)size, flags & ~SHM_RND);
    if (rc != 0) {
        free(at);
        errno = -rc;
        return failed;
    }
    return at;
}

/* addr is where shmat attached the segment, over memory it took: the
 * memory is given back once the segment is detached. */
int shmdt(const void *addr) {
    const int rc = __wasmify_host_ipc_shmdt(addr);
    if (rc != 0) return fail(-rc);
    free(const_cast<void *>(addr));
    return 0;
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
