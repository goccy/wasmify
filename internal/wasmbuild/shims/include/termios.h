/* termios.h — the settings of a terminal, which wasi-libc has no header
 * for.
 *
 * The types and the layout of struct termios are those of Linux on most
 * architectures (asm-generic, and musl's), and so are the numbers below:
 * the bits of the four flag words, the places of the control characters,
 * the speeds, the selectors of tcsetattr, tcflow and tcflush. They are the
 * C library's own numbers.
 *
 * A terminal is a device of the host, and what tcgetattr puts into the
 * structure is what the host has: in the host's numbers. A program that
 * shows its user the host's numbers (the program is the host process, see
 * host_numbering in wasmify_posix.h) reads, changes and hands back the
 * structure in one numbering; the functions (host_tty.cc) pass the
 * structure and the selectors on as they are. The speed of the settings is
 * kept where the host keeps it, which is why cfgetispeed and its three
 * companions ask the host.
 */
#ifndef WASMIFY_TERMIOS_H
#define WASMIFY_TERMIOS_H

#include <sys/types.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef unsigned char cc_t;
typedef unsigned int speed_t;
typedef unsigned int tcflag_t;

#define NCCS 32

struct termios {
    tcflag_t c_iflag;
    tcflag_t c_oflag;
    tcflag_t c_cflag;
    tcflag_t c_lflag;
    cc_t c_line;
    cc_t c_cc[NCCS];
    speed_t __c_ispeed;
    speed_t __c_ospeed;
};

/* c_cc */
#define VINTR 0
#define VQUIT 1
#define VERASE 2
#define VKILL 3
#define VEOF 4
#define VTIME 5
#define VMIN 6
#define VSTART 8
#define VSTOP 9
#define VSUSP 10
#define VEOL 11

/* c_iflag */
#define IGNBRK 0000001
#define BRKINT 0000002
#define IGNPAR 0000004
#define PARMRK 0000010
#define INPCK 0000020
#define ISTRIP 0000040
#define INLCR 0000100
#define IGNCR 0000200
#define ICRNL 0000400
#define IXON 0002000
#define IXOFF 0010000

/* c_oflag */
#define OPOST 0000001

/* c_cflag: the speeds, then the rest */
#define B0 0000000
#define B50 0000001
#define B75 0000002
#define B110 0000003
#define B134 0000004
#define B150 0000005
#define B200 0000006
#define B300 0000007
#define B600 0000010
#define B1200 0000011
#define B1800 0000012
#define B2400 0000013
#define B4800 0000014
#define B9600 0000015
#define B19200 0000016
#define B38400 0000017

#define CSIZE 0000060
#define CS5 0000000
#define CS6 0000020
#define CS7 0000040
#define CS8 0000060
#define CSTOPB 0000100
#define CREAD 0000200
#define PARENB 0000400
#define PARODD 0001000
#define HUPCL 0002000
#define CLOCAL 0004000

/* c_lflag */
#define ISIG 0000001
#define ICANON 0000002
#define ECHO 0000010
#define ECHOE 0000020
#define ECHOK 0000040
#define ECHONL 0000100
#define NOFLSH 0000200
#define TOSTOP 0000400
#define IEXTEN 0100000

/* tcflow */
#define TCOOFF 0
#define TCOON 1
#define TCIOFF 2
#define TCION 3

/* tcflush */
#define TCIFLUSH 0
#define TCOFLUSH 1
#define TCIOFLUSH 2

/* tcsetattr */
#define TCSANOW 0
#define TCSADRAIN 1
#define TCSAFLUSH 2

int tcgetattr(int fd, struct termios *settings);
int tcsetattr(int fd, int actions, const struct termios *settings);
speed_t cfgetispeed(const struct termios *settings);
speed_t cfgetospeed(const struct termios *settings);
int cfsetispeed(struct termios *settings, speed_t speed);
int cfsetospeed(struct termios *settings, speed_t speed);
int tcdrain(int fd);
int tcflow(int fd, int action);
int tcflush(int fd, int queue);
int tcsendbreak(int fd, int duration);

#ifdef __cplusplus
}
#endif

#endif
