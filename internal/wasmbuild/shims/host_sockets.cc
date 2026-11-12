/* host_sockets.cc — generic host-provided socket shim.
 *
 * This translation unit is deployed and compiled by wasmify itself, and only
 * when host socket support is opted in (bridge.HostSockets in wasmify.json,
 * which makes wasmify define WASMIFY_HOST_SOCKETS for this compile). Without
 * that opt-in the file is excluded entirely and the wasm imports only standard
 * wasi, staying portable to any wasi runtime.
 *
 * WASI preview1 has no way to create, connect or bind a socket, and
 * wasi-libc under __wasip1__ does not even declare or define socket()/connect()
 * (the prototypes are guarded out). So we DEFINE them here, backed by host
 * imports the WASI host implements in Go (e.g. via net.Dial / net.Listen). The
 * resulting fd is host-managed; send()/recv()/close() keep flowing through the
 * standard wasi sock_send/sock_recv/fd_close path the host already implements.
 *
 * What is handled: IPv4 TCP sockets — clients (connect) and servers (bind,
 * listen, accept) — and IPv4 UDP sockets (bind, connect, sendto, recvfrom,
 * and send/recv once connected); send and recv with the flags MSG_PEEK,
 * MSG_WAITALL, MSG_DONTWAIT and MSG_NOSIGNAL; their names (getsockname,
 * getpeername), a fixed set of socket options, the receive and send
 * timeouts, and socketpair() for a pair of connected AF_UNIX stream
 * sockets. Everything else fails with the errno the missing
 * feature has: a socket of another type cannot be created (EPROTONOSUPPORT),
 * another address family is EAFNOSUPPORT, an option outside the set is
 * ENOPROTOOPT. Nothing here reports success for something it did not do.
 *
 * The matching declarations come from wasmify's <netdb.h> stub,
 * deployed into the build-local host-include dir that every wasm-build compile
 * gets on its -I whenever host sockets are opted in (independent of the
 * POSIX-compat overlay), so a libc socket module compiled with
 * HAVE_SOCKET/HAVE_CONNECT sees the same struct addrinfo layout this file
 * fills in. */
#ifdef WASMIFY_HOST_SOCKETS

#include <sys/socket.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <arpa/inet.h>
#include <netdb.h>
#include <fcntl.h>
#include <cerrno>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <unistd.h>
#include <wasi/api.h>

extern "C" {

/* The socket types, by the host's own numbering (libc's SOCK_* numbers
 * differ from libc to libc). */
#define WASMIFY_SOCK_TYPE_STREAM 1
#define WASMIFY_SOCK_TYPE_DGRAM 2

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_socket")))
extern int __wasmify_host_sock_socket(int domain, int type);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_connect")))
extern int __wasmify_host_sock_connect(int fd, unsigned int ip_be, int port);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_getaddrinfo")))
extern int __wasmify_host_getaddrinfo(const char *node, int len, unsigned int *out_ip_be);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_bind")))
extern int __wasmify_host_sock_bind(int fd, unsigned int ip_be, int port);

/* The name of the host with an IPv4 address (as it sits in s_addr), not
 * NUL-terminated: *needed receives its length, also when buf is too small
 * (-ERANGE). -ENOENT when the address has no name. */
__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_reverse")))
extern int __wasmify_host_sock_reverse(unsigned int ip_be, char *buf, int buf_len,
                                       unsigned int *needed);

/* An entry of the host's services database, by name (by_port 0) or by
 * port, for the protocol proto when it is not empty. The record is the port
 * (u32), then the name and the protocol, each a u32 length and the bytes,
 * then the aliases. -ENOENT when there is no such service. */
__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_service")))
extern int __wasmify_host_sock_service(const char *name, int name_len, int port, int by_port,
                                       const char *proto, int proto_len, void *buf,
                                       int buf_len, unsigned int *needed);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_listen")))
extern int __wasmify_host_sock_listen(int fd, int backlog);

/* A socket name from the host: family (1 IPv4, 2 Unix), the IPv4 address as
 * it sits in sin_addr.s_addr, and the port in host byte order. */
struct __wasmify_sock_name { uint32_t family; uint32_t ip_be; uint32_t port; };
#define WASMIFY_SOCK_FAMILY_IPV4 1
#define WASMIFY_SOCK_FAMILY_UNIX 2

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_getsockname")))
extern int __wasmify_host_sock_getsockname(int fd, struct __wasmify_sock_name *out);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_getpeername")))
extern int __wasmify_host_sock_getpeername(int fd, struct __wasmify_sock_name *out);

/* The flags of a send or a receive the host knows, by the host's own
 * numbering (libc's MSG_ numbers differ from libc to libc). */
#define WASMIFY_MSG_PEEK 1     /* receive: leave it in place */
#define WASMIFY_MSG_WAITALL 2  /* receive: until the buffer is full */
#define WASMIFY_MSG_DONTWAIT 4 /* do not wait, whatever O_NONBLOCK says */
#define WASMIFY_MSG_NOSIGNAL 8 /* send: EPIPE without SIGPIPE */

/* sendto: with has_addr, a datagram to ip_be:port; without, a send to the
 * socket's peer. flags are WASMIFY_MSG_ flags. *sent receives the count. */
__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_sendto")))
extern int __wasmify_host_sock_sendto(int fd, const void *buf, int len, int flags,
                                      int has_addr, unsigned int ip_be, int port, int *sent);

/* recvfrom: one datagram (or, on a stream socket, what there is) into buf;
 * flags are WASMIFY_MSG_ flags. *from receives the sender's name, with
 * family 0 when there is none. */
__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_recvfrom")))
extern int __wasmify_host_sock_recvfrom(int fd, void *buf, int len, int flags,
                                        struct __wasmify_sock_name *from, int *received);

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
__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_timeout")))
extern int __wasmify_host_sock_timeout(int fd, int which, int set, int64_t nanos,
                                       int64_t *old_nanos);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_setopt")))
extern int __wasmify_host_sock_setopt(int fd, int option, int value);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_getopt")))
extern int __wasmify_host_sock_getopt(int fd, int option, int *value);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_pair")))
extern int __wasmify_host_sock_pair(int fds[2]);

/* struct addrinfo, the EAI_* values, and the prototypes this file
 * implements all come from wasmify's <netdb.h> stub header (included
 * above) — the SAME header a consuming libc socket module compiles
 * against, so the struct layout the module reads is the layout built
 * here by construction. */
#define WASMIFY_EAI_FAIL EAI_FAIL
#define WASMIFY_EAI_MEMORY EAI_MEMORY
#define WASMIFY_EAI_NONAME EAI_NONAME

/* Resolves a host name to one IPv4 address, as it sits in s_addr. An
 * address written in numbers is converted here: that is no name lookup,
 * and the host's lookup policy is not asked about it. Returns 0 or the
 * host's negative errno. */
static int __wasmify_resolve_ipv4(const char *node, unsigned int *ip_be) {
    struct in_addr literal;
    if (node != nullptr && inet_pton(AF_INET, node, &literal) == 1) {
        *ip_be = literal.s_addr;
        return 0;
    }
    return __wasmify_host_getaddrinfo(node, node ? (int)strlen(node) : 0, ip_be);
}

/* getaddrinfo(): resolve node via the host (numeric IPs are converted
 * here), returning a single IPv4 result. The port comes from a numeric
 * service string; named services are not resolved (port 0). */
int getaddrinfo(const char *node, const char *service,
                const struct addrinfo *hints, struct addrinfo **res) {
    unsigned int ip_be = 0;
    int rc = __wasmify_resolve_ipv4(node, &ip_be);
    if (rc != 0) {
        return WASMIFY_EAI_NONAME;
    }
    int port = 0;
    if (service != nullptr && service[0] != '\0') {
        port = atoi(service);
    }
    struct sockaddr_in *sa =
        static_cast<struct sockaddr_in *>(calloc(1, sizeof(struct sockaddr_in)));
    struct addrinfo *ai =
        static_cast<struct addrinfo *>(calloc(1, sizeof(struct addrinfo)));
    if (sa == nullptr || ai == nullptr) {
        free(sa);
        free(ai);
        return WASMIFY_EAI_MEMORY;
    }
    sa->sin_family = AF_INET;
    sa->sin_port = htons(static_cast<unsigned short>(port));
    sa->sin_addr.s_addr = ip_be;
    ai->ai_family = AF_INET;
    ai->ai_socktype = (hints != nullptr && hints->ai_socktype != 0) ? hints->ai_socktype : SOCK_STREAM;
    ai->ai_protocol = (hints != nullptr) ? hints->ai_protocol : 0;
    ai->ai_addrlen = sizeof(struct sockaddr_in);
    ai->ai_canonname = nullptr;
    ai->ai_addr = reinterpret_cast<struct sockaddr *>(sa);
    ai->ai_next = nullptr;
    *res = ai;
    return 0;
}

void freeaddrinfo(struct addrinfo *ai) {
    while (ai != nullptr) {
        struct addrinfo *next = ai->ai_next;
        if (ai->ai_addr != nullptr) {
            free(ai->ai_addr);
        }
        free(ai);
        ai = next;
    }
}

const char *gai_strerror(int ecode) {
    (void)ecode;
    return "getaddrinfo failed";
}

/* The name of the host with an IPv4 address, into buf as a C string.
 * Returns 0, or the host's negative errno (-ERANGE for a name that does not
 * fit). */
static int __wasmify_reverse_ipv4(unsigned int ip_be, char *buf, size_t size) {
    unsigned int needed = 0;
    if (size == 0) return -ERANGE;
    int rc = __wasmify_host_sock_reverse(ip_be, buf, (int)size - 1, &needed);
    if (rc != 0) return rc;
    buf[needed] = '\0';
    return 0;
}

/* getnameinfo(): the host's name and the service's, from the host's
 * resolver and services database. An address or a port that has no name
 * is given in numbers — unless the caller requires a name (NI_NAMEREQD) —
 * and so are both when the caller asks for numbers (NI_NUMERICHOST,
 * NI_NUMERICSERV). */
int getnameinfo(const struct sockaddr *sa, socklen_t salen, char *host,
                socklen_t hostlen, char *serv, socklen_t servlen, int flags) {
    (void)salen;
    if (sa == nullptr || sa->sa_family != AF_INET) return EAI_FAMILY;
    const struct sockaddr_in *in = reinterpret_cast<const struct sockaddr_in *>(sa);
    if (host != nullptr && hostlen > 0) {
        int named = -ENOENT;
        if (!(flags & NI_NUMERICHOST)) {
            named = __wasmify_reverse_ipv4(in->sin_addr.s_addr, host, hostlen);
            if (named == -ERANGE) return EAI_OVERFLOW;
        }
        if (named != 0) {
            if (flags & NI_NAMEREQD) return EAI_NONAME;
            unsigned int ip = ntohl(in->sin_addr.s_addr);
            int n = snprintf(host, hostlen, "%u.%u.%u.%u",
                             (ip >> 24) & 0xff, (ip >> 16) & 0xff,
                             (ip >> 8) & 0xff, ip & 0xff);
            if (n < 0 || (socklen_t)n >= hostlen) return EAI_OVERFLOW;
        }
    }
    if (serv != nullptr && servlen > 0) {
        const unsigned int port = ntohs(in->sin_port);
        bool named = false;
        if (!(flags & NI_NUMERICSERV)) {
            /* the record starts with the port and the name */
            char rec[512];
            unsigned int needed = 0;
            const char *proto = (flags & NI_DGRAM) ? "udp" : "tcp";
            if (__wasmify_host_sock_service("", 0, (int)port, 1, proto, 3, rec,
                                            (int)sizeof(rec), &needed) == 0 && needed >= 8) {
                unsigned int len = 0;
                memcpy(&len, rec + 4, 4);
                if (len <= needed - 8) {
                    if (len >= servlen) return EAI_OVERFLOW;
                    memcpy(serv, rec + 8, len);
                    serv[len] = '\0';
                    named = true;
                }
            }
        }
        if (!named) {
            int n = snprintf(serv, servlen, "%u", port);
            if (n < 0 || (socklen_t)n >= servlen) return EAI_OVERFLOW;
        }
    }
    return 0;
}

/* gethostbyname(): the host's lookup, one IPv4 address. The result lives
 * in static storage, as the interface has it. */
struct hostent *gethostbyname(const char *name) {
    static struct hostent ent;
    static struct in_addr addr;
    static char *addr_list[2];
    static char *no_aliases[1];
    static char canonical[256];
    if (name == nullptr || strlen(name) >= sizeof(canonical)) return nullptr;
    unsigned int ip_be = 0;
    if (__wasmify_resolve_ipv4(name, &ip_be) != 0) return nullptr;
    addr.s_addr = ip_be;
    strcpy(canonical, name);
    addr_list[0] = reinterpret_cast<char *>(&addr);
    addr_list[1] = nullptr;
    no_aliases[0] = nullptr;
    ent.h_name = canonical;
    ent.h_aliases = no_aliases;
    ent.h_addrtype = AF_INET;
    ent.h_length = sizeof(addr);
    ent.h_addr_list = addr_list;
    return &ent;
}

/* gethostbyaddr(): the host's reverse lookup of an IPv4 address. The
 * result lives in static storage, as the interface has it. */
struct hostent *gethostbyaddr(const void *addr, socklen_t len, int type) {
    static struct hostent ent;
    static struct in_addr kept;
    static char *addr_list[2];
    static char *no_aliases[1];
    static char name[256];
    if (addr == nullptr || type != AF_INET || len < (socklen_t)sizeof(kept)) return nullptr;
    memcpy(&kept, addr, sizeof(kept));
    if (__wasmify_reverse_ipv4(kept.s_addr, name, sizeof(name)) != 0) return nullptr;
    addr_list[0] = reinterpret_cast<char *>(&kept);
    addr_list[1] = nullptr;
    no_aliases[0] = nullptr;
    ent.h_name = name;
    ent.h_aliases = no_aliases;
    ent.h_addrtype = AF_INET;
    ent.h_length = sizeof(kept);
    ent.h_addr_list = addr_list;
    return &ent;
}

/* socket(): allocate a host-managed socket fd: a TCP or a UDP socket over
 * IPv4. Host returns the fd, or a negative errno on failure. */
int socket(int domain, int type, int protocol) {
    if (domain != AF_INET) { errno = EAFNOSUPPORT; return -1; }
    const int flags = type & (SOCK_NONBLOCK | SOCK_CLOEXEC);
    int host_type;
    if ((type & ~flags) == SOCK_STREAM && (protocol == 0 || protocol == IPPROTO_TCP)) {
        host_type = WASMIFY_SOCK_TYPE_STREAM;
    } else if ((type & ~flags) == SOCK_DGRAM && (protocol == 0 || protocol == IPPROTO_UDP)) {
        host_type = WASMIFY_SOCK_TYPE_DGRAM;
    } else {
        errno = EPROTONOSUPPORT;
        return -1;
    }
    int r = __wasmify_host_sock_socket(domain, host_type);
    if (r < 0) { errno = -r; return -1; }
    if ((flags & SOCK_NONBLOCK) && fcntl(r, F_SETFL, O_NONBLOCK) != 0) {
        const int e = errno;
        close(r);
        errno = e;
        return -1;
    }
    return r;
}

/* connect(): parse the IPv4 sockaddr and ask the host to dial. sin_addr.s_addr
 * is already network byte order (host decodes it); sin_port is network order,
 * converted to host order here. */
int connect(int fd, const struct sockaddr *addr, socklen_t addrlen) {
    if (addr == nullptr || addrlen < (socklen_t)sizeof(struct sockaddr_in) ||
        addr->sa_family != AF_INET) {
        errno = EAFNOSUPPORT;
        return -1;
    }
    const struct sockaddr_in *in = reinterpret_cast<const struct sockaddr_in *>(addr);
    int r = __wasmify_host_sock_connect(fd,
                                        static_cast<unsigned int>(in->sin_addr.s_addr),
                                        static_cast<int>(ntohs(in->sin_port)));
    if (r != 0) { errno = (r < 0) ? -r : r; return -1; }
    return 0;
}

/* bind(): give the socket a local IPv4 address; port 0 lets the host pick. */
int bind(int fd, const struct sockaddr *addr, socklen_t addrlen) {
    if (addr == nullptr || addrlen < (socklen_t)sizeof(struct sockaddr_in) ||
        addr->sa_family != AF_INET) {
        errno = EAFNOSUPPORT;
        return -1;
    }
    const struct sockaddr_in *in = reinterpret_cast<const struct sockaddr_in *>(addr);
    int r = __wasmify_host_sock_bind(fd,
                                     static_cast<unsigned int>(in->sin_addr.s_addr),
                                     static_cast<int>(ntohs(in->sin_port)));
    if (r != 0) { errno = (r < 0) ? -r : r; return -1; }
    return 0;
}

int listen(int fd, int backlog) {
    int r = __wasmify_host_sock_listen(fd, backlog);
    if (r != 0) { errno = (r < 0) ? -r : r; return -1; }
    return 0;
}

/* Renders a socket name from the host as a sockaddr, the way getsockname()
 * and friends return one: as much of it as fits is copied, and *addrlen is
 * the full size. */
static void __wasmify_fill_sockaddr(const struct __wasmify_sock_name *name,
                                    struct sockaddr *addr, socklen_t *addrlen) {
    if (name->family == WASMIFY_SOCK_FAMILY_UNIX) {
        /* an unnamed Unix-domain socket: the family and no path */
        struct sockaddr un;
        memset(&un, 0, sizeof(un));
        un.sa_family = AF_UNIX;
        socklen_t n = *addrlen;
        if (n > (socklen_t)sizeof(sa_family_t)) n = sizeof(sa_family_t);
        memcpy(addr, &un, n);
        *addrlen = sizeof(sa_family_t);
        return;
    }
    struct sockaddr_in in;
    memset(&in, 0, sizeof(in));
    in.sin_family = AF_INET;
    in.sin_port = htons(static_cast<unsigned short>(name->port));
    in.sin_addr.s_addr = name->ip_be;
    socklen_t n = *addrlen;
    if (n > (socklen_t)sizeof(in)) n = sizeof(in);
    memcpy(addr, &in, n);
    *addrlen = sizeof(in);
}

/* getsockname(): the socket's local name, from the host. */
int getsockname(int fd, struct sockaddr *addr, socklen_t *addrlen) {
    if (addr == nullptr || addrlen == nullptr) { errno = EFAULT; return -1; }
    struct __wasmify_sock_name name;
    int r = __wasmify_host_sock_getsockname(fd, &name);
    if (r != 0) { errno = (r < 0) ? -r : r; return -1; }
    __wasmify_fill_sockaddr(&name, addr, addrlen);
    return 0;
}

/* getpeername(): the name of the socket's peer, from the host. */
int getpeername(int fd, struct sockaddr *addr, socklen_t *addrlen) {
    if (addr == nullptr || addrlen == nullptr) { errno = EFAULT; return -1; }
    struct __wasmify_sock_name name;
    int r = __wasmify_host_sock_getpeername(fd, &name);
    if (r != 0) { errno = (r < 0) ? -r : r; return -1; }
    __wasmify_fill_sockaddr(&name, addr, addrlen);
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
    if (addr != nullptr && addrlen != nullptr) {
        struct __wasmify_sock_name name;
        int r = __wasmify_host_sock_getpeername(static_cast<int>(conn), &name);
        if (r != 0) {
            /* the peer went away already: an address of no family */
            *addrlen = 0;
        } else {
            __wasmify_fill_sockaddr(&name, addr, addrlen);
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
        }
    } else if (level == IPPROTO_TCP) {
        if (optname == TCP_NODELAY) return WASMIFY_SOCKOPT_NODELAY;
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
        /* The value is a struct timeval. Where time_t is wider than long,
         * that is not the pair of longs it used to be, and programs that
         * build the value by hand (a scripting language packing two native
         * longs) still pass the pair: both layouts are taken, told apart by
         * their size. */
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
        /* a time too far off to count is the farthest one there is */
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

/* socketpair(): a pair of connected AF_UNIX stream sockets. */
int socketpair(int domain, int type, int protocol, int sv[2]) {
    if (domain != AF_UNIX) { errno = EAFNOSUPPORT; return -1; }
    const int flags = type & (SOCK_NONBLOCK | SOCK_CLOEXEC);
    if ((type & ~flags) != SOCK_STREAM || protocol != 0) {
        errno = EPROTONOSUPPORT;
        return -1;
    }
    if (sv == nullptr) { errno = EFAULT; return -1; }
    int fds[2] = {-1, -1};
    int r = __wasmify_host_sock_pair(fds);
    if (r != 0) { errno = (r < 0) ? -r : r; return -1; }
    if (flags & SOCK_NONBLOCK) {
        for (int i = 0; i < 2; i++) {
            if (fcntl(fds[i], F_SETFL, O_NONBLOCK) != 0) {
                const int e = errno;
                close(fds[0]);
                close(fds[1]);
                errno = e;
                return -1;
            }
        }
    }
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
    int has_addr = 0;
    unsigned int ip_be = 0;
    int port = 0;
    if (addr != nullptr) {
        if (addrlen < (socklen_t)sizeof(struct sockaddr_in) || addr->sa_family != AF_INET) {
            errno = EAFNOSUPPORT;
            return -1;
        }
        const struct sockaddr_in *in = reinterpret_cast<const struct sockaddr_in *>(addr);
        has_addr = 1;
        ip_be = static_cast<unsigned int>(in->sin_addr.s_addr);
        port = static_cast<int>(ntohs(in->sin_port));
    }
    if (len > 0x7fffffff) { errno = EMSGSIZE; return -1; }
    int sent = 0;
    int r = __wasmify_host_sock_sendto(fd, buf, static_cast<int>(len), host_flags, has_addr,
                                       ip_be, port, &sent);
    if (r != 0) { errno = (r < 0) ? -r : r; return -1; }
    return sent;
}

ssize_t recvfrom(int fd, void *buf, size_t len, int flags,
                 struct sockaddr *addr, socklen_t *addrlen) {
    const int host_flags = __wasmify_msg_flags(flags, MSG_PEEK | MSG_WAITALL | MSG_DONTWAIT);
    if (host_flags < 0) { errno = EOPNOTSUPP; return -1; }
    if (len > 0x7fffffff) len = 0x7fffffff;
    struct __wasmify_sock_name name;
    int received = 0;
    int r = __wasmify_host_sock_recvfrom(fd, buf, static_cast<int>(len), host_flags, &name, &received);
    if (r != 0) { errno = (r < 0) ? -r : r; return -1; }
    if (addr != nullptr && addrlen != nullptr) {
        if (name.family == 0)
            *addrlen = 0;
        else
            __wasmify_fill_sockaddr(&name, addr, addrlen);
    }
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

} /* extern "C" */

#endif /* WASMIFY_HOST_SOCKETS */
