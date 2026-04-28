/*
 * Replacement for wasi-sdk's __struct_in6_addr.h
 *
 * The original WASI version defines in6_addr with only s6_addr (uint8 array).
 * This version provides the full POSIX union with s6_addr, s6_addr16, s6_addr32
 * so code that uses addr.s6_addr32[i] compiles correctly.
 */
#ifndef __wasilibc___struct_in6_addr_h
#define __wasilibc___struct_in6_addr_h

#include <stdint.h>

struct in6_addr {
    union {
        uint8_t  __s6_addr[16];
        uint16_t __s6_addr16[8];
        uint32_t __s6_addr32[4];
    } __in6_union;
};
#define s6_addr   __in6_union.__s6_addr
#define s6_addr16 __in6_union.__s6_addr16
#define s6_addr32 __in6_union.__s6_addr32

#endif
