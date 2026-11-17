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
 * EAFNOSUPPORT, an option outside the set is ENOPROTOOPT. Nothing here
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
#include <cstddef>
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

/* The address families, by the host's own numbering (libc's AF_* numbers
 * differ from libc to libc). */
#define WASMIFY_SOCK_FAMILY_IPV4 1
#define WASMIFY_SOCK_FAMILY_UNIX 2
#define WASMIFY_SOCK_FAMILY_IPV6 3

/* A socket address as it crosses to the host and back, whatever its
 * family: the port in host byte order, the address as it sits in the
 * sockaddr (4 bytes for IPv4, 16 for IPv6), the scope id of an IPv6
 * address, and the name of a Unix-domain socket as the program gave it.
 * family 0 is no address. */
struct __wasmify_sock_addr {
    uint32_t family;
    uint32_t port;
    uint8_t ip[16];
    uint32_t scope;
    uint32_t name_len;
    char name[108];
};

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

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_open")))
extern int __wasmify_host_sock_open(int family, int type);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_connect_addr")))
extern int __wasmify_host_sock_connect(int fd, const struct __wasmify_sock_addr *addr,
                                       const char *path, int path_len);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_bind_addr")))
extern int __wasmify_host_sock_bind(int fd, const struct __wasmify_sock_addr *addr,
                                    const char *path, int path_len);

/* The addresses of a host name, of one family or (family 0) of both, IPv4
 * first: at most max records, *count receives their number. An address
 * written in numbers is converted. -ENOENT when the name has none. */
__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_resolve")))
extern int __wasmify_host_sock_resolve(const char *node, int len, int family,
                                       struct __wasmify_sock_addr *out, int max,
                                       unsigned int *count);

/* gethostbyname's lookup, as the host's gethostbyname does it where the
 * host can (it is not getaddrinfo's: darwin's getaddrinfo keeps a socket
 * open that gethostbyname does not, which every command started afterwards
 * would inherit). The record is the number of IPv4 addresses (u32), the
 * addresses (4 bytes each, as in s_addr), then the official name, not
 * NUL-terminated: *needed receives its size, also when buf is too small
 * (-ERANGE). -ENOENT when the name has no IPv4 address. */
__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_host_by_name")))
extern int __wasmify_host_sock_host_by_name(const char *node, int len, void *buf, int buf_len,
                                            unsigned int *needed);

/* The name of the host with an IP address, not NUL-terminated: *needed
 * receives its length, also when buf is too small (-ERANGE). -ENOENT when
 * the address has no name. */
__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_reverse_addr")))
extern int __wasmify_host_sock_reverse(const struct __wasmify_sock_addr *addr, char *buf,
                                       int buf_len, unsigned int *needed);

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

/* The socket's local name (peer 0) or its peer's. */
__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_name")))
extern int __wasmify_host_sock_name(int fd, int peer, struct __wasmify_sock_addr *out);

/* The address of a record as the host's C library has it in a struct
 * sockaddr (cap bytes of room at out, the length at *len), and the
 * reverse. 0 or a negative errno. */
__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_addr_to_host")))
extern int __wasmify_host_sock_addr_to_host(const struct __wasmify_sock_addr *addr, void *out,
                                            int cap, int *len);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_addr_from_host")))
extern int __wasmify_host_sock_addr_from_host(const void *raw, int len,
                                              struct __wasmify_sock_addr *addr);

/* The flags of a send or a receive the host knows, by the host's own
 * numbering (libc's MSG_ numbers differ from libc to libc). */
#define WASMIFY_MSG_PEEK 1     /* receive: leave it in place */
#define WASMIFY_MSG_WAITALL 2  /* receive: until the buffer is full */
#define WASMIFY_MSG_DONTWAIT 4 /* do not wait, whatever O_NONBLOCK says */
#define WASMIFY_MSG_NOSIGNAL 8 /* send: EPIPE without SIGPIPE */

/* sendto: with addr, a datagram to that address (path as for connect);
 * without, a send to the socket's peer. flags are WASMIFY_MSG_ flags. *sent
 * receives the count. */
__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_sendto_addr")))
extern int __wasmify_host_sock_sendto(int fd, const void *buf, int len, int flags,
                                      const struct __wasmify_sock_addr *addr,
                                      const char *path, int path_len, int *sent);

/* recvfrom: one datagram (or, on a stream socket, what there is) into buf;
 * flags are WASMIFY_MSG_ flags. *from receives the sender's address, with
 * family 0 when there is none. */
__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_recvfrom_addr")))
extern int __wasmify_host_sock_recvfrom(int fd, void *buf, int len, int flags,
                                        struct __wasmify_sock_addr *from, int *received);

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
__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_timeout")))
extern int __wasmify_host_sock_timeout(int fd, int which, int set, int64_t nanos,
                                       int64_t *old_nanos);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_setopt")))
extern int __wasmify_host_sock_setopt(int fd, int option, int value);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_getopt")))
extern int __wasmify_host_sock_getopt(int fd, int option, int *value);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("sock_socketpair")))
extern int __wasmify_host_sock_socketpair(int family, int type, int fds[2]);

/* struct addrinfo, the EAI_* values, and the prototypes this file
 * implements all come from wasmify's <netdb.h> stub header (included
 * above) — the SAME header a consuming libc socket module compiles
 * against, so the struct layout the module reads is the layout built
 * here by construction. */
#define WASMIFY_EAI_FAIL EAI_FAIL
#define WASMIFY_EAI_MEMORY EAI_MEMORY
#define WASMIFY_EAI_NONAME EAI_NONAME

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

/* Reads a sockaddr into the host's form. For a Unix-domain address, path
 * (of path_size bytes) receives the socket's path as the host takes paths
 * — made absolute against the working directory, without the leading '/' —
 * and *path_len its length; otherwise *path_len is 0. Returns 0 or the
 * errno: EAFNOSUPPORT for a family the host has no sockets of, EINVAL for
 * an address shorter than its family needs or a Unix-domain one without a
 * name, ENAMETOOLONG for a path that does not fit. */
static int __wasmify_sock_addr_in(const struct sockaddr *addr, socklen_t addrlen,
                                  struct __wasmify_sock_addr *out, char *path,
                                  size_t path_size, int *path_len) {
    memset(out, 0, sizeof(*out));
    if (path_len != nullptr) *path_len = 0;
    if (addr == nullptr || addrlen < (socklen_t)sizeof(sa_family_t)) return EINVAL;
    switch (addr->sa_family) {
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
                char cwd[3584];
                if (getcwd(cwd, sizeof(cwd)) == nullptr) return errno;
                n = snprintf(path, path_size, "%s/%s", cwd, name);
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
         * address apart: a path is the whole structure, padded with NULs;
         * an abstract name (one that starts with a NUL) has no terminator
         * and ends where the address does; a socket without a name has an
         * address that ends before the path. */
        full = sizeof(sa.un);
        if (len == 0 || in->name[0] == '\0') {
            full = (socklen_t)(offsetof(struct __wasmify_sockaddr_un, sun_path) + len);
        }
        break;
    }
    }
    socklen_t n = *addrlen;
    if (n > full) n = full;
    memcpy(addr, &sa, n);
    *addrlen = full;
}

/* A socket address of this C library as the host's C library has it: the
 * bytes of a struct sockaddr of the host's (cap bytes of room at out, their
 * number at *out_len), which native code of the host's makes and takes. A
 * project whose program is the host process shows its program these, so
 * that the packed addresses it holds are the ones the system and native
 * libraries use. Returns 0 or the errno: EAFNOSUPPORT for a family the host
 * has no sockets of, EINVAL for an address shorter than its family needs,
 * ERANGE where the room is too small. A Unix-domain address without a name
 * (the family alone) is one, as getsockname gives it for an unbound
 * socket. */
int __wasmify_sockaddr_to_host(const struct sockaddr *addr, socklen_t addrlen, void *out,
                               size_t cap, size_t *out_len) {
    struct __wasmify_sock_addr rec;
    if (addr != nullptr && addrlen >= (socklen_t)sizeof(sa_family_t) && addr->sa_family == AF_UNIX &&
        addrlen <= (socklen_t)offsetof(struct __wasmify_sockaddr_un, sun_path)) {
        memset(&rec, 0, sizeof(rec));
        rec.family = WASMIFY_SOCK_FAMILY_UNIX;
    } else {
        const int err = __wasmify_sock_addr_in(addr, addrlen, &rec, nullptr, 0, nullptr);
        if (err != 0) return err;
    }
    int len = 0;
    const int rc = __wasmify_host_sock_addr_to_host(&rec, out, cap > 0x7fffffff ? 0x7fffffff : (int)cap, &len);
    if (rc < 0) return -rc;
    *out_len = (size_t)len;
    return 0;
}

/* The reverse of __wasmify_sockaddr_to_host: the struct sockaddr of the
 * host's in the len bytes at raw as one of this C library, written the way
 * getsockname() writes one (as much as fits in *out_len bytes at out;
 * *out_len becomes the full size). Returns 0 or the errno: EAFNOSUPPORT for
 * bytes that are no address of a family this C library has, EINVAL for
 * bytes too few for their family. */
int __wasmify_sockaddr_from_host(const void *raw, size_t len, struct sockaddr *out,
                                 socklen_t *out_len) {
    struct __wasmify_sock_addr rec;
    const int rc = __wasmify_host_sock_addr_from_host(raw, len > 0x7fffffff ? 0x7fffffff : (int)len, &rec);
    if (rc < 0) return -rc;
    __wasmify_sock_addr_out(&rec, out, out_len);
    return 0;
}

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
    /* the record starts with the port */
    char rec[512];
    unsigned int needed = 0;
    const char *proto = socktype == SOCK_DGRAM ? "udp" : (socktype == SOCK_STREAM ? "tcp" : "");
    if (__wasmify_host_sock_service(service, (int)strlen(service), 0, 0, proto,
                                    (int)strlen(proto), rec, (int)sizeof(rec), &needed) != 0 ||
        needed < 4)
        return EAI_SERVICE;
    unsigned int found = 0;
    memcpy(&found, rec, 4);
    *port = (int)found;
    return 0;
}

/* An IPv4 address in the numbers-and-dots notation inet_aton(3) takes: one
 * to four numbers, each decimal, octal (a leading 0) or hexadecimal (a
 * leading 0x), the last filling the bytes the others leave (a.b.c.d,
 * a.b.c with c 16 bits, a.b with b 24 bits, a alone 32 bits). glibc's
 * getaddrinfo takes a node in that notation as an address, without asking
 * a resolver: "127.1", "2130706433", "0x7f000001" and "127.0.0.01" are all
 * 127.0.0.1. Returns 1 and the address in network byte order, or 0. */
static int __wasmify_inet_aton(const char *cp, struct in_addr *addr) {
    uint32_t parts[4];
    int n = 0;
    for (;;) {
        if (n == 4) return 0;
        int base = 10;
        if (*cp == '0') {
            base = 8;
            cp++;
            if (*cp == 'x' || *cp == 'X') {
                base = 16;
                cp++;
                if (!((*cp >= '0' && *cp <= '9') || (*cp >= 'a' && *cp <= 'f') || (*cp >= 'A' && *cp <= 'F')))
                    return 0;
            } else if (*cp == '\0' || *cp == '.') {
                /* "0" alone */
                parts[n++] = 0;
                if (*cp == '\0') break;
                cp++;
                continue;
            }
        } else if (!(*cp >= '0' && *cp <= '9')) {
            return 0;
        }
        uint64_t value = 0;
        for (;; cp++) {
            int digit;
            if (*cp >= '0' && *cp <= '9') digit = *cp - '0';
            else if (base == 16 && *cp >= 'a' && *cp <= 'f') digit = *cp - 'a' + 10;
            else if (base == 16 && *cp >= 'A' && *cp <= 'F') digit = *cp - 'A' + 10;
            else break;
            if (digit >= base) return 0;
            value = value * base + digit;
            if (value > 0xffffffffu) return 0;
        }
        parts[n++] = (uint32_t)value;
        if (*cp == '\0') break;
        if (*cp != '.') return 0;
        cp++;
    }
    uint32_t ip;
    switch (n) {
    case 1: ip = parts[0]; break;
    case 2:
        if (parts[0] > 0xff || parts[1] > 0xffffff) return 0;
        ip = parts[0] << 24 | parts[1];
        break;
    case 3:
        if (parts[0] > 0xff || parts[1] > 0xff || parts[2] > 0xffff) return 0;
        ip = parts[0] << 24 | parts[1] << 16 | parts[2];
        break;
    default:
        if (parts[0] > 0xff || parts[1] > 0xff || parts[2] > 0xff || parts[3] > 0xff) return 0;
        ip = parts[0] << 24 | parts[1] << 16 | parts[2] << 8 | parts[3];
        break;
    }
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
    struct __wasmify_sock_addr found[16];
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
            char literal[64];
            const char *zone = strchr(node, '%');
            const size_t len = zone != nullptr ? (size_t)(zone - node) : strlen(node);
            unsigned char parsed[16];
            if (len >= sizeof(literal)) return EAI_NONAME;
            memcpy(literal, node, len);
            literal[len] = '\0';
            if (inet_pton(AF_INET, literal, parsed) != 1 && inet_pton(AF_INET6, literal, parsed) != 1)
                return EAI_NONAME;
        }
        const int rc = __wasmify_host_sock_resolve(node, (int)strlen(node), want, found, 16, &count);
        if (rc != 0 || count == 0) return rc == -ENOMEM ? WASMIFY_EAI_MEMORY : WASMIFY_EAI_NONAME;
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
                return WASMIFY_EAI_MEMORY;
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
    if ((flags & AI_CANONNAME) && node != nullptr && head != nullptr) {
        head->ai_canonname = strdup(node);
        if (head->ai_canonname == nullptr) {
            freeaddrinfo(head);
            return WASMIFY_EAI_MEMORY;
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
 * resolver and services database. An address or a port that has no name
 * is given in numbers — unless the caller requires a name (NI_NAMEREQD) —
 * and so are both when the caller asks for numbers (NI_NUMERICHOST,
 * NI_NUMERICSERV). */
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
        }
        if (named != 0) {
            if (flags & NI_NAMEREQD) return EAI_NONAME;
            char text[64];
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

/* gethostbyname(): the host's gethostbyname lookup: the official name and
 * the IPv4 addresses (up to 16) of the name. The result lives in static
 * storage, as the interface has it. */
struct hostent *gethostbyname(const char *name) {
    enum { MAX_ADDRS = 16 };
    static struct hostent ent;
    static struct in_addr addrs[MAX_ADDRS];
    static char *addr_list[MAX_ADDRS + 1];
    static char *no_aliases[1];
    static char official[256];
    /* the record: count, the addresses, the name */
    static unsigned char rec[4 + 4 * MAX_ADDRS + sizeof(official)];
    if (name == nullptr) return nullptr;
    unsigned int needed = 0;
    const int rc = __wasmify_host_sock_host_by_name(name, (int)strlen(name), rec, (int)sizeof(rec),
                                                    &needed);
    /* a record too large holds more addresses or a longer name than are
     * kept here: none is taken rather than part of one */
    if (rc != 0 || needed < 4) return nullptr;
    unsigned int count;
    memcpy(&count, rec, 4);
    if (count == 0 || needed < 4 + 4 * count) return nullptr;
    const unsigned int name_len = needed - 4 - 4 * count;
    if (name_len >= sizeof(official)) return nullptr;
    const unsigned int kept = count < MAX_ADDRS ? count : MAX_ADDRS;
    for (unsigned int i = 0; i < kept; i++) {
        memcpy(&addrs[i], rec + 4 + 4 * i, 4);
        addr_list[i] = reinterpret_cast<char *>(&addrs[i]);
    }
    addr_list[kept] = nullptr;
    memcpy(official, rec + 4 + 4 * count, name_len);
    official[name_len] = '\0';
    no_aliases[0] = nullptr;
    ent.h_name = official;
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
    static char name[256];
    const int family = (type == AF_INET || type == AF_INET6) ? __wasmify_sock_family(type) : 0;
    const socklen_t size = type == AF_INET ? 4 : 16;
    if (addr == nullptr || family == 0 || len < size) return nullptr;
    struct __wasmify_sock_addr rec;
    __wasmify_sock_addr_ip(&rec, family, addr);
    if (__wasmify_reverse(&rec, name, sizeof(name)) != 0) return nullptr;
    memcpy(kept, addr, size);
    addr_list[0] = reinterpret_cast<char *>(kept);
    addr_list[1] = nullptr;
    no_aliases[0] = nullptr;
    ent.h_name = name;
    ent.h_aliases = no_aliases;
    ent.h_addrtype = type;
    ent.h_length = (int)size;
    ent.h_addr_list = addr_list;
    return &ent;
}

/* The host's type for a libc socket type and protocol of a family, or 0
 * for a combination the host has no sockets of. */
static int __wasmify_sock_type(int family, int type, int protocol) {
    const bool ip = family != WASMIFY_SOCK_FAMILY_UNIX;
    if (type == SOCK_STREAM && (protocol == 0 || (ip && protocol == IPPROTO_TCP)))
        return WASMIFY_SOCK_TYPE_STREAM;
    if (type == SOCK_DGRAM && (protocol == 0 || (ip && protocol == IPPROTO_UDP)))
        return WASMIFY_SOCK_TYPE_DGRAM;
    return 0;
}

/* Makes both descriptors non-blocking; on failure they are closed and -1
 * is returned with errno set. count is 1 or 2. */
static int __wasmify_sock_nonblock(int *fds, int count) {
    for (int i = 0; i < count; i++) {
        if (fcntl(fds[i], F_SETFL, O_NONBLOCK) != 0) {
            const int e = errno;
            for (int j = 0; j < count; j++) close(fds[j]);
            errno = e;
            return -1;
        }
    }
    return 0;
}

/* socket(): allocate a host-managed socket fd: a stream or a datagram
 * socket of the family. Host returns the fd, or a negative errno on
 * failure. */
int socket(int domain, int type, int protocol) {
    const int family = __wasmify_sock_family(domain);
    if (family == 0) { errno = EAFNOSUPPORT; return -1; }
    const int flags = type & (SOCK_NONBLOCK | SOCK_CLOEXEC);
    const int host_type = __wasmify_sock_type(family, type & ~flags, protocol);
    if (host_type == 0) { errno = EPROTONOSUPPORT; return -1; }
    int r = __wasmify_host_sock_open(family, host_type);
    if (r < 0) { errno = -r; return -1; }
    if ((flags & SOCK_NONBLOCK) && __wasmify_sock_nonblock(&r, 1) != 0) return -1;
    return r;
}

/* connect(): hand the address to the host, which dials. A connect that
 * has to wait does so on a blocking socket — a signal ends the wait
 * (EINTR), the send timeout too (EINPROGRESS) — and reports EINPROGRESS at
 * once on a non-blocking one; the connection is then made in the
 * background, the socket polls writable when that has ended, and SO_ERROR
 * tells how. */
int connect(int fd, const struct sockaddr *addr, socklen_t addrlen) {
    struct __wasmify_sock_addr to;
    char path[4096];
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
    char path[4096];
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
    if (addr != nullptr && addrlen != nullptr) {
        struct __wasmify_sock_addr name;
        int r = __wasmify_host_sock_name(static_cast<int>(conn), 1, &name);
        if (r != 0) {
            /* the peer went away already: an address of no family */
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
        }
    } else if (level == IPPROTO_TCP) {
        if (optname == TCP_NODELAY) return WASMIFY_SOCKOPT_NODELAY;
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
    if ((flags & SOCK_NONBLOCK) && __wasmify_sock_nonblock(fds, 2) != 0) return -1;
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
    char path[4096];
    int path_len = 0;
    if (addr != nullptr) {
        if (const int e = __wasmify_sock_addr_in(addr, addrlen, &to, path, sizeof(path), &path_len)) {
            errno = e;
            return -1;
        }
    }
    if (len > 0x7fffffff) { errno = EMSGSIZE; return -1; }
    int sent = 0;
    int r = __wasmify_host_sock_sendto(fd, buf, static_cast<int>(len), host_flags,
                                       addr != nullptr ? &to : nullptr, path, path_len, &sent);
    if (r != 0) { errno = (r < 0) ? -r : r; return -1; }
    return sent;
}

ssize_t recvfrom(int fd, void *buf, size_t len, int flags,
                 struct sockaddr *addr, socklen_t *addrlen) {
    const int host_flags = __wasmify_msg_flags(flags, MSG_PEEK | MSG_WAITALL | MSG_DONTWAIT);
    if (host_flags < 0) { errno = EOPNOTSUPP; return -1; }
    if (len > 0x7fffffff) len = 0x7fffffff;
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

} /* extern "C" */

#endif /* WASMIFY_HOST_SOCKETS */
