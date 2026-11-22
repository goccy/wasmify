/* host_tty.cc — terminals, for a C library that has none.
 *
 * A terminal is a device of the host. Its requests are made as a program of
 * Linux makes them, whatever the host is: in the numbers of asm-generic and
 * with the kernel's structures (<sys/ioctl.h>, <termios.h>), of the host's
 * terminal behind the descriptor (fd_ioctl), which the host makes in its
 * own numbers. The termios functions are musl's, on those requests; ioctl
 * takes the requests of a terminal to the host (the link wraps it) and
 * leaves every other request to the C library (FIONREAD, FIONBIO); ttyname
 * reads the link /proc/self/fd/N, as Linux's C libraries do.
 *
 * Compiled when the host capability terminal is given (WASMIFY_HOST_TERMINAL);
 * tcgetpgrp and tcsetpgrp with process.job_control.
 */
#ifdef WASMIFY_HOST_TERMINAL

#include <sys/ioctl.h>
#include <termios.h>
#include <unistd.h>
#include <cerrno>
#include <climits>
#include <cstdarg>
#include <cstddef>
#include <cstdint>
#include <cstdio>
#include <cstring>

#include "wasmify_posix.h"

extern "C" int __real_ioctl(int fd, int request, ...);

namespace {

/* TCGETS and TCSETS read and write the kernel's struct termios, which is
 * the start of this C library's. */
static_assert(offsetof(struct termios, c_line) == 16, "struct termios starts as the kernel's");
static_assert(offsetof(struct termios, c_cc) == 17, "struct termios starts as the kernel's");

int fail(int error) {
    errno = error;
    return -1;
}

/* What a request of a terminal takes: nothing, an int, or an address; not
 * one of a terminal's (other). The requests are those fd_ioctl makes. */
enum class Takes { nothing, value, address, other };

Takes argument_of(unsigned int request) {
    switch (request) {
    case TIOCEXCL:
    case TIOCNXCL:
    case TIOCNOTTY:
        return Takes::nothing;
    case TCSBRK:
    case TCXONC:
    case TCFLSH:
    case TIOCSCTTY:
        return Takes::value;
    case TCGETS:
    case TCSETS:
    case TCSETSW:
    case TCSETSF:
    case TCGETS2:
    case TCSETS2:
    case TCSETSW2:
    case TCSETSF2:
    case TIOCGWINSZ:
    case TIOCSWINSZ:
    case TIOCGPGRP:
    case TIOCSPGRP:
    case TIOCOUTQ:
    case TIOCGSID:
    case TIOCGPTN:
    case TIOCSPTLCK:
        return Takes::address;
    }
    return Takes::other;
}

/* The request of the terminal behind fd: what it returned, or -1 with
 * errno set. */
int terminal_request(int fd, unsigned int request, uintptr_t arg) {
    const int rc = __wasmify_host_fd_ioctl(fd, request, arg);
    return rc < 0 ? fail(-rc) : rc;
}

} /* namespace */

extern "C" {

/* ioctl(2): a request of a terminal reads the argument it takes; any other
 * request is the C library's, with the one argument its ioctl reads. */
int __wrap_ioctl(int fd, int request, ...) {
    va_list ap;
    va_start(ap, request);
    int rc = 0;
    switch (argument_of((unsigned int)request)) {
    case Takes::nothing:
        rc = terminal_request(fd, (unsigned int)request, 0);
        break;
    case Takes::value:
        rc = terminal_request(fd, (unsigned int)request, (uintptr_t)(unsigned int)va_arg(ap, int));
        break;
    case Takes::address:
        rc = terminal_request(fd, (unsigned int)request, (uintptr_t)va_arg(ap, void *));
        break;
    case Takes::other:
        rc = __real_ioctl(fd, request, va_arg(ap, void *));
        break;
    }
    va_end(ap);
    return rc;
}

int tcgetattr(int fd, struct termios *settings) {
    return terminal_request(fd, TCGETS, (uintptr_t)settings) < 0 ? -1 : 0;
}

int tcsetattr(int fd, int actions, const struct termios *settings) {
    if (actions < TCSANOW || actions > TCSAFLUSH) return fail(EINVAL);
    return terminal_request(fd, TCSETS + (unsigned int)actions, (uintptr_t)settings) < 0 ? -1 : 0;
}

/* The speed is the one of c_cflag (CBAUD), for input as for output. */
speed_t cfgetospeed(const struct termios *settings) { return settings->c_cflag & CBAUD; }

speed_t cfgetispeed(const struct termios *settings) { return cfgetospeed(settings); }

int cfsetospeed(struct termios *settings, speed_t speed) {
    if ((speed & ~CBAUD) != 0) return fail(EINVAL);
    settings->c_cflag = (settings->c_cflag & ~CBAUD) | speed;
    return 0;
}

/* An input speed of 0 is the output speed. */
int cfsetispeed(struct termios *settings, speed_t speed) {
    return speed != 0 ? cfsetospeed(settings, speed) : 0;
}

int tcdrain(int fd) { return terminal_request(fd, TCSBRK, 1) < 0 ? -1 : 0; }

int tcflow(int fd, int action) {
    return terminal_request(fd, TCXONC, (uintptr_t)(unsigned int)action) < 0 ? -1 : 0;
}

int tcflush(int fd, int queue) {
    return terminal_request(fd, TCFLSH, (uintptr_t)(unsigned int)queue) < 0 ? -1 : 0;
}

/* A duration other than 0 is the implementation's to define; this one
 * sends the break of 0. */
int tcsendbreak(int fd, int duration) {
    (void)duration;
    return terminal_request(fd, TCSBRK, 0) < 0 ? -1 : 0;
}

/* The name of the terminal is where the descriptor's link leads. */
int ttyname_r(int fd, char *name, size_t size) {
    if (!isatty(fd)) return errno;
    char link[sizeof "/proc/self/fd/" + 3 * sizeof(int)];
    if (snprintf(link, sizeof link, "/proc/self/fd/%d", fd) < 0) return errno;
    const ssize_t len = readlink(link, name, size);
    if (len < 0) return errno;
    if ((size_t)len == size) return ERANGE;
    name[len] = '\0';
    return 0;
}

char *ttyname(int fd) {
    static char name[TTY_NAME_MAX];
    const int error = ttyname_r(fd, name, sizeof name);
    if (error != 0) {
        errno = error;
        return nullptr;
    }
    return name;
}

/* The controlling terminal has one name for every process. */
char *ctermid(char *buf) {
    static char name[L_ctermid];
    if (buf == nullptr) buf = name;
    strcpy(buf, "/dev/tty");
    return buf;
}

#ifdef WASMIFY_HOST_PROCESS_JOB_CONTROL
pid_t tcgetpgrp(int fd) {
    int pgrp = 0;
    if (terminal_request(fd, TIOCGPGRP, (uintptr_t)&pgrp) < 0) return -1;
    return (pid_t)pgrp;
}

int tcsetpgrp(int fd, pid_t pgrp) {
    int group = (int)pgrp;
    return terminal_request(fd, TIOCSPGRP, (uintptr_t)&group) < 0 ? -1 : 0;
}
#endif

} /* extern "C" */

#endif /* WASMIFY_HOST_TERMINAL */
