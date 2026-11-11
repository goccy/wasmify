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
 * listen, accept) — their names (getsockname, getpeername), a fixed set of
 * socket options, and socketpair() for a pair of connected AF_UNIX stream
 * sockets. Everything else fails with the errno the missing feature has: a
 * datagram socket cannot be created (EPROTONOSUPPORT), another address family
 * is EAFNOSUPPORT, an option outside the set is ENOPROTOOPT. Nothing here
 * reports success for something it did not do.
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

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_socket")))
extern int __wasmify_host_sock_socket(int domain, int type);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_connect")))
extern int __wasmify_host_sock_connect(int fd, unsigned int ip_be, int port);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_getaddrinfo")))
extern int __wasmify_host_getaddrinfo(const char *node, int len, unsigned int *out_ip_be);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_bind")))
extern int __wasmify_host_sock_bind(int fd, unsigned int ip_be, int port);

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
#define WASMIFY_SOCKOPT_TYPE 9   /* read only: 1 for a stream socket */

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

/* getnameinfo(): numeric-only — reverse DNS is not supported by this
 * bridge, so the address and port are always rendered numerically (the
 * NI_NUMERICHOST/NI_NUMERICSERV result), which is what callers like
 * IO::Socket libraries use it for here (stringifying peers). */
int getnameinfo(const struct sockaddr *sa, socklen_t salen, char *host,
                socklen_t hostlen, char *serv, socklen_t servlen, int flags) {
    (void)salen; (void)flags;
    if (sa == nullptr || sa->sa_family != AF_INET) return EAI_FAMILY;
    const struct sockaddr_in *in = reinterpret_cast<const struct sockaddr_in *>(sa);
    if (host != nullptr && hostlen > 0) {
        unsigned int ip = ntohl(in->sin_addr.s_addr);
        int n = snprintf(host, hostlen, "%u.%u.%u.%u",
                         (ip >> 24) & 0xff, (ip >> 16) & 0xff,
                         (ip >> 8) & 0xff, ip & 0xff);
        if (n < 0 || (socklen_t)n >= hostlen) return EAI_OVERFLOW;
    }
    if (serv != nullptr && servlen > 0) {
        int n = snprintf(serv, servlen, "%u", (unsigned int)ntohs(in->sin_port));
        if (n < 0 || (socklen_t)n >= servlen) return EAI_OVERFLOW;
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

/* gethostbyaddr(): the host offers no reverse lookup, so no address has a
 * name. */
struct hostent *gethostbyaddr(const void *addr, socklen_t len, int type) {
    (void)addr; (void)len; (void)type;
    return nullptr;
}

/* socket(): allocate a host-managed socket fd. Only a TCP socket over IPv4
 * can be made. Host returns the fd, or a negative errno on failure. */
int socket(int domain, int type, int protocol) {
    if (domain != AF_INET) { errno = EAFNOSUPPORT; return -1; }
    const int flags = type & (SOCK_NONBLOCK | SOCK_CLOEXEC);
    if ((type & ~flags) != SOCK_STREAM || (protocol != 0 && protocol != IPPROTO_TCP)) {
        errno = EPROTONOSUPPORT;
        return -1;
    }
    int r = __wasmify_host_sock_socket(domain, SOCK_STREAM);
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
        }
    } else if (level == IPPROTO_TCP) {
        if (optname == TCP_NODELAY) return WASMIFY_SOCKOPT_NODELAY;
    }
    return 0;
}

int setsockopt(int fd, int level, int optname, const void *optval, socklen_t optlen) {
    const int option = __wasmify_sock_option(level, optname);
    if (option == 0) { errno = ENOPROTOOPT; return -1; }
    if (optval == nullptr) { errno = EFAULT; return -1; }
    int value = 0;
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
    if (option == WASMIFY_SOCKOPT_TYPE) value = SOCK_STREAM; /* the host's 1 */
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

/* sendto() / recvfrom(): every socket here is a stream socket, on which the
 * address argument carries nothing — the peer is the connection's. sendto
 * is send (a destination is ignored, as for any connected stream socket),
 * and recvfrom is recv plus the peer's name. */
ssize_t sendto(int fd, const void *buf, size_t len, int flags,
               const struct sockaddr *addr, socklen_t addrlen) {
    (void)addr; (void)addrlen;
    return send(fd, buf, len, flags);
}

ssize_t recvfrom(int fd, void *buf, size_t len, int flags,
                 struct sockaddr *addr, socklen_t *addrlen) {
    ssize_t n = recv(fd, buf, len, flags);
    if (n >= 0 && addr != nullptr && addrlen != nullptr) {
        struct __wasmify_sock_name name;
        if (__wasmify_host_sock_getpeername(fd, &name) != 0)
            *addrlen = 0;
        else
            __wasmify_fill_sockaddr(&name, addr, addrlen);
    }
    return n;
}

} /* extern "C" */

#endif /* WASMIFY_HOST_SOCKETS */
