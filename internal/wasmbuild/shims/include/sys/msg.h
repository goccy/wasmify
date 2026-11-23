/* sys/msg.h — System V message queues (see sys/ipc.h). A message is a long
 * (its type) and its bytes, as on every 64-bit host. */
#ifndef WASMIFY_SYS_MSG_H
#define WASMIFY_SYS_MSG_H

#include "ipc.h"
#include <time.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef unsigned long msgqnum_t;
typedef unsigned long msglen_t;

struct msqid_ds {
    struct ipc_perm msg_perm;
    time_t msg_stime;
    time_t msg_rtime;
    time_t msg_ctime;
    unsigned long msg_cbytes;
    msgqnum_t msg_qnum;
    msglen_t msg_qbytes;
    pid_t msg_lspid;
    pid_t msg_lrpid;
    long __unused1;
    long __unused2;
};

#define MSG_NOERROR 010000

int msgget(key_t key, int flags);
int msgctl(int id, int cmd, struct msqid_ds *buf);
int msgsnd(int id, const void *msg, size_t size, int flags);
ssize_t msgrcv(int id, void *msg, size_t size, long type, int flags);

#ifdef __cplusplus
}
#endif

#endif
