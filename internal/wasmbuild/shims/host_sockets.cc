/* host_sockets.cc — generic host-provided outbound socket shim.
 *
 * This translation unit is deployed and compiled by wasmify itself, and only
 * when host socket support is opted in (bridge.HostSockets in wasmify.json,
 * which makes wasmify define WASMIFY_HOST_SOCKETS for this compile). Without
 * that opt-in the file is excluded entirely and the wasm imports only standard
 * wasi, staying portable to any wasi runtime.
 *
 * WASI preview1 has no way to create or connect an outbound socket, and
 * wasi-libc under __wasip1__ does not even declare or define socket()/connect()
 * (the prototypes are guarded out). So we DEFINE them here, backed by host
 * imports the WASI host implements in Go (e.g. via net.Dial). The resulting fd
 * is host-managed; send()/recv()/close() keep flowing through the standard wasi
 * sock_send/sock_recv/fd_close path the host already implements. Only IPv4 TCP
 * is handled. The matching declarations come from wasmify's <netdb.h> stub,
 * deployed into the build-local host-include dir that every wasm-build compile
 * gets on its -I whenever host sockets are opted in (independent of the
 * POSIX-compat overlay), so a libc socket module compiled with
 * HAVE_SOCKET/HAVE_CONNECT sees the same struct addrinfo layout this file
 * fills in. */
#ifdef WASMIFY_HOST_SOCKETS

#include <sys/socket.h>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <netdb.h>
#include <cerrno>
#include <cstdio>
#include <cstdlib>
#include <cstring>

extern "C" {

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_socket")))
extern int __wasmify_host_sock_socket(int domain, int type);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_connect")))
extern int __wasmify_host_sock_connect(int fd, unsigned int ip_be, int port);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_getaddrinfo")))
extern int __wasmify_host_getaddrinfo(const char *node, int len, unsigned int *out_ip_be);

/* struct addrinfo, the EAI_* values, and the prototypes this file
 * implements all come from wasmify's <netdb.h> stub header (included
 * above) — the SAME header a consuming libc socket module compiles
 * against, so the struct layout the module reads is the layout built
 * here by construction. */
#define WASMIFY_EAI_FAIL EAI_FAIL
#define WASMIFY_EAI_MEMORY EAI_MEMORY
#define WASMIFY_EAI_NONAME EAI_NONAME

/* getaddrinfo(): resolve node via the host (numeric IPs pass straight
 * through), returning a single IPv4 result. The port comes from a numeric
 * service string; named services are not resolved (port 0). */
int getaddrinfo(const char *node, const char *service,
                const struct addrinfo *hints, struct addrinfo **res) {
    unsigned int ip_be = 0;
    int rc = __wasmify_host_getaddrinfo(node, node ? (int)strlen(node) : 0, &ip_be);
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

/* socket(): allocate a host-managed socket fd. Host returns the fd, or a
 * negative errno on failure. */
int socket(int domain, int type, int protocol) {
    (void)protocol;
    if (domain != AF_INET) { errno = EAFNOSUPPORT; return -1; }
    int r = __wasmify_host_sock_socket(domain, type);
    if (r < 0) { errno = -r; return -1; }
    return r;
}

/* Peer bookkeeping for getpeername(): connect() records the address each
 * fd dialed, so getpeername() answers from this table without a host
 * round-trip. Host socket fds are allocated monotonically and never
 * reused, so a stale entry for a closed fd can never alias a live one;
 * when the ring is full the oldest entry is evicted. */
#define WASMIFY_SOCK_PEER_MAX 64
static struct { int fd; struct sockaddr_in peer; } __wasmify_peers[WASMIFY_SOCK_PEER_MAX];
static int __wasmify_peer_next = 0;
static int __wasmify_peer_used = 0;

/* connect(): parse the IPv4 sockaddr and ask the host to dial. sin_addr.s_addr
 * is already network byte order (host decodes it); sin_port is network order,
 * converted to host order here. */
int connect(int fd, const struct sockaddr *addr, socklen_t addrlen) {
    (void)addrlen;
    if (addr == nullptr || addr->sa_family != AF_INET) {
        errno = EAFNOSUPPORT;
        return -1;
    }
    const struct sockaddr_in *in = reinterpret_cast<const struct sockaddr_in *>(addr);
    int r = __wasmify_host_sock_connect(fd,
                                        static_cast<unsigned int>(in->sin_addr.s_addr),
                                        static_cast<int>(ntohs(in->sin_port)));
    if (r != 0) { errno = (r < 0) ? -r : r; return -1; }
    __wasmify_peers[__wasmify_peer_next].fd = fd;
    __wasmify_peers[__wasmify_peer_next].peer = *in;
    __wasmify_peer_next = (__wasmify_peer_next + 1) % WASMIFY_SOCK_PEER_MAX;
    if (__wasmify_peer_used < WASMIFY_SOCK_PEER_MAX) __wasmify_peer_used++;
    return 0;
}

/* getpeername(): the address connect() dialed for this fd. */
int getpeername(int fd, struct sockaddr *addr, socklen_t *addrlen) {
    if (addr == nullptr || addrlen == nullptr) { errno = EFAULT; return -1; }
    for (int i = 0; i < __wasmify_peer_used; i++) {
        if (__wasmify_peers[i].fd != fd) continue;
        socklen_t n = *addrlen;
        if (n > (socklen_t)sizeof(struct sockaddr_in)) n = sizeof(struct sockaddr_in);
        memcpy(addr, &__wasmify_peers[i].peer, n);
        *addrlen = sizeof(struct sockaddr_in);
        return 0;
    }
    errno = ENOTCONN;
    return -1;
}

/* getsockname(): the host does not expose the local endpoint of its dialed
 * connections, so report the IPv4 unspecified address. Callers here use
 * this only to stringify the local end. */
int getsockname(int fd, struct sockaddr *addr, socklen_t *addrlen) {
    (void)fd;
    if (addr == nullptr || addrlen == nullptr) { errno = EFAULT; return -1; }
    struct sockaddr_in in;
    memset(&in, 0, sizeof(in));
    in.sin_family = AF_INET;
    socklen_t n = *addrlen;
    if (n > (socklen_t)sizeof(in)) n = sizeof(in);
    memcpy(addr, &in, n);
    *addrlen = sizeof(in);
    return 0;
}

} /* extern "C" */

#endif /* WASMIFY_HOST_SOCKETS */
