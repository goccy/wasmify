/*
 * POSIX netdb.h compatibility header for wasip1.
 *
 * The wasip1 sysroot does not provide netdb.h at all. This header
 * supplies the type definitions, constants, and function declarations
 * needed by code that uses DNS resolution (getaddrinfo, gethostbyname,
 * etc.). No implementations are provided.
 */
#ifndef _WASMIFY_NETDB_H
#define _WASMIFY_NETDB_H

#include <sys/socket.h>
#include <netinet/in.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* --- addrinfo structure --- */

struct addrinfo {
    int              ai_flags;
    int              ai_family;
    int              ai_socktype;
    int              ai_protocol;
    socklen_t        ai_addrlen;
    struct sockaddr *ai_addr;
    char            *ai_canonname;
    struct addrinfo *ai_next;
};

/* --- hostent structure --- */

struct hostent {
    char  *h_name;
    char **h_aliases;
    int    h_addrtype;
    int    h_length;
    char **h_addr_list;
};

#define h_addr h_addr_list[0]

/* --- AI_* flags for getaddrinfo --- */

#ifndef AI_PASSIVE
#define AI_PASSIVE      0x0001
#endif
#ifndef AI_CANONNAME
#define AI_CANONNAME    0x0002
#endif
#ifndef AI_NUMERICHOST
#define AI_NUMERICHOST  0x0004
#endif
#ifndef AI_NUMERICSERV
#define AI_NUMERICSERV  0x0400
#endif
#ifndef AI_V4MAPPED
#define AI_V4MAPPED     0x0008
#endif
#ifndef AI_ALL
#define AI_ALL          0x0010
#endif
#ifndef AI_ADDRCONFIG
#define AI_ADDRCONFIG   0x0020
#endif

/* --- NI_* flags for getnameinfo --- */

#ifndef NI_MAXHOST
#define NI_MAXHOST      1025
#endif
#ifndef NI_MAXSERV
#define NI_MAXSERV      32
#endif
#ifndef NI_NUMERICHOST
#define NI_NUMERICHOST  0x0001
#endif
#ifndef NI_NUMERICSERV
#define NI_NUMERICSERV  0x0002
#endif
#ifndef NI_NOFQDN
#define NI_NOFQDN       0x0004
#endif
#ifndef NI_NAMEREQD
#define NI_NAMEREQD     0x0008
#endif
#ifndef NI_DGRAM
#define NI_DGRAM        0x0010
#endif

/* --- EAI_* error codes --- */

#ifndef EAI_AGAIN
#define EAI_AGAIN       2
#endif
#ifndef EAI_BADFLAGS
#define EAI_BADFLAGS    3
#endif
#ifndef EAI_FAIL
#define EAI_FAIL        4
#endif
#ifndef EAI_FAMILY
#define EAI_FAMILY      5
#endif
#ifndef EAI_MEMORY
#define EAI_MEMORY      6
#endif
#ifndef EAI_NONAME
#define EAI_NONAME      8
#endif
#ifndef EAI_SERVICE
#define EAI_SERVICE     9
#endif
#ifndef EAI_SOCKTYPE
#define EAI_SOCKTYPE    10
#endif
#ifndef EAI_SYSTEM
#define EAI_SYSTEM      11
#endif
#ifndef EAI_OVERFLOW
#define EAI_OVERFLOW    14
#endif

/* --- Function declarations --- */

int getaddrinfo(const char *__restrict, const char *__restrict,
                const struct addrinfo *__restrict,
                struct addrinfo **__restrict);
void freeaddrinfo(struct addrinfo *);
const char *gai_strerror(int);
int getnameinfo(const struct sockaddr *__restrict, socklen_t,
                char *__restrict, socklen_t,
                char *__restrict, socklen_t, int);
struct hostent *gethostbyname(const char *);
struct hostent *gethostbyaddr(const void *, socklen_t, int);

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_NETDB_H */
