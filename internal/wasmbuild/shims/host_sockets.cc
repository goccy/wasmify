/* host_sockets.cc — sockets and name resolution on the host's network.
 *
 * Compiled when the host capability network is given (WASMIFY_HOST_NETWORK).
 * The sockets (socket(), connect(), bind(), listen(), accept(), the
 * options, send and receive, socketpair(), shutdown, poll) come with
 * network.sockets, the resolver (getaddrinfo(), gethostbyname(),
 * getnameinfo() and the services, protocols and networks databases) with
 * network.resolver; inet_ntop with the capability itself.
 *
 * WASI preview1 has no way to create, connect or bind a socket, and
 * wasi-libc under __wasip1__ neither declares nor defines socket() or
 * connect(). They are defined here, on imports of wasmify_posix.h (module
 * wasip1_posix_extension) that the host implements. A socket's fd is the
 * host's; close() goes through the standard WASI fd_close.
 *
 * What is handled: stream and datagram sockets of the families AF_INET,
 * AF_INET6 and AF_UNIX (named by a path) — clients (connect; one on a
 * non-blocking socket reports EINPROGRESS and SO_ERROR tells how it ended)
 * and servers (bind, listen, accept), sendto and recvfrom, and send/recv
 * once connected; send and recv with the flags MSG_PEEK, MSG_WAITALL,
 * MSG_DONTWAIT and MSG_NOSIGNAL; their names (getsockname, getpeername), a
 * fixed set of socket options, the receive and send timeouts, socketpair()
 * for a pair of connected AF_UNIX sockets, and name resolution
 * (getaddrinfo, getnameinfo, gethostbyname, gethostbyaddr). Everything else
 * fails with the errno the missing feature has: a socket of another type
 * cannot be created (EPROTONOSUPPORT), another address family is
 * EAFNOSUPPORT, an option outside the set is ENOPROTOOPT.
 *
 * struct addrinfo, the EAI_* values and the prototypes come from wasmify's
 * <netdb.h>, deployed into the host-include directory with the network
 * capability (and the same file as the POSIX-compat overlay's), so a
 * program and this file see one layout of struct addrinfo. */
#ifdef WASMIFY_HOST_NETWORK

#include <sys/socket.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <arpa/inet.h>
#include <netdb.h>
#include <fcntl.h>
#include <poll.h>
#include <cerrno>
#include <climits>
#include <cstddef>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <unistd.h>
#include <vector>
#include <wasi/api.h>

#include "wasmify_posix.h"

namespace {

#ifdef WASMIFY_HOST_NETWORK_RESOLVER
/* A variable-length record the host hands over: it reports the record's
 * size also when the buffer is too small (-ERANGE). data has room for one
 * byte past cap, for a terminator. */
struct Record {
    char *data = nullptr;
    uint32_t cap = 0;
    uint32_t len = 0;
};

/* Runs lookup into rec, growing rec to the size the host reports. Returns
 * 0 (the record is rec.len bytes at rec.data) or the host's negative errno
 * (-ENOMEM when rec cannot grow). */
template <typename Lookup>
int fetch(Record &rec, Lookup lookup) {
    if (rec.data == nullptr) {
        rec.data = static_cast<char *>(malloc(1));
        if (rec.data == nullptr) return -ENOMEM;
        rec.cap = 0;
    }
    for (;;) {
        unsigned int needed = 0;
        const int rc = lookup(rec.data, (int)rec.cap, &needed);
        if (rc == 0) {
            rec.len = needed;
            return 0;
        }
        if (rc != -ERANGE || needed <= rec.cap) return rc;
        char *grown = static_cast<char *>(realloc(rec.data, (size_t)needed + 1));
        if (grown == nullptr) return -ENOMEM;
        rec.data = grown;
        rec.cap = needed;
    }
}

/* An entry of the host's services database into rec: by name (by_port 0)
 * or by port, for the protocol proto (any when it is empty). The record is
 * the port (u32), the name and the protocol (each a u32 length and the
 * bytes), the number of aliases (u32) and the aliases. Returns 0 or the
 * host's negative errno: -ENOENT when there is no such service. */
int lookup_service(Record &rec, const char *name, int port, int by_port, const char *proto) {
    return fetch(rec, [&](void *buf, int len, unsigned int *needed) {
        return __wasmify_host_sock_service(name, (int)strlen(name), port, by_port, proto,
                                           (int)strlen(proto), buf, len, needed);
    });
}

/* Makes *list (of *cap entries) hold at least n entries. Returns false with
 * errno ENOMEM when it cannot grow. */
bool room(char ***list, size_t *cap, size_t n) {
    if (n <= *cap) return true;
    char **grown = static_cast<char **>(realloc(*list, n * sizeof(char *)));
    if (grown == nullptr) {
        errno = ENOMEM;
        return false;
    }
    *list = grown;
    *cap = n;
    return true;
}

/* The EAI_ code of a lookup's negative errno: a name the host has no
 * address for is EAI_NONAME, one that may resolve later EAI_AGAIN, and any
 * other failure EAI_SYSTEM with errno set. */
int eai_of(int rc) {
    switch (rc) {
    case -ENOENT: return EAI_NONAME;
    case -EAGAIN: return EAI_AGAIN;
    case -ENOMEM: return EAI_MEMORY;
    }
    errno = -rc;
    return EAI_SYSTEM;
}

/* The h_errno of a failed gethostbyname or gethostbyaddr lookup's negative
 * errno: a name the host has no address for is HOST_NOT_FOUND, one that may
 * resolve later TRY_AGAIN, and any other failure NO_RECOVERY. */
int herrno_of(int rc) {
    switch (rc) {
    case -ENOENT: return HOST_NOT_FOUND;
    case -EAGAIN: return TRY_AGAIN;
    }
    return NO_RECOVERY;
}
#endif /* WASMIFY_HOST_NETWORK_RESOLVER */

} /* namespace */

extern "C" {

/* The socket types, by the host's own numbering (libc's SOCK_* numbers
 * differ from libc to libc). */
#define WASMIFY_SOCK_TYPE_STREAM 1
#define WASMIFY_SOCK_TYPE_DGRAM 2

/* The address families, by the host's own numbering (libc's AF_* numbers
 * differ from libc to libc). */
#define WASMIFY_SOCK_FAMILY_IPV4 1
#define WASMIFY_SOCK_FAMILY_UNIX 2
#define WASMIFY_SOCK_FAMILY_IPV6 3

/* A socket address as it crosses to the host and back, whatever its
 * family: the port in host byte order, the address as it sits in the
 * sockaddr (4 bytes for IPv4, 16 for IPv6), the scope id of an IPv6
 * address, and the name of a Unix-domain socket as the program gave it.
 * family 0 is no address (AF_UNSPEC). */
struct __wasmify_sock_addr {
    uint32_t family;
    uint32_t port;
    uint8_t ip[16];
    uint32_t scope;
    uint32_t name_len;
    char name[108];
};
static_assert(sizeof(struct __wasmify_sock_addr) == 140, "the host's address record is 140 bytes");

/* A Unix-domain address as a program hands it over. wasi-libc's own
 * struct sockaddr_un has the family and no room for a name, so a project
 * that uses such sockets brings the usual layout, which is this one. */
struct __wasmify_sockaddr_un {
    sa_family_t sun_family;
    char sun_path[108];
};

/* Where an import takes an address that may be a Unix-domain one, it also
 * takes the socket's path the way the filesystem imports take paths:
 * absolute, without the leading '/'. The host does not know the program's
 * working directory. */

/* The flags of a send or a receive the host knows, by the host's own
 * numbering (libc's MSG_ numbers differ from libc to libc): a receive takes
 * PEEK, WAITALL and DONTWAIT, a send DONTWAIT and NOSIGNAL. */
#define WASMIFY_MSG_PEEK 1     /* receive: leave it in place */
#define WASMIFY_MSG_WAITALL 2  /* receive: until the buffer is full */
#define WASMIFY_MSG_DONTWAIT 4 /* do not wait, whatever O_NONBLOCK says */
#define WASMIFY_MSG_NOSIGNAL 8 /* send: a broken connection is EPIPE without SIGPIPE */

/* The socket options the host knows, by the host's own numbering: libc's
 * numbers for them differ from libc to libc. */
#define WASMIFY_SOCKOPT_REUSEADDR 1
#define WASMIFY_SOCKOPT_REUSEPORT 2
#define WASMIFY_SOCKOPT_KEEPALIVE 3
#define WASMIFY_SOCKOPT_NODELAY 4
#define WASMIFY_SOCKOPT_RCVBUF 5
#define WASMIFY_SOCKOPT_SNDBUF 6
#define WASMIFY_SOCKOPT_LINGER 7 /* seconds, or -1 for off */
#define WASMIFY_SOCKOPT_ERROR 8  /* read only */
#define WASMIFY_SOCKOPT_TYPE 9   /* read only: a WASMIFY_SOCK_TYPE_* */
#define WASMIFY_SOCKOPT_ACCEPTCONN 10 /* read only: 1 for a listening socket */
#define WASMIFY_SOCKOPT_V6ONLY 11 /* IPV6_V6ONLY, before the socket is bound */
#define WASMIFY_SOCKOPT_BROADCAST 12 /* SO_BROADCAST */
#define WASMIFY_SOCKOPT_DEFER_ACCEPT 13 /* TCP_DEFER_ACCEPT: seconds */

/* SOCK_RAW has no number in every libc (wasi-libc's headers leave it
 * out); getaddrinfo answers for it with Linux's. */
#ifndef SOCK_RAW
#define SOCK_RAW 3
#endif

/* SO_ACCEPTCONN has no number in every libc; a project that gives it one
 * uses this one (Linux's). */
#ifndef SO_ACCEPTCONN
#define SO_ACCEPTCONN 30
#endif

/* The receive and send timeouts of a socket (SO_RCVTIMEO, SO_SNDTIMEO):
 * *old_nanos receives the timeout before the call, in nanoseconds (0: no
 * limit); with set it is then replaced by nanos. */
#define WASMIFY_SOCK_TIMEOUT_RECEIVE 1
#define WASMIFY_SOCK_TIMEOUT_SEND 2

#if defined(WASMIFY_HOST_NETWORK_SOCKETS) || defined(WASMIFY_HOST_NETWORK_RESOLVER)
/* The host's family for a libc address family, or 0 for one the host has
 * no sockets of. */
static int __wasmify_sock_family(int domain) {
    switch (domain) {
    case AF_INET:  return WASMIFY_SOCK_FAMILY_IPV4;
    case AF_INET6: return WASMIFY_SOCK_FAMILY_IPV6;
    case AF_UNIX:  return WASMIFY_SOCK_FAMILY_UNIX;
    }
    return 0;
}

/* Reads a sockaddr into the host's form; AF_UNSPEC is the record of no
 * address. For a Unix-domain address, path (of path_size bytes) receives
 * the socket's path as the host takes paths — made absolute against the
 * working directory, without the leading '/' — and *path_len its length;
 * otherwise *path_len is 0. Returns 0 or the errno: EAFNOSUPPORT for a
 * family the host has no sockets of, EINVAL for an address shorter than
 * its family needs or a Unix-domain one without a name, ENAMETOOLONG for a
 * path that does not fit, and getcwd's. */
static int __wasmify_sock_addr_in(const struct sockaddr *addr, socklen_t addrlen,
                                  struct __wasmify_sock_addr *out, char *path,
                                  size_t path_size, int *path_len) {
    memset(out, 0, sizeof(*out));
    if (path_len != nullptr) *path_len = 0;
    if (addr == nullptr || addrlen < (socklen_t)sizeof(sa_family_t)) return EINVAL;
    switch (addr->sa_family) {
    case AF_UNSPEC:
        return 0;
    case AF_INET: {
        if (addrlen < (socklen_t)sizeof(struct sockaddr_in)) return EINVAL;
        const struct sockaddr_in *in = reinterpret_cast<const struct sockaddr_in *>(addr);
        out->family = WASMIFY_SOCK_FAMILY_IPV4;
        out->port = ntohs(in->sin_port);
        memcpy(out->ip, &in->sin_addr, 4);
        return 0;
    }
    case AF_INET6: {
        if (addrlen < (socklen_t)sizeof(struct sockaddr_in6)) return EINVAL;
        const struct sockaddr_in6 *in6 = reinterpret_cast<const struct sockaddr_in6 *>(addr);
        out->family = WASMIFY_SOCK_FAMILY_IPV6;
        out->port = ntohs(in6->sin6_port);
        memcpy(out->ip, &in6->sin6_addr, 16);
        out->scope = in6->sin6_scope_id;
        return 0;
    }
    case AF_UNIX: {
        const struct __wasmify_sockaddr_un *un = reinterpret_cast<const struct __wasmify_sockaddr_un *>(addr);
        const size_t head = offsetof(struct __wasmify_sockaddr_un, sun_path);
        size_t room = addrlen > head ? (size_t)addrlen - head : 0;
        if (room > sizeof(un->sun_path)) room = sizeof(un->sun_path);
        size_t len;
        if (room > 0 && un->sun_path[0] == '\0') {
            /* An abstract name is every byte the address length covers,
             * NULs included: a name of NULs alone is a name. */
            len = room;
        } else {
            len = strnlen(un->sun_path, room);
            if (len == 0) return EINVAL;
        }
        out->family = WASMIFY_SOCK_FAMILY_UNIX;
        out->name_len = (uint32_t)len;
        memcpy(out->name, un->sun_path, len);
        if (un->sun_path[0] != '\0' && path != nullptr) {
            char name[sizeof(un->sun_path) + 1];
            memcpy(name, un->sun_path, len);
            name[len] = '\0';
            int n;
            if (name[0] == '/') {
                n = snprintf(path, path_size, "%s", name);
            } else {
                char *cwd = getcwd(nullptr, 0);
                if (cwd == nullptr) return errno;
                n = snprintf(path, path_size, "%s/%s", cwd, name);
                free(cwd);
            }
            if (n < 0 || (size_t)n >= path_size) return ENAMETOOLONG;
            size_t lead = 0;
            while (path[lead] == '/') lead++;
            memmove(path, path + lead, (size_t)n - lead + 1);
            *path_len = n - (int)lead;
        }
        return 0;
    }
    }
    return EAFNOSUPPORT;
}

/* Renders an address from the host as a sockaddr, the way getsockname()
 * and friends return one: as much of it as fits is copied, and *addrlen is
 * the full size — 0 when there is no address. */
static void __wasmify_sock_addr_out(const struct __wasmify_sock_addr *in,
                                    struct sockaddr *addr, socklen_t *addrlen) {
    union {
        struct sockaddr_in in4;
        struct sockaddr_in6 in6;
        struct __wasmify_sockaddr_un un;
    } sa;
    memset(&sa, 0, sizeof(sa));
    socklen_t full = 0;
    switch (in->family) {
    case WASMIFY_SOCK_FAMILY_IPV4:
        sa.in4.sin_family = AF_INET;
        sa.in4.sin_port = htons(static_cast<unsigned short>(in->port));
        memcpy(&sa.in4.sin_addr, in->ip, 4);
        full = sizeof(sa.in4);
        break;
    case WASMIFY_SOCK_FAMILY_IPV6:
        sa.in6.sin6_family = AF_INET6;
        sa.in6.sin6_port = htons(static_cast<unsigned short>(in->port));
        memcpy(&sa.in6.sin6_addr, in->ip, 16);
        sa.in6.sin6_scope_id = in->scope;
        full = sizeof(sa.in6);
        break;
    case WASMIFY_SOCK_FAMILY_UNIX: {
        sa.un.sun_family = AF_UNIX;
        size_t len = in->name_len;
        if (len > sizeof(sa.un.sun_path)) len = sizeof(sa.un.sun_path);
        memcpy(sa.un.sun_path, in->name, len);
        /* The lengths are Linux's, which is what tells the three kinds of
         * address apart: a path ends with its terminating NUL (with none
         * when it fills sun_path); an abstract name (one that starts with
         * a NUL) has no terminator and ends where the address does; a
         * socket without a name has an address that ends before the path. */
        full = (socklen_t)(offsetof(struct __wasmify_sockaddr_un, sun_path) + len);
        if (len != 0 && in->name[0] != '\0' && len < sizeof(sa.un.sun_path)) full++;
        break;
    }
    }
    socklen_t n = *addrlen;
    if (n > full) n = full;
    memcpy(addr, &sa, n);
    *addrlen = full;
}

#endif

#ifdef WASMIFY_HOST_NETWORK_RESOLVER
/* The address record of an IPv4 or IPv6 address as it sits in an in_addr
 * or in6_addr. */
static void __wasmify_sock_addr_ip(struct __wasmify_sock_addr *out, int family,
                                   const void *ip) {
    memset(out, 0, sizeof(*out));
    out->family = (uint32_t)family;
    memcpy(out->ip, ip, family == WASMIFY_SOCK_FAMILY_IPV4 ? 4 : 16);
}

/* The port of a service given as a number or as a name of the host's
 * services database, for the socket type (0: any). Returns 0, or the EAI_
 * code. */
static int __wasmify_service_port(const char *service, int socktype, int flags, int *port) {
    *port = 0;
    if (service == nullptr || service[0] == '\0') return 0;
    char *end = nullptr;
    const long number = strtol(service, &end, 10);
    if (end != service && *end == '\0') {
        if (number < 0 || number > 65535) return EAI_SERVICE;
        *port = (int)number;
        return 0;
    }
    if (flags & AI_NUMERICSERV) return EAI_NONAME;
    const char *proto = socktype == SOCK_DGRAM ? "udp" : (socktype == SOCK_STREAM ? "tcp" : "");
    Record rec;
    const int rc = lookup_service(rec, service, 0, 0, proto);
    int bad = 0;
    if (rc == -ENOENT) {
        bad = EAI_SERVICE;
    } else if (rc != 0) {
        bad = eai_of(rc);
    } else if (rec.len < 4) {
        /* shorter than the port it starts with */
        bad = EAI_FAIL;
    } else {
        uint32_t found = 0;
        memcpy(&found, rec.data, 4);
        *port = (int)found;
    }
    free(rec.data);
    return bad;
}

/* An IPv4 address in the numbers-and-dots notation inet_aton(3) takes,
 * which wasi-libc does not have: one to four numbers, each decimal, octal
 * (a leading 0) or hexadecimal (a leading 0x), the last filling the bytes
 * the others leave (a.b.c.d, a.b.c with c 16 bits, a.b with b 24 bits, a
 * alone 32 bits). glibc's getaddrinfo takes a node in that notation as an
 * address, without asking a resolver: "127.1", "2130706433",
 * "0x7f000001" and "127.0.0.01" are all 127.0.0.1. Returns 1 and the
 * address in network byte order, or 0. */
static int __wasmify_inet_aton(const char *cp, struct in_addr *addr) {
    unsigned long long parts[4] = {0, 0, 0, 0};
    int n = 0;
    for (;;) {
        if (n == 4 || *cp < '0' || *cp > '9') return 0;
        char *end = nullptr;
        /* a number too large for strtoull is ULLONG_MAX, too large here */
        parts[n] = strtoull(cp, &end, 0);
        if (parts[n] > 0xffffffffu) return 0;
        n++;
        if (*end == '\0') break;
        if (*end != '.') return 0;
        cp = end + 1;
    }
    /* the last number fills the bytes the ones before it leave */
    const unsigned long long last_max = 0xffffffffull >> (8 * (n - 1));
    uint32_t ip = 0;
    for (int i = 0; i < n - 1; i++) {
        if (parts[i] > 0xff) return 0;
        ip |= (uint32_t)parts[i] << (24 - 8 * i);
    }
    if (parts[n - 1] > last_max) return 0;
    ip |= (uint32_t)parts[n - 1];
    addr->s_addr = htonl(ip);
    return 1;
}

/* getaddrinfo(): the addresses of node — an IPv4 address in the notation
 * inet_aton takes, or every one the host's resolver gives, IPv4 before
 * IPv6 — or, without a node ("*" is none, as in glibc), the address to bind
 * to for a passive socket (AI_PASSIVE) and the loopback address otherwise;
 * each with the port of service and once per socket type asked for: a
 * stream and a datagram socket when none is asked, or the one asked for
 * (SOCK_RAW with the protocol asked for). */
int getaddrinfo(const char *node, const char *service,
                const struct addrinfo *hints, struct addrinfo **res) {
    const int flags = hints != nullptr ? hints->ai_flags : 0;
    const int family = hints != nullptr ? hints->ai_family : AF_UNSPEC;
    int socktype = hints != nullptr ? hints->ai_socktype : 0;
    const int protocol = hints != nullptr ? hints->ai_protocol : 0;
    if (node != nullptr && node[0] == '*' && node[1] == '\0') node = nullptr;
    if (node == nullptr && (service == nullptr || service[0] == '\0')) return EAI_NONAME;
    if (family != AF_UNSPEC && family != AF_INET && family != AF_INET6) return EAI_FAMILY;
    if (socktype != 0 && socktype != SOCK_STREAM && socktype != SOCK_DGRAM && socktype != SOCK_RAW)
        return EAI_SOCKTYPE;
    if (socktype == 0 && protocol == IPPROTO_TCP) socktype = SOCK_STREAM;
    if (socktype == 0 && protocol == IPPROTO_UDP) socktype = SOCK_DGRAM;
    int port = 0;
    if (const int bad = __wasmify_service_port(service, socktype, flags, &port)) return bad;

    const int want = family == AF_UNSPEC ? 0 : __wasmify_sock_family(family);
    /* Without a node and for one in numbers there are at most two
     * addresses; the resolver's go into a list that grows until the host
     * gives fewer than it has room for. */
    struct __wasmify_sock_addr fixed[2];
    struct __wasmify_sock_addr *resolved = nullptr;
    struct __wasmify_sock_addr *found = fixed;
    unsigned int count = 0;
    if (node == nullptr) {
        if (want != WASMIFY_SOCK_FAMILY_IPV6) {
            struct in_addr a;
            a.s_addr = htonl((flags & AI_PASSIVE) ? INADDR_ANY : INADDR_LOOPBACK);
            __wasmify_sock_addr_ip(&found[count++], WASMIFY_SOCK_FAMILY_IPV4, &a);
        }
        if (want != WASMIFY_SOCK_FAMILY_IPV4) {
            unsigned char a[16] = {0}; /* :: */
            if (!(flags & AI_PASSIVE)) a[15] = 1; /* ::1 */
            __wasmify_sock_addr_ip(&found[count++], WASMIFY_SOCK_FAMILY_IPV6, a);
        }
    } else if (struct in_addr a; want != WASMIFY_SOCK_FAMILY_IPV6 && __wasmify_inet_aton(node, &a)) {
        __wasmify_sock_addr_ip(&found[count++], WASMIFY_SOCK_FAMILY_IPV4, &a);
    } else {
        if (flags & AI_NUMERICHOST) {
            /* an address in numbers, with or without a scope: no lookup */
            char literal[INET6_ADDRSTRLEN];
            const char *zone = strchr(node, '%');
            const size_t len = zone != nullptr ? (size_t)(zone - node) : strlen(node);
            unsigned char parsed[16];
            if (len >= sizeof(literal)) return EAI_NONAME;
            memcpy(literal, node, len);
            literal[len] = '\0';
            if (inet_pton(AF_INET, literal, parsed) != 1 && inet_pton(AF_INET6, literal, parsed) != 1)
                return EAI_NONAME;
        }
        for (unsigned int room = 16;; room *= 2) {
            void *grown = realloc(resolved, room * sizeof(*resolved));
            if (grown == nullptr) {
                free(resolved);
                return EAI_MEMORY;
            }
            resolved = static_cast<struct __wasmify_sock_addr *>(grown);
            const int rc = __wasmify_host_sock_resolve(node, (int)strlen(node), want, resolved,
                                                       (int)room, &count);
            if (rc != 0) {
                free(resolved);
                return eai_of(rc);
            }
            if (count < room) break;
        }
        found = resolved;
    }

    static const int types[3] = {SOCK_STREAM, SOCK_DGRAM, SOCK_RAW};
    struct addrinfo *head = nullptr;
    struct addrinfo **tail = &head;
    for (unsigned int i = 0; i < count; i++) {
        found[i].port = (uint32_t)port;
        for (int t = 0; t < 3; t++) {
            if (socktype == 0 ? types[t] == SOCK_RAW : socktype != types[t]) continue;
            struct addrinfo *ai =
                static_cast<struct addrinfo *>(calloc(1, sizeof(struct addrinfo)));
            struct sockaddr *sa = static_cast<struct sockaddr *>(
                calloc(1, sizeof(struct sockaddr_in6)));
            if (ai == nullptr || sa == nullptr) {
                free(ai);
                free(sa);
                freeaddrinfo(head);
                free(resolved);
                return EAI_MEMORY;
            }
            socklen_t len = sizeof(struct sockaddr_in6);
            __wasmify_sock_addr_out(&found[i], sa, &len);
            ai->ai_family = found[i].family == WASMIFY_SOCK_FAMILY_IPV6 ? AF_INET6 : AF_INET;
            ai->ai_socktype = types[t];
            ai->ai_protocol = protocol != 0 || types[t] == SOCK_RAW ? protocol
                              : (types[t] == SOCK_STREAM ? IPPROTO_TCP : IPPROTO_UDP);
            ai->ai_addrlen = len;
            ai->ai_addr = sa;
            *tail = ai;
            tail = &ai->ai_next;
        }
    }
    free(resolved);
    if ((flags & AI_CANONNAME) && node != nullptr && head != nullptr) {
        head->ai_canonname = strdup(node);
        if (head->ai_canonname == nullptr) {
            freeaddrinfo(head);
            return EAI_MEMORY;
        }
    }
    *res = head;
    return 0;
}

void freeaddrinfo(struct addrinfo *ai) {
    while (ai != nullptr) {
        struct addrinfo *next = ai->ai_next;
        free(ai->ai_addr);
        free(ai->ai_canonname);
        free(ai);
        ai = next;
    }
}

const char *gai_strerror(int ecode) {
    switch (ecode) {
    case 0:            return "Success";
    case EAI_AGAIN:    return "Temporary failure in name resolution";
    case EAI_BADFLAGS: return "Bad value for ai_flags";
    case EAI_FAIL:     return "Non-recoverable failure in name resolution";
    case EAI_FAMILY:   return "ai_family not supported";
    case EAI_MEMORY:   return "Memory allocation failure";
    case EAI_NONAME:   return "Name or service not known";
    case EAI_SERVICE:  return "Servname not supported for ai_socktype";
    case EAI_SOCKTYPE: return "ai_socktype not supported";
    case EAI_SYSTEM:   return "System error";
    case EAI_OVERFLOW: return "Argument buffer overflow";
    }
    return "Unknown error";
}

/* The name of the host with an IP address, into buf as a C string.
 * Returns 0, or the host's negative errno (-ERANGE for a name that does not
 * fit). */
static int __wasmify_reverse(const struct __wasmify_sock_addr *addr, char *buf, size_t size) {
    unsigned int needed = 0;
    if (size == 0) return -ERANGE;
    int rc = __wasmify_host_sock_reverse(addr, buf, (int)size - 1, &needed);
    if (rc != 0) return rc;
    buf[needed] = '\0';
    return 0;
}

/* getnameinfo(): the host's name and the service's, from the host's
 * resolver and services database. An address or a port that has no name —
 * an address the host has no name for or may not look up (EACCES: the
 * instance's policy) — is given in numbers, unless the caller requires a
 * name (NI_NAMEREQD); so are both when the caller asks for numbers
 * (NI_NUMERICHOST, NI_NUMERICSERV). */
int getnameinfo(const struct sockaddr *sa, socklen_t salen, char *host,
                socklen_t hostlen, char *serv, socklen_t servlen, int flags) {
    struct __wasmify_sock_addr addr;
    if (sa == nullptr || (sa->sa_family != AF_INET && sa->sa_family != AF_INET6) ||
        __wasmify_sock_addr_in(sa, salen, &addr, nullptr, 0, nullptr) != 0)
        return EAI_FAMILY;
    if (host != nullptr && hostlen > 0) {
        int named = -ENOENT;
        if (!(flags & NI_NUMERICHOST)) {
            named = __wasmify_reverse(&addr, host, hostlen);
            if (named == -ERANGE) return EAI_OVERFLOW;
            if (named != 0 && named != -ENOENT && named != -EACCES) return eai_of(named);
        }
        if (named != 0) {
            if (flags & NI_NAMEREQD) return EAI_NONAME;
            char text[INET6_ADDRSTRLEN];
            if (inet_ntop(sa->sa_family, addr.ip, text, sizeof(text)) == nullptr) return EAI_FAIL;
            int n = addr.scope != 0 ? snprintf(host, hostlen, "%s%%%u", text, addr.scope)
                                    : snprintf(host, hostlen, "%s", text);
            if (n < 0 || (socklen_t)n >= hostlen) return EAI_OVERFLOW;
        }
    }
    if (serv != nullptr && servlen > 0) {
        const unsigned int port = addr.port;
        bool named = false;
        if (!(flags & NI_NUMERICSERV)) {
            /* the record starts with the port and the name */
            Record rec;
            const int rc = lookup_service(rec, "", (int)port, 1, (flags & NI_DGRAM) ? "udp" : "tcp");
            int bad = 0;
            uint32_t len = 0;
            if (rc == 0 && rec.len >= 8) {
                memcpy(&len, rec.data + 4, 4);
                if (len > rec.len - 8) {
                    bad = EAI_FAIL;
                } else if (len >= servlen) {
                    bad = EAI_OVERFLOW;
                } else {
                    memcpy(serv, rec.data + 8, len);
                    serv[len] = '\0';
                    named = true;
                }
            } else if (rc == 0) {
                bad = EAI_FAIL;
            } else if (rc != -ENOENT && rc != -EACCES) {
                bad = eai_of(rc);
            }
            free(rec.data);
            if (bad != 0) return bad;
        }
        if (!named) {
            int n = snprintf(serv, servlen, "%u", port);
            if (n < 0 || (socklen_t)n >= servlen) return EAI_OVERFLOW;
        }
    }
    return 0;
}

/* h_errno: why the last gethostbyname or gethostbyaddr of the thread found
 * nothing. */
int *__h_errno_location(void) {
    static thread_local int h;
    return &h;
}

/* gethostbyname(): the host's gethostbyname lookup: the official name and
 * the IPv4 addresses of the name. The result lives in static storage, as
 * the interface has it. */
struct hostent *gethostbyname(const char *name) {
    static struct hostent ent;
    static Record rec;
    static char **addr_list = nullptr;
    static size_t addr_cap = 0;
    static char *no_aliases[1];
    if (name == nullptr) {
        h_errno = HOST_NOT_FOUND;
        return nullptr;
    }
    /* the record: count, the addresses, the name */
    const int rc = fetch(rec, [name](void *buf, int len, unsigned int *needed) {
        return __wasmify_host_sock_host_by_name(name, (int)strlen(name), buf, len, needed);
    });
    if (rc != 0) {
        if (rc != -ENOENT) errno = -rc;
        h_errno = herrno_of(rc);
        return nullptr;
    }
    uint32_t count = 0;
    if (rec.len < 4) {
        h_errno = NO_RECOVERY;
        return nullptr;
    }
    memcpy(&count, rec.data, 4);
    if (count == 0) {
        h_errno = NO_DATA;
        return nullptr;
    }
    if ((rec.len - 4) / 4 < count) {
        h_errno = NO_RECOVERY;
        return nullptr;
    }
    if (!room(&addr_list, &addr_cap, (size_t)count + 1)) {
        h_errno = NO_RECOVERY;
        return nullptr;
    }
    for (uint32_t i = 0; i < count; i++) addr_list[i] = rec.data + 4 + 4 * i;
    addr_list[count] = nullptr;
    rec.data[rec.len] = '\0';
    no_aliases[0] = nullptr;
    ent.h_name = rec.data + 4 + 4 * count;
    ent.h_aliases = no_aliases;
    ent.h_addrtype = AF_INET;
    ent.h_length = sizeof(struct in_addr);
    ent.h_addr_list = addr_list;
    return &ent;
}

/* gethostbyaddr(): the host's reverse lookup of an IPv4 or IPv6 address.
 * The result lives in static storage, as the interface has it. */
struct hostent *gethostbyaddr(const void *addr, socklen_t len, int type) {
    static struct hostent ent;
    static unsigned char kept[16];
    static char *addr_list[2];
    static char *no_aliases[1];
    static Record name;
    const int family = (type == AF_INET || type == AF_INET6) ? __wasmify_sock_family(type) : 0;
    const socklen_t size = type == AF_INET ? 4 : 16;
    if (addr == nullptr || family == 0 || len < size) {
        h_errno = HOST_NOT_FOUND;
        return nullptr;
    }
    struct __wasmify_sock_addr rec;
    __wasmify_sock_addr_ip(&rec, family, addr);
    const int rc = fetch(name, [&rec](char *buf, int buf_len, unsigned int *needed) {
        return __wasmify_host_sock_reverse(&rec, buf, buf_len, needed);
    });
    if (rc != 0) {
        if (rc != -ENOENT) errno = -rc;
        h_errno = herrno_of(rc);
        return nullptr;
    }
    name.data[name.len] = '\0';
    memcpy(kept, addr, size);
    addr_list[0] = reinterpret_cast<char *>(kept);
    addr_list[1] = nullptr;
    no_aliases[0] = nullptr;
    ent.h_name = name.data;
    ent.h_aliases = no_aliases;
    ent.h_addrtype = type;
    ent.h_length = (int)size;
    ent.h_addr_list = addr_list;
    return &ent;
}

#endif /* WASMIFY_HOST_NETWORK_RESOLVER */

#ifdef WASMIFY_HOST_NETWORK_SOCKETS
/* The host's type for a libc socket type and protocol of a family, or 0
 * for a combination the host has no sockets of. */
static int __wasmify_sock_type(int family, int type, int protocol) {
    const bool ip = family != WASMIFY_SOCK_FAMILY_UNIX;
    /* the protocol of a Unix-domain socket is 0 or the family's own number
     * (PF_UNIX), as Linux takes it */
    if (!ip && protocol == AF_UNIX) protocol = 0;
    if (type == SOCK_STREAM && (protocol == 0 || (ip && protocol == IPPROTO_TCP)))
        return WASMIFY_SOCK_TYPE_STREAM;
    if (type == SOCK_DGRAM && (protocol == 0 || (ip && protocol == IPPROTO_UDP)))
        return WASMIFY_SOCK_TYPE_DGRAM;
    return 0;
}

/* Gives the count (1 or 2) new descriptors what flags asks: SOCK_NONBLOCK
 * makes them non-blocking, SOCK_CLOEXEC marks them close-on-exec with the
 * host. On failure they are closed and -1 is returned with errno set. */
static int __wasmify_sock_flags(int *fds, int count, int flags) {
    for (int i = 0; i < count; i++) {
        int e = 0;
        if ((flags & SOCK_NONBLOCK) && fcntl(fds[i], F_SETFL, O_NONBLOCK) != 0) e = errno;
        int old = 0;
        if (e == 0 && (flags & SOCK_CLOEXEC)) {
            const int rc = __wasmify_host_fd_cloexec(fds[i], 1, 1, &old);
            if (rc != 0) e = (rc < 0) ? -rc : rc;
        }
        if (e != 0) {
            /* The sockets are the caller's to discard, unseen by the
             * program: the call fails with the flag's errno whatever their
             * close reports. */
            for (int j = 0; j < count; j++) close(fds[j]);
            errno = e;
            return -1;
        }
    }
    return 0;
}

/* socket(): a socket of the host's: a stream or a datagram socket of the
 * family. The host returns the fd, or a negative errno. */
int socket(int domain, int type, int protocol) {
    const int family = __wasmify_sock_family(domain);
    if (family == 0) { errno = EAFNOSUPPORT; return -1; }
    const int flags = type & (SOCK_NONBLOCK | SOCK_CLOEXEC);
    const int host_type = __wasmify_sock_type(family, type & ~flags, protocol);
    if (host_type == 0) { errno = EPROTONOSUPPORT; return -1; }
    int r = __wasmify_host_sock_open(family, host_type);
    if (r < 0) { errno = -r; return -1; }
    if (__wasmify_sock_flags(&r, 1, flags) != 0) return -1;
    return r;
}

/* connect(): hand the address to the host, which dials (AF_UNSPEC
 * dissolves a datagram socket's association). A connect that has to wait
 * does so on a blocking socket — a signal ends the wait (EINTR), the send
 * timeout too (EINPROGRESS) — and reports EINPROGRESS at once on a
 * non-blocking one; the connection is then made in the background, the
 * socket polls writable when that has ended, and SO_ERROR tells how. */
int connect(int fd, const struct sockaddr *addr, socklen_t addrlen) {
    struct __wasmify_sock_addr to;
    char path[PATH_MAX];
    int path_len = 0;
    if (const int e = __wasmify_sock_addr_in(addr, addrlen, &to, path, sizeof(path), &path_len)) {
        errno = e;
        return -1;
    }
    int r = __wasmify_host_sock_connect(fd, &to, path, path_len);
    if (r != 0) { errno = (r < 0) ? -r : r; return -1; }
    return 0;
}

/* bind(): give the socket a local address; port 0 lets the host pick. A
 * Unix-domain socket's file is created, and stays when the socket is
 * closed. */
int bind(int fd, const struct sockaddr *addr, socklen_t addrlen) {
    struct __wasmify_sock_addr at;
    char path[PATH_MAX];
    int path_len = 0;
    if (const int e = __wasmify_sock_addr_in(addr, addrlen, &at, path, sizeof(path), &path_len)) {
        errno = e;
        return -1;
    }
    int r = __wasmify_host_sock_bind(fd, &at, path, path_len);
    if (r != 0) { errno = (r < 0) ? -r : r; return -1; }
    return 0;
}

int listen(int fd, int backlog) {
    int r = __wasmify_host_sock_listen(fd, backlog);
    if (r != 0) { errno = (r < 0) ? -r : r; return -1; }
    return 0;
}

/* getsockname(): the socket's local name, from the host. */
int getsockname(int fd, struct sockaddr *addr, socklen_t *addrlen) {
    if (addr == nullptr || addrlen == nullptr) { errno = EFAULT; return -1; }
    struct __wasmify_sock_addr name;
    int r = __wasmify_host_sock_name(fd, 0, &name);
    if (r != 0) { errno = (r < 0) ? -r : r; return -1; }
    __wasmify_sock_addr_out(&name, addr, addrlen);
    return 0;
}

/* getpeername(): the name of the socket's peer, from the host. */
int getpeername(int fd, struct sockaddr *addr, socklen_t *addrlen) {
    if (addr == nullptr || addrlen == nullptr) { errno = EFAULT; return -1; }
    struct __wasmify_sock_addr name;
    int r = __wasmify_host_sock_name(fd, 1, &name);
    if (r != 0) { errno = (r < 0) ? -r : r; return -1; }
    __wasmify_sock_addr_out(&name, addr, addrlen);
    return 0;
}

/* accept4() / accept(): take a connection off a listening socket (the
 * standard wasi sock_accept) and report who connected. wasi-libc's own
 * accept cannot do the second part: it knows no peer and hands back an
 * address without a family. */
int accept4(int fd, struct sockaddr *addr, socklen_t *addrlen, int flags) {
    if (flags & ~(SOCK_NONBLOCK | SOCK_CLOEXEC)) { errno = EINVAL; return -1; }
    __wasi_fd_t conn = 0;
    __wasi_errno_t e = __wasi_sock_accept(
        fd, (flags & SOCK_NONBLOCK) ? __WASI_FDFLAGS_NONBLOCK : 0, &conn);
    if (e != 0) { errno = e; return -1; }
    int fd_out = static_cast<int>(conn);
    if (__wasmify_sock_flags(&fd_out, 1, flags & SOCK_CLOEXEC) != 0) return -1;
    if (addr != nullptr && addrlen != nullptr) {
        struct __wasmify_sock_addr name;
        int r = __wasmify_host_sock_name(static_cast<int>(conn), 1, &name);
        if (r != 0) {
            /* The connection is taken and is the caller's: a peer the host
             * cannot name (one that has gone already) is an address of no
             * family. */
            *addrlen = 0;
        } else {
            __wasmify_sock_addr_out(&name, addr, addrlen);
        }
    }
    return static_cast<int>(conn);
}

int accept(int fd, struct sockaddr *addr, socklen_t *addrlen) {
    return accept4(fd, addr, addrlen, 0);
}

/* Maps a (level, option) of setsockopt/getsockopt to the host's option, or
 * 0 for one the host does not know. */
static int __wasmify_sock_option(int level, int optname) {
    if (level == SOL_SOCKET) {
        switch (optname) {
        case SO_REUSEADDR: return WASMIFY_SOCKOPT_REUSEADDR;
        case SO_REUSEPORT: return WASMIFY_SOCKOPT_REUSEPORT;
        case SO_KEEPALIVE: return WASMIFY_SOCKOPT_KEEPALIVE;
        case SO_RCVBUF:    return WASMIFY_SOCKOPT_RCVBUF;
        case SO_SNDBUF:    return WASMIFY_SOCKOPT_SNDBUF;
        case SO_LINGER:    return WASMIFY_SOCKOPT_LINGER;
        case SO_ERROR:     return WASMIFY_SOCKOPT_ERROR;
        case SO_TYPE:      return WASMIFY_SOCKOPT_TYPE;
        case SO_ACCEPTCONN: return WASMIFY_SOCKOPT_ACCEPTCONN;
        case SO_BROADCAST: return WASMIFY_SOCKOPT_BROADCAST;
        }
    } else if (level == IPPROTO_TCP) {
        if (optname == TCP_NODELAY) return WASMIFY_SOCKOPT_NODELAY;
#ifdef TCP_DEFER_ACCEPT
        if (optname == TCP_DEFER_ACCEPT) return WASMIFY_SOCKOPT_DEFER_ACCEPT;
#endif
    } else if (level == IPPROTO_IPV6) {
        if (optname == IPV6_V6ONLY) return WASMIFY_SOCKOPT_V6ONLY;
    }
    return 0;
}

/* Which timeout a (level, option) names, or 0 for an option that is not a
 * timeout. */
static int __wasmify_sock_timeout_of(int level, int optname) {
    if (level != SOL_SOCKET) return 0;
    if (optname == SO_RCVTIMEO) return WASMIFY_SOCK_TIMEOUT_RECEIVE;
    if (optname == SO_SNDTIMEO) return WASMIFY_SOCK_TIMEOUT_SEND;
    return 0;
}

int setsockopt(int fd, int level, int optname, const void *optval, socklen_t optlen) {
    if (const int which = __wasmify_sock_timeout_of(level, optname)) {
        if (optval == nullptr) { errno = EFAULT; return -1; }
        /* The value is a struct timeval, or, of the size of two longs, the
         * timeval of a time_t as wide as long (Linux's SO_RCVTIMEO_OLD and
         * SO_SNDTIMEO_OLD where time_t is wider): seconds and
         * microseconds. */
        struct timeval tv;
        if (optlen >= (socklen_t)sizeof(struct timeval)) {
            memcpy(&tv, optval, sizeof(tv));
        } else if (optlen == (socklen_t)(2 * sizeof(long))) {
            long pair[2];
            memcpy(pair, optval, sizeof(pair));
            tv.tv_sec = pair[0];
            tv.tv_usec = pair[1];
        } else {
            errno = EINVAL;
            return -1;
        }
        if (tv.tv_sec < 0 || tv.tv_usec < 0 || tv.tv_usec >= 1000000) { errno = EDOM; return -1; }
        /* a time too far off to count in nanoseconds is the farthest one
         * there is */
        const int64_t limit = INT64_MAX / 1000000000 - 1;
        const int64_t nanos = (int64_t)tv.tv_sec > limit
            ? INT64_MAX
            : (int64_t)tv.tv_sec * 1000000000 + (int64_t)tv.tv_usec * 1000;
        int64_t old = 0;
        int r = __wasmify_host_sock_timeout(fd, which, 1, nanos, &old);
        if (r != 0) { errno = (r < 0) ? -r : r; return -1; }
        return 0;
    }
    const int option = __wasmify_sock_option(level, optname);
    if (option == 0) { errno = ENOPROTOOPT; return -1; }
    if (optval == nullptr) { errno = EFAULT; return -1; }
    int value = 0;
    if (option == WASMIFY_SOCKOPT_ERROR || option == WASMIFY_SOCKOPT_TYPE ||
        option == WASMIFY_SOCKOPT_ACCEPTCONN) {
        errno = ENOPROTOOPT; /* read only */
        return -1;
    }
    if (option == WASMIFY_SOCKOPT_LINGER) {
        if (optlen < (socklen_t)sizeof(struct linger)) { errno = EINVAL; return -1; }
        struct linger l;
        memcpy(&l, optval, sizeof(l));
        value = l.l_onoff ? l.l_linger : -1;
    } else {
        if (optlen < (socklen_t)sizeof(int)) { errno = EINVAL; return -1; }
        memcpy(&value, optval, sizeof(value));
    }
    int r = __wasmify_host_sock_setopt(fd, option, value);
    if (r != 0) { errno = (r < 0) ? -r : r; return -1; }
    return 0;
}

int getsockopt(int fd, int level, int optname, void *optval, socklen_t *optlen) {
    if (const int which = __wasmify_sock_timeout_of(level, optname)) {
        if (optval == nullptr || optlen == nullptr) { errno = EFAULT; return -1; }
        if (*optlen < (socklen_t)sizeof(struct timeval)) { errno = EINVAL; return -1; }
        int64_t nanos = 0;
        int r = __wasmify_host_sock_timeout(fd, which, 0, 0, &nanos);
        if (r != 0) { errno = (r < 0) ? -r : r; return -1; }
        struct timeval tv;
        tv.tv_sec = (time_t)(nanos / 1000000000);
        tv.tv_usec = (suseconds_t)((nanos % 1000000000) / 1000);
        memcpy(optval, &tv, sizeof(tv));
        *optlen = sizeof(tv);
        return 0;
    }
    const int option = __wasmify_sock_option(level, optname);
    if (option == 0) { errno = ENOPROTOOPT; return -1; }
    if (optval == nullptr || optlen == nullptr) { errno = EFAULT; return -1; }
    int value = 0;
    int r = __wasmify_host_sock_getopt(fd, option, &value);
    if (r != 0) { errno = (r < 0) ? -r : r; return -1; }
    if (option == WASMIFY_SOCKOPT_LINGER) {
        if (*optlen < (socklen_t)sizeof(struct linger)) { errno = EINVAL; return -1; }
        struct linger l;
        l.l_onoff = value >= 0;
        l.l_linger = value >= 0 ? value : 0;
        memcpy(optval, &l, sizeof(l));
        *optlen = sizeof(l);
        return 0;
    }
    if (option == WASMIFY_SOCKOPT_TYPE)
        value = (value == WASMIFY_SOCK_TYPE_DGRAM) ? SOCK_DGRAM : SOCK_STREAM;
    if (*optlen < (socklen_t)sizeof(int)) { errno = EINVAL; return -1; }
    memcpy(optval, &value, sizeof(value));
    *optlen = sizeof(value);
    return 0;
}

/* socketpair(): a pair of connected sockets. The Unix domain has pairs, of
 * stream and of datagram sockets; the other families have none
 * (EOPNOTSUPP), as socketpair(2) has it. */
int socketpair(int domain, int type, int protocol, int sv[2]) {
    const int family = __wasmify_sock_family(domain);
    if (family == 0) { errno = EAFNOSUPPORT; return -1; }
    const int flags = type & (SOCK_NONBLOCK | SOCK_CLOEXEC);
    const int host_type = __wasmify_sock_type(family, type & ~flags, protocol);
    if (host_type == 0) { errno = EPROTONOSUPPORT; return -1; }
    if (sv == nullptr) { errno = EFAULT; return -1; }
    int fds[2] = {-1, -1};
    int r = __wasmify_host_sock_socketpair(family, host_type, fds);
    if (r != 0) { errno = (r < 0) ? -r : r; return -1; }
    if (__wasmify_sock_flags(fds, 2, flags) != 0) return -1;
    sv[0] = fds[0];
    sv[1] = fds[1];
    return 0;
}

/* sendto() / recvfrom(): the host knows what kind of socket fd is. On a
 * datagram socket the address is the datagram's destination, and recvfrom
 * reports who sent the datagram it hands over. On a stream socket the
 * address carries nothing — the peer is the connection's: sendto is send,
 * and recvfrom is recv plus the peer's name. */
/* Translates the MSG_ flags of a send or a receive into the host's. A flag
 * outside allowed is one the call does not have: -1, and the caller reports
 * EOPNOTSUPP. */
static int __wasmify_msg_flags(int flags, int allowed) {
    if (flags & ~allowed) return -1;
    int host = 0;
    if (flags & MSG_PEEK) host |= WASMIFY_MSG_PEEK;
    if (flags & MSG_WAITALL) host |= WASMIFY_MSG_WAITALL;
    if (flags & MSG_DONTWAIT) host |= WASMIFY_MSG_DONTWAIT;
    if (flags & MSG_NOSIGNAL) host |= WASMIFY_MSG_NOSIGNAL;
    return host;
}

ssize_t sendto(int fd, const void *buf, size_t len, int flags,
               const struct sockaddr *addr, socklen_t addrlen) {
    const int host_flags = __wasmify_msg_flags(flags, MSG_DONTWAIT | MSG_NOSIGNAL);
    if (host_flags < 0) { errno = EOPNOTSUPP; return -1; }
    struct __wasmify_sock_addr to;
    char path[PATH_MAX];
    int path_len = 0;
    if (addr != nullptr) {
        if (const int e = __wasmify_sock_addr_in(addr, addrlen, &to, path, sizeof(path), &path_len)) {
            errno = e;
            return -1;
        }
    }
    /* A send of more than the host takes at once sends a part, as a stream
     * socket's send may; a datagram that long is more than any socket
     * carries, and the host refuses it (EMSGSIZE). */
    if (len > INT_MAX) len = INT_MAX;
    int sent = 0;
    int r = __wasmify_host_sock_sendto(fd, buf, static_cast<int>(len), host_flags,
                                       addr != nullptr ? &to : nullptr, path, path_len, &sent);
    if (r != 0) {
        errno = (r < 0) ? -r : r;
#ifdef WASMIFY_HOST_SIGNALS
        /* the SIGPIPE the host posted with EPIPE, delivered before the call
         * returns (see host_signal.cc) */
        if (errno == EPIPE) {
            __wasmify_signal_after_call();
            errno = EPIPE;
        }
#endif
        return -1;
    }
    return sent;
}

ssize_t recvfrom(int fd, void *buf, size_t len, int flags,
                 struct sockaddr *addr, socklen_t *addrlen) {
    const int host_flags = __wasmify_msg_flags(flags, MSG_PEEK | MSG_WAITALL | MSG_DONTWAIT);
    if (host_flags < 0) { errno = EOPNOTSUPP; return -1; }
    if (len > INT_MAX) len = INT_MAX;
    struct __wasmify_sock_addr from;
    int received = 0;
    int r = __wasmify_host_sock_recvfrom(fd, buf, static_cast<int>(len), host_flags, &from, &received);
    if (r != 0) { errno = (r < 0) ? -r : r; return -1; }
    if (addr != nullptr && addrlen != nullptr) __wasmify_sock_addr_out(&from, addr, addrlen);
    return received;
}

/* send() / recv(): sendto and recvfrom without an address. wasi-libc's own
 * pair goes to the standard wasi calls, which have no flag for a send and
 * two for a receive, and refuses every other flag; these replace it. */
ssize_t send(int fd, const void *buf, size_t len, int flags) {
    return sendto(fd, buf, len, flags, nullptr, 0);
}

ssize_t recv(int fd, void *buf, size_t len, int flags) {
    return recvfrom(fd, buf, len, flags, nullptr, nullptr);
}

#endif /* WASMIFY_HOST_NETWORK_SOCKETS */

} /* extern "C" */

/* ---- shutdown, the network databases, inet_ntop, poll ----------------- */

namespace {

#ifdef WASMIFY_HOST_NETWORK_SOCKETS
int fail(int error) {
    errno = error;
    return -1;
}
#endif

#ifdef WASMIFY_HOST_NETWORK_RESOLVER
/* A lookup in the host's services database. The entry lives in static
 * storage, as the interface has it. */
struct servent *find_service(const char *name, int port, int by_port, const char *proto) {
    static struct servent ent;
    static Record rec;
    static char **aliases = NULL;
    static size_t aliases_cap = 0;
    if (!proto) proto = "";
    const int rc = lookup_service(rec, name, port, by_port, proto);
    if (rc != 0) {
        if (rc != -ENOENT) errno = -rc;
        return NULL;
    }
    /* The record: u32 port, then strings as a u32 length and the bytes —
     * the name, the protocol — a u32 count and the aliases. Each string is
     * moved down onto its length, which makes room for its terminator. */
    const uint32_t needed = rec.len;
    uint32_t pos = 0, value = 0;
    auto number = [&](uint32_t *out) {
        if (needed - pos < 4) return false;
        memcpy(out, rec.data + pos, 4);
        pos += 4;
        return true;
    };
    auto string = [&]() -> char * {
        uint32_t n = 0;
        if (!number(&n) || needed - pos < n) return NULL;
        char *s = rec.data + pos - 4;
        memmove(s, rec.data + pos, n);
        s[n] = '\0';
        pos += n;
        return s;
    };
    if (!number(&value)) return NULL;
    ent.s_port = (int)htons((uint16_t)value);
    ent.s_name = string();
    ent.s_proto = string();
    uint32_t count = 0;
    if (!ent.s_name || !ent.s_proto || !number(&count)) return NULL;
    /* each alias takes its length at least */
    if (count > (needed - pos) / 4) return NULL;
    if (!room(&aliases, &aliases_cap, (size_t)count + 1)) return NULL;
    for (uint32_t i = 0; i < count; i++) {
        aliases[i] = string();
        if (!aliases[i]) return NULL;
    }
    aliases[count] = NULL;
    ent.s_aliases = aliases;
    return &ent;
}

/* The protocols database: the file /etc/protocols, as the C library of
 * the system reads it, or, where that file cannot be opened (an in-memory
 * filesystem has none), the entries every /etc/protocols starts with. */
char *no_aliases[] = {NULL};
char proto_ip[] = "ip", proto_icmp[] = "icmp", proto_tcp[] = "tcp", proto_udp[] = "udp",
     proto_ipv6[] = "ipv6", proto_icmpv6[] = "ipv6-icmp";
struct protoent builtin_protocols[] = {
    {proto_ip, no_aliases, 0},      {proto_icmp, no_aliases, 1},
    {proto_tcp, no_aliases, 6},     {proto_udp, no_aliases, 17},
    {proto_ipv6, no_aliases, 41},   {proto_icmpv6, no_aliases, 58},
};
FILE *protocol_file = NULL; /* getprotoent's position in /etc/protocols */
size_t protocol_next = 0;   /* ... or in the entries above */
bool protocol_from_file = false;

/* The next entry of the open protocols file: protocols(5), a name, a
 * number and aliases a line, white space between them, # to the end of
 * the line a comment. The entry lives in static storage; NULL at the end
 * of the file and on a failure (errno set). */
struct protoent *next_protocol_line(FILE *f) {
    static char *line = NULL;
    static size_t line_cap = 0;
    static char **aliases = NULL;
    static size_t aliases_cap = 0;
    static struct protoent ent;
    while (getline(&line, &line_cap, f) >= 0) {
        if (char *hash = strchr(line, '#')) *hash = '\0';
        char *save = NULL;
        char *name = strtok_r(line, " \t\r\n", &save);
        char *number = name ? strtok_r(NULL, " \t\r\n", &save) : NULL;
        if (!number) continue;
        char *end = NULL;
        const long value = strtol(number, &end, 10);
        if (*end != '\0' || value < 0 || value > 255) continue;
        size_t n = 0;
        for (;;) {
            if (!room(&aliases, &aliases_cap, n + 1)) return NULL;
            char *alias = strtok_r(NULL, " \t\r\n", &save);
            aliases[n] = alias;
            if (!alias) break;
            n++;
        }
        ent.p_name = name;
        ent.p_aliases = aliases;
        ent.p_proto = (int)value;
        return &ent;
    }
    return NULL;
}

/* The next entry of the database from where the walk is: the file when it
 * can be opened, the built-in entries otherwise. */
struct protoent *next_protocol() {
    if (!protocol_file && !protocol_from_file && protocol_next == 0) {
        protocol_file = fopen("/etc/protocols", "re");
        protocol_from_file = protocol_file != NULL;
    }
    if (protocol_from_file) return protocol_file ? next_protocol_line(protocol_file) : NULL;
    const size_t count = sizeof(builtin_protocols) / sizeof(builtin_protocols[0]);
    return protocol_next < count ? &builtin_protocols[protocol_next++] : NULL;
}

void rewind_protocols() {
    /* The file is only read: its close has nothing to lose, and the walk
     * starts over either way. */
    if (protocol_file) fclose(protocol_file);
    protocol_file = NULL;
    protocol_from_file = false;
    protocol_next = 0;
}

/* A lookup walks the database from its start, as glibc's does (and leaves
 * a walk of getprotoent's where it starts over). */
template <typename Match> struct protoent *find_protocol(Match match) {
    rewind_protocols();
    struct protoent *p;
    while ((p = next_protocol()) != NULL && !match(p)) {
    }
    rewind_protocols();
    return p;
}

#endif /* WASMIFY_HOST_NETWORK_RESOLVER */

#ifdef WASMIFY_HOST_NETWORK_SOCKETS
/* What a subscription of poll's is for, in the low bits of its userdata
 * (the descriptor's index is above them). */
const uint64_t POLL_READ = 0, POLL_WRITE = 1, POLL_UNASKED = 2;

uint64_t poll_userdata(size_t index, uint64_t kind) { return (uint64_t)index << 2 | kind; }
#endif

} /* namespace */

extern "C" {

#ifdef WASMIFY_HOST_NETWORK_SOCKETS
/* ---- sockets ----------------------------------------------------------------
 * shutdown: "how" is 0, 1 or 2 (SHUT_RD, SHUT_WR, SHUT_RDWR as every
 * system numbers them, and as the <sys/socket.h> of host-include defines
 * them; programs pass the numbers literally too); the WASI call takes one
 * bit per direction. This definition takes the place of wasi-libc's, which
 * hands its argument over as the bits. */
int shutdown(int fd, int how) {
    __wasi_sdflags_t flags;
    switch (how) {
    case 0: /* SHUT_RD */
        flags = __WASI_SDFLAGS_RD;
        break;
    case 1: /* SHUT_WR */
        flags = __WASI_SDFLAGS_WR;
        break;
    case 2: /* SHUT_RDWR */
        flags = __WASI_SDFLAGS_RD | __WASI_SDFLAGS_WR;
        break;
    default:
        return fail(EINVAL);
    }
    const __wasi_errno_t error = __wasi_sock_shutdown((__wasi_fd_t)fd, flags);
    if (error != 0) return fail(error);
    return 0;
}

#endif /* WASMIFY_HOST_NETWORK_SOCKETS */

#ifdef WASMIFY_HOST_NETWORK_RESOLVER
/* ---- network databases -----------------------------------------------------
 * Hosts: lookups by name go to the host (gethostbyname, above); there is
 * no table to walk. */
void sethostent(int stayopen) { (void)stayopen; }
void endhostent(void) {}
struct hostent *gethostent(void) { return NULL; }

/* Networks: no database. */
void setnetent(int stayopen) { (void)stayopen; }
void endnetent(void) {}
struct netent *getnetent(void) { return NULL; }
struct netent *getnetbyname(const char *name) {
    (void)name;
    return NULL;
}
struct netent *getnetbyaddr(uint32_t net, int type) {
    (void)net;
    (void)type;
    return NULL;
}

/* Services: lookups by name and by port go to the host, which has the
 * database it is given; there is no table to walk. */
void setservent(int stayopen) { (void)stayopen; }
void endservent(void) {}
struct servent *getservent(void) { return NULL; }
struct servent *getservbyname(const char *name, const char *proto) {
    if (!name) return NULL;
    return find_service(name, 0, 0, proto);
}
struct servent *getservbyport(int port, const char *proto) {
    /* the port is in network byte order, as in a sockaddr */
    return find_service("", (int)ntohs((uint16_t)port), 1, proto);
}

/* Protocols: the database above, by name or alias, or by number. */
void setprotoent(int stayopen) {
    (void)stayopen;
    rewind_protocols();
}
void endprotoent(void) { rewind_protocols(); }
struct protoent *getprotoent(void) {
    struct protoent *p = next_protocol();
    if (!p) rewind_protocols();
    return p;
}
struct protoent *getprotobyname(const char *name) {
    if (!name) return NULL;
    return find_protocol([name](struct protoent *p) {
        if (strcmp(p->p_name, name) == 0) return true;
        for (char **a = p->p_aliases; *a; a++)
            if (strcmp(*a, name) == 0) return true;
        return false;
    });
}
struct protoent *getprotobynumber(int number) {
    return find_protocol([number](struct protoent *p) { return p->p_proto == number; });
}

#endif /* WASMIFY_HOST_NETWORK_RESOLVER */

/* inet_ntop(3) as glibc words an address. The C library words an
 * IPv4-compatible IPv6 address (the first 96 bits zero, the next 16 not)
 * in hexadecimal (::102:304) where glibc ends it in the dotted IPv4
 * notation (::1.2.3.4); every other address it words as glibc does. The
 * link wraps inet_ntop with the network capability. */
const char *__real_inet_ntop(int af, const void *src, char *dst, socklen_t size);
const char *__wrap_inet_ntop(int af, const void *src, char *dst, socklen_t size) {
    static const unsigned char zeros[12] = {0};
    const unsigned char *b = static_cast<const unsigned char *>(src);
    if (af != AF_INET6 || memcmp(b, zeros, sizeof(zeros)) != 0 || (b[12] == 0 && b[13] == 0))
        return __real_inet_ntop(af, src, dst, size);
    const int n = snprintf(dst, size, "::%u.%u.%u.%u", b[12], b[13], b[14], b[15]);
    if (n < 0 || (socklen_t)n >= size) {
        errno = ENOSPC;
        return NULL;
    }
    return dst;
}

#ifdef WASMIFY_HOST_NETWORK_SOCKETS
/* ---- poll(2) ---------------------------------------------------------------
 * poll over poll_oneoff, as wasi-libc's ppoll, except that a descriptor not
 * asked for reading is still watched for the events poll reports whether
 * asked or not: POLLNVAL (it is not open), POLLERR (nobody reads the pipe
 * it writes to any more) and POLLHUP (its peer has hung up). wasi-libc
 * refuses a descriptor asked for neither reading nor writing (ENOSYS: it
 * builds one subscription per POLLRDNORM and POLLWRNORM).
 *
 * Such a descriptor gets a subscription for reading whose readiness is not
 * reported, as it was not asked for: only its error (POLLNVAL, POLLERR) and
 * the event's hang-up flag (POLLHUP). When a wait ends with nothing to
 * report but such readiness — the descriptor has input nobody asked about —
 * the wait goes on without that subscription for what is left of the
 * timeout, as poll(2) would not have returned. */
int poll(struct pollfd *fds, nfds_t nfds, int timeout) {
    for (nfds_t i = 0; i < nfds; i++) fds[i].revents = 0;
    /* A descriptor whose unasked readiness ended a wait: left out of the
     * waits that follow. */
    std::vector<bool> quiet(nfds, false);
    const bool bounded = timeout >= 0;
    __wasi_timestamp_t now = 0;
    if (bounded) {
        if (const __wasi_errno_t error = __wasi_clock_time_get(__WASI_CLOCKID_MONOTONIC, 1, &now)) {
            errno = error;
            return -1;
        }
    }
    const uint64_t deadline = bounded ? now + (uint64_t)timeout * 1000000 : 0;
    std::vector<__wasi_subscription_t> subs;
    std::vector<__wasi_event_t> events;
    for (;;) {
        subs.clear();
        for (nfds_t i = 0; i < nfds; i++) {
            if (fds[i].fd < 0) continue;
            /* POLLPRI is never reported: nothing here has priority data */
            const short asked = fds[i].events & ~POLLPRI;
            __wasi_subscription_t sub = {};
            if (asked & POLLRDNORM) {
                sub.userdata = poll_userdata(i, POLL_READ);
                sub.u.tag = __WASI_EVENTTYPE_FD_READ;
                sub.u.u.fd_read.file_descriptor = fds[i].fd;
                subs.push_back(sub);
            }
            if (asked & POLLWRNORM) {
                sub.userdata = poll_userdata(i, POLL_WRITE);
                sub.u.tag = __WASI_EVENTTYPE_FD_WRITE;
                sub.u.u.fd_write.file_descriptor = fds[i].fd;
                subs.push_back(sub);
            }
            if (!(asked & POLLRDNORM) && !quiet[i]) {
                sub.userdata = poll_userdata(i, POLL_UNASKED);
                sub.u.tag = __WASI_EVENTTYPE_FD_READ;
                sub.u.u.fd_read.file_descriptor = fds[i].fd;
                subs.push_back(sub);
            }
        }
        const bool watched = !subs.empty();
        if (bounded || (!watched && nfds > 0)) {
            /* The timeout; with nothing left to watch and none given, a
             * clock of 2^62 ns (over a century) stands for none: the wait
             * is one a signal ends, as poll(2) waits on descriptors that
             * report nothing. */
            uint64_t left = (uint64_t)1 << 62;
            if (bounded) {
                if (const __wasi_errno_t error = __wasi_clock_time_get(__WASI_CLOCKID_MONOTONIC, 1, &now)) {
                    errno = error;
                    return -1;
                }
                left = deadline > now ? deadline - now : 0;
            }
            __wasi_subscription_t sub = {};
            sub.userdata = UINT64_MAX;
            sub.u.tag = __WASI_EVENTTYPE_CLOCK;
            sub.u.u.clock.id = __WASI_CLOCKID_MONOTONIC;
            sub.u.u.clock.timeout = left;
            subs.push_back(sub);
        }
        if (subs.empty()) {
            /* nothing at all to wait for: as wasi-libc */
            errno = ENOTSUP;
            return -1;
        }
        events.resize(subs.size());
        size_t nevents = 0;
        const __wasi_errno_t error = __wasi_poll_oneoff(subs.data(), events.data(), subs.size(), &nevents);
        if (error != 0) {
            errno = error;
            return -1;
        }
        bool expired = false;
        for (size_t e = 0; e < nevents; e++) {
            const __wasi_event_t &ev = events[e];
            if (ev.type == __WASI_EVENTTYPE_CLOCK) {
                expired = expired || bounded;
                continue;
            }
            const size_t i = (size_t)(ev.userdata >> 2);
            const uint64_t kind = ev.userdata & 3;
            if (i >= nfds) continue;
            struct pollfd &p = fds[i];
            if (ev.error == __WASI_ERRNO_BADF) {
                p.revents |= POLLNVAL;
            } else if (ev.error != 0) {
                /* in error: the reader of the pipe it writes to has closed
                 * it, and a write fails at once */
                p.revents |= POLLERR;
            } else {
                const bool hangup = (ev.fd_readwrite.flags & __WASI_EVENTRWFLAGS_FD_READWRITE_HANGUP) != 0;
                if (kind == POLL_READ) p.revents |= POLLRDNORM;
                if (kind == POLL_WRITE) p.revents |= POLLWRNORM;
                if (hangup) p.revents |= POLLHUP;
                /* readable, and nobody asked: not an event of poll's */
                if (kind == POLL_UNASKED && !hangup) quiet[i] = true;
            }
        }
        /* A socket whose peer has hung up is reported hung up and, when
         * that was asked, writable (the write finds out), as on Linux. */
        int ready = 0;
        for (nfds_t i = 0; i < nfds; i++) {
            if (fds[i].revents) ready++;
        }
        if (ready > 0 || expired) return ready;
        if (bounded) {
            if (const __wasi_errno_t error = __wasi_clock_time_get(__WASI_CLOCKID_MONOTONIC, 1, &now)) {
                errno = error;
                return -1;
            }
            if (now >= deadline) return 0;
        }
    }
}
#endif /* WASMIFY_HOST_NETWORK_SOCKETS */

} /* extern "C" */

#endif /* WASMIFY_HOST_NETWORK */
