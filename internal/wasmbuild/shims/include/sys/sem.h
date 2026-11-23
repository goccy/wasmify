/* sys/sem.h — System V semaphores (see sys/ipc.h). */
#ifndef WASMIFY_SYS_SEM_H
#define WASMIFY_SYS_SEM_H

#include "ipc.h"
#include <time.h>

#ifdef __cplusplus
extern "C" {
#endif

struct semid_ds {
    struct ipc_perm sem_perm;
    time_t sem_otime;
    time_t sem_ctime;
    unsigned long sem_nsems;
    long __unused3;
    long __unused4;
};

struct sembuf {
    unsigned short sem_num;
    short sem_op;
    short sem_flg;
};

#define GETPID 11
#define GETVAL 12
#define GETALL 13
#define GETNCNT 14
#define GETZCNT 15
#define SETVAL 16
#define SETALL 17

#define SEM_UNDO 0x1000

int semget(key_t key, int nsems, int flags);
int semop(int id, struct sembuf *ops, size_t nops);
/* the fourth argument, when the command takes one, is the union semun of
 * the program's (the C library defines none) */
int semctl(int id, int num, int cmd, ...);

#ifdef __cplusplus
}
#endif

#endif
