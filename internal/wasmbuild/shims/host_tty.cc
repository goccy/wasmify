/* host_tty.cc — terminals, for a C library that has none.
 *
 * Compiled when host subprocess support is opted in (WASMIFY_HOST_SUBPROCESS).
 * tcgetattr and its companions, ttyname and ctermid, on the tty_* imports.
 */
#ifdef WASMIFY_HOST_SUBPROCESS

#include <cerrno>
#include <cstddef>
#include <cstdio>
#include <cstring>
#include <termios.h>
#include <unistd.h>

#include "wasmify_posix.h"

namespace {

int fail(int error) {
    errno = error;
    return -1;
}

} /* namespace */

extern "C" {

/* ---- terminals ----------------------------------------------------------------
 * A terminal is a device of the host: its name, its settings and what is
 * done to it are the host's. The structure the settings are kept in is this
 * C library's (<termios.h>); what is in it, and the selectors
 * the functions take, are in the host's numbers and are passed on as they
 * are. A descriptor that is not one of the host's is no terminal here. */

/* The layout the host reads and writes. */
static_assert(sizeof(struct termios) == 60, "struct termios is what the host fills");
static_assert(offsetof(struct termios, c_cc) == 17, "struct termios is what the host fills");
static_assert(offsetof(struct termios, __c_ispeed) == 52, "struct termios is what the host fills");

char *ttyname(int fd) {
    static char name[256];
    int len = 0;
    const int rc = __wasmify_host_tty_name(fd, name, (int)sizeof(name) - 1, &len);
    if (rc != 0) {
        /* a stream the host handed over that is a terminal has no name */
        errno = rc == -ENOTTY && isatty(fd) ? ENODEV : -rc;
        return NULL;
    }
    name[len] = '\0';
    return name;
}

/* The controlling terminal has one name for every process. */
char *ctermid(char *buf) {
    static char name[L_ctermid];
    if (!buf) buf = name;
    strcpy(buf, "/dev/tty");
    return buf;
}

int tcgetattr(int fd, struct termios *settings) {
    const int rc = __wasmify_host_tty_getattr(fd, settings);
    return rc != 0 ? fail(-rc) : 0;
}

int tcsetattr(int fd, int actions, const struct termios *settings) {
    const int rc = __wasmify_host_tty_setattr(fd, actions, settings);
    return rc != 0 ? fail(-rc) : 0;
}

/* Where the speed is kept in the settings is the host's own. */
static speed_t terminal_speed(const struct termios *settings, int output) {
    unsigned int speed = 0;
    struct termios copy = *settings;
    __wasmify_host_tty_speed(&copy, output, 0, 0, &speed);
    return (speed_t)speed;
}

static int set_terminal_speed(struct termios *settings, int output, speed_t speed) {
    unsigned int was = 0;
    const int rc = __wasmify_host_tty_speed(settings, output, 1, (unsigned int)speed, &was);
    return rc != 0 ? fail(-rc) : 0;
}

speed_t cfgetispeed(const struct termios *settings) { return terminal_speed(settings, 0); }
speed_t cfgetospeed(const struct termios *settings) { return terminal_speed(settings, 1); }
int cfsetispeed(struct termios *settings, speed_t speed) { return set_terminal_speed(settings, 0, speed); }
int cfsetospeed(struct termios *settings, speed_t speed) { return set_terminal_speed(settings, 1, speed); }

static int terminal_control(int fd, int what, int arg) {
    const int rc = __wasmify_host_tty_control(fd, what, arg);
    return rc != 0 ? fail(-rc) : 0;
}

int tcdrain(int fd) { return terminal_control(fd, 0, 0); }
int tcflow(int fd, int action) { return terminal_control(fd, 1, action); }
int tcflush(int fd, int queue) { return terminal_control(fd, 2, queue); }
int tcsendbreak(int fd, int duration) { return terminal_control(fd, 3, duration); }

pid_t tcgetpgrp(int fd) {
    int pgrp = 0;
    const int rc = __wasmify_host_tty_pgrp(fd, 0, 0, &pgrp);
    return rc != 0 ? (pid_t)fail(-rc) : (pid_t)pgrp;
}

int tcsetpgrp(int fd, pid_t pgrp) {
    int was = 0;
    const int rc = __wasmify_host_tty_pgrp(fd, 1, (int)pgrp, &was);
    return rc != 0 ? fail(-rc) : 0;
}

} /* extern "C" */

#endif /* WASMIFY_HOST_SUBPROCESS */
