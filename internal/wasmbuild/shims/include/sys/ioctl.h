/* sys/ioctl.h of the host capabilities: the C library's, and the requests
 * of a terminal host_tty.cc makes with terminal, in the numbers of Linux on
 * most architectures (asm-generic), with struct winsize. */
#include_next <sys/ioctl.h>

#ifndef _WASMIFY_HOST_SYS_IOCTL_H
#define _WASMIFY_HOST_SYS_IOCTL_H

#ifdef WASMIFY_HOST_TERMINAL
struct winsize {
    unsigned short ws_row;
    unsigned short ws_col;
    unsigned short ws_xpixel;
    unsigned short ws_ypixel;
};

#define TCGETS 0x5401
#define TCSETS 0x5402
#define TCSETSW 0x5403
#define TCSETSF 0x5404
#define TCSBRK 0x5409
#define TCXONC 0x540A
#define TCFLSH 0x540B
#define TIOCEXCL 0x540C
#define TIOCNXCL 0x540D
#define TIOCSCTTY 0x540E
#define TIOCGPGRP 0x540F
#define TIOCSPGRP 0x5410
#define TIOCOUTQ 0x5411
#define TIOCGWINSZ 0x5413
#define TIOCSWINSZ 0x5414
#define TIOCNOTTY 0x5422
#define TIOCGSID 0x5429
#define TCGETS2 0x802C542AU
#define TCSETS2 0x402C542BU
#define TCSETSW2 0x402C542CU
#define TCSETSF2 0x402C542DU
#define TIOCGPTN 0x80045430U
#define TIOCSPTLCK 0x40045431U
#endif

#endif /* _WASMIFY_HOST_SYS_IOCTL_H */
