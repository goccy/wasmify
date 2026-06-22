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
 * is handled. The matching declarations come from wasmify's POSIX-compat
 * headers, so a libc socket module compiled with HAVE_SOCKET/HAVE_CONNECT sees
 * them. */
#ifdef WASMIFY_HOST_SOCKETS

#include <sys/socket.h>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <cerrno>
#include <cstdlib>
#include <cstring>

extern "C" {

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_socket")))
extern int __wasmify_host_sock_socket(int domain, int type);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_connect")))
extern int __wasmify_host_sock_connect(int fd, unsigned int ip_be, int port);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_getaddrinfo")))
extern int __wasmify_host_getaddrinfo(const char *node, int len, unsigned int *out_ip_be);

/* struct addrinfo in the RFC2553 layout (canonname before addr). A consuming
 * libc socket module reads ai_addr/ai_addrlen from the struct this
 * getaddrinfo() allocates, so the field order must match the standard. */
struct addrinfo {
    int ai_flags;
    int ai_family;
    int ai_socktype;
    int ai_protocol;
    size_t ai_addrlen;
    char *ai_canonname;
    struct sockaddr *ai_addr;
    struct addrinfo *ai_next;
};

/* Standard EAI_* error values. */
#define WASMIFY_EAI_FAIL 4
#define WASMIFY_EAI_MEMORY 6
#define WASMIFY_EAI_NONAME 8

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

/* getnameinfo(): reverse lookup is not supported by this bridge. */
int getnameinfo(const struct sockaddr *sa, socklen_t salen, char *host,
                socklen_t hostlen, char *serv, socklen_t servlen, int flags) {
    (void)sa; (void)salen; (void)host; (void)hostlen;
    (void)serv; (void)servlen; (void)flags;
    return WASMIFY_EAI_FAIL;
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
    return 0;
}

} /* extern "C" */

#endif /* WASMIFY_HOST_SOCKETS */
