/*
 * POSIX socket compatibility header for wasip1.
 *
 * wasi-sdk's wasip1 sysroot provides sys/socket.h but guards most
 * declarations behind `#if !(defined __wasip1__)`. This header uses
 * #include_next to pull in the original sysroot header (for types like
 * sockaddr, socklen_t, etc.) and then adds the missing constants and
 * function declarations.
 *
 * No implementations are provided — the linker's --allow-undefined flag
 * turns unresolved symbols into wasm imports, which are satisfied at
 * runtime by the host (e.g. goccy/wasi-go socket extensions).
 */
#include_next <sys/socket.h>

#ifndef _WASMIFY_POSIX_SOCKET_H
#define _WASMIFY_POSIX_SOCKET_H

#include <stddef.h>

/* --- Socket option constants --- */

#ifndef SO_KEEPALIVE
#define SO_KEEPALIVE    9
#endif
#ifndef SO_REUSEADDR
#define SO_REUSEADDR    2
#endif
#ifndef SO_REUSEPORT
#define SO_REUSEPORT    15
#endif
#ifndef SO_BROADCAST
#define SO_BROADCAST    6
#endif
#ifndef SO_SNDBUF
#define SO_SNDBUF       7
#endif
#ifndef SO_RCVBUF
#define SO_RCVBUF       8
#endif
#ifndef SO_SNDTIMEO
#define SO_SNDTIMEO     21
#endif
#ifndef SO_RCVTIMEO
#define SO_RCVTIMEO     20
#endif
#ifndef SO_LINGER
#define SO_LINGER       13
#endif
#ifndef SO_OOBINLINE
#define SO_OOBINLINE    10
#endif
#ifndef SO_NOSIGPIPE
#define SO_NOSIGPIPE    0x0800
#endif
#ifndef SO_ERROR
#define SO_ERROR        4
#endif

/* --- Socket option levels --- */

#ifndef SOL_SOCKET
#define SOL_SOCKET      1
#endif
#ifndef SOL_TCP
#define SOL_TCP         6
#endif
#ifndef SOL_IP
#define SOL_IP          0
#endif
#ifndef SOL_IPV6
#define SOL_IPV6        41
#endif

/* --- TCP options --- */

#ifndef TCP_NODELAY
#define TCP_NODELAY     1
#endif
#ifndef TCP_KEEPIDLE
#define TCP_KEEPIDLE    4
#endif
#ifndef TCP_KEEPINTVL
#define TCP_KEEPINTVL   5
#endif
#ifndef TCP_KEEPCNT
#define TCP_KEEPCNT     6
#endif

/* --- IP options --- */

#ifndef IP_TOS
#define IP_TOS          1
#endif
#ifndef IP_TTL
#define IP_TTL          2
#endif
#ifndef IP_MULTICAST_TTL
#define IP_MULTICAST_TTL 33
#endif
#ifndef IP_MULTICAST_LOOP
#define IP_MULTICAST_LOOP 34
#endif
#ifndef IP_ADD_MEMBERSHIP
#define IP_ADD_MEMBERSHIP 35
#endif
#ifndef IP_DROP_MEMBERSHIP
#define IP_DROP_MEMBERSHIP 36
#endif

/* --- IPV6 options --- */

#ifndef IPV6_V6ONLY
#define IPV6_V6ONLY     26
#endif

/* --- MSG flags --- */

#ifndef MSG_DONTWAIT
#define MSG_DONTWAIT    0x0040
#endif
#ifndef MSG_NOSIGNAL
#define MSG_NOSIGNAL    0x4000
#endif
#ifndef MSG_MORE
#define MSG_MORE        0x8000
#endif
#ifndef MSG_EOR
#define MSG_EOR         0x0080
#endif
#ifndef MSG_CMSG_CLOEXEC
#define MSG_CMSG_CLOEXEC 0x40000000
#endif

/* --- Address families --- */

#ifndef AF_UNSPEC
#define AF_UNSPEC       0
#endif
#ifndef AF_LOCAL
#define AF_LOCAL        1
#endif
#ifndef AF_UNIX
#define AF_UNIX         AF_LOCAL
#endif

/* --- Protocol families (aliases) --- */

#ifndef PF_UNSPEC
#define PF_UNSPEC       AF_UNSPEC
#endif
#ifndef PF_INET
#define PF_INET         AF_INET
#endif
#ifndef PF_INET6
#define PF_INET6        AF_INET6
#endif
#ifndef PF_LOCAL
#define PF_LOCAL        AF_LOCAL
#endif
#ifndef PF_UNIX
#define PF_UNIX         AF_UNIX
#endif

/* --- Socket shutdown modes --- */

#ifndef SHUT_RD
#define SHUT_RD         0
#endif
#ifndef SHUT_WR
#define SHUT_WR         1
#endif
#ifndef SHUT_RDWR
#define SHUT_RDWR       2
#endif

/* --- CMSG macros --- */

#ifndef CMSG_ALIGN
#define CMSG_ALIGN(len) (((len) + sizeof(size_t) - 1) & ~(sizeof(size_t) - 1))
#endif
#ifndef CMSG_SPACE
#define CMSG_SPACE(len) (CMSG_ALIGN(sizeof(struct cmsghdr)) + CMSG_ALIGN(len))
#endif
#ifndef CMSG_LEN
#define CMSG_LEN(len)   (CMSG_ALIGN(sizeof(struct cmsghdr)) + (len))
#endif

#ifdef __cplusplus
extern "C" {
#endif

/* --- Function declarations guarded out for wasip1 --- */

#ifndef __wasip2__
int socket(int, int, int);
int connect(int, const struct sockaddr *, socklen_t);
int bind(int, const struct sockaddr *, socklen_t);
int listen(int, int);
int accept(int, struct sockaddr *__restrict, socklen_t *__restrict);
int getsockname(int, struct sockaddr *__restrict, socklen_t *__restrict);
int getpeername(int, struct sockaddr *__restrict, socklen_t *__restrict);
ssize_t send(int, const void *, size_t, int);
ssize_t recv(int, void *, size_t, int);
ssize_t sendto(int, const void *, size_t, int, const struct sockaddr *, socklen_t);
ssize_t recvfrom(int, void *__restrict, size_t, int, struct sockaddr *__restrict, socklen_t *__restrict);
int setsockopt(int, int, int, const void *, socklen_t);
int getsockopt(int, int, int, void *__restrict, socklen_t *__restrict);
int shutdown(int, int);
int socketpair(int, int, int, int[2]);
ssize_t sendmsg(int, const struct msghdr *, int);
ssize_t recvmsg(int, struct msghdr *, int);
#endif

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_POSIX_SOCKET_H */
