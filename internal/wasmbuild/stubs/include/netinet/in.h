/*
 * POSIX netinet/in.h compatibility overlay for wasip1.
 *
 * Note: struct in6_addr is fixed via __struct_in6_addr.h replacement.
 * sockaddr_in6 is already provided by WASI's __struct_sockaddr_in6.h.
 */
#include_next <netinet/in.h>

#ifndef _WASMIFY_NETINET_IN_OVERLAY_H
#define _WASMIFY_NETINET_IN_OVERLAY_H

/* IP protocol constants that may be missing */
#ifndef IPPROTO_IPV6
#define IPPROTO_IPV6 41
#endif

#ifndef IPPROTO_ICMPV6
#define IPPROTO_ICMPV6 58
#endif

#ifndef IPPROTO_RAW
#define IPPROTO_RAW 255
#endif

/* IN6ADDR macros */
#ifndef IN6ADDR_ANY_INIT
#define IN6ADDR_ANY_INIT {{0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0}}
#endif
#ifndef IN6ADDR_LOOPBACK_INIT
#define IN6ADDR_LOOPBACK_INIT {{0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,1}}
#endif

#endif /* _WASMIFY_NETINET_IN_OVERLAY_H */
