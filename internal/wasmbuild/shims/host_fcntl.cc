/* host_fcntl.cc — fcntl for what the host keeps of a descriptor.
 *
 * Compiled when the host capability process.spawn (the close-on-exec flag,
 * which decides what a command inherits, and F_DUPFD) or
 * filesystem.locking (record locks) is given; each request is handled
 * when its capability is.
 */
#if defined(WASMIFY_HOST_PROCESS_SPAWN) || defined(WASMIFY_HOST_FILESYSTEM_LOCKING)

#include <fcntl.h>
#include <unistd.h>
#include <cerrno>
#include <cstdarg>
#include <cstdint>
#include <cstring>

#include "wasmify_posix.h"

extern "C" {

/* A POSIX record lock as fd_lock passes it: type 0 F_RDLCK, 1 F_WRLCK,
 * 2 F_UNLCK; whence 0 SEEK_SET, 1 SEEK_CUR, 2 SEEK_END. */
struct __wasmify_record_lock {
    int32_t type;
    int32_t whence;
    int64_t start;
    int64_t len;
    int32_t pid;
    int32_t pad;
};

extern int __real_fcntl(int fd, int cmd, ...);

/* The C library names the record locks and F_DUPFD where it has them: the
 * wasm64 build of wasi-libc does, wasm32's does not (a program there cannot
 * ask for them, and the requests below are only recognized where they have
 * names). */
#if defined(WASMIFY_HOST_FILESYSTEM_LOCKING) && defined(F_GETLK)
/* fcntl's record locks (F_GETLK, F_SETLK, F_SETLKW) on the host's file
 * behind fd, which wasi-libc refuses (ENOTSUP). */
static int __wasmify_fcntl_lock(int fd, int cmd, struct flock *fl) {
    if (fl == nullptr) { errno = EFAULT; return -1; }
    struct __wasmify_record_lock lk;
    memset(&lk, 0, sizeof(lk));
    switch (fl->l_type) {
    case F_RDLCK: lk.type = 0; break;
    case F_WRLCK: lk.type = 1; break;
    case F_UNLCK: lk.type = 2; break;
    default: errno = EINVAL; return -1;
    }
    switch (fl->l_whence) {
    case SEEK_SET: lk.whence = 0; break;
    case SEEK_CUR: lk.whence = 1; break;
    case SEEK_END: lk.whence = 2; break;
    default: errno = EINVAL; return -1;
    }
    lk.start = fl->l_start;
    lk.len = fl->l_len;
    const int rc = __wasmify_host_fd_lock(fd, cmd == F_GETLK ? 0 : cmd == F_SETLK ? 1 : 2, &lk);
    if (rc != 0) { errno = (rc < 0) ? -rc : rc; return -1; }
    if (cmd == F_GETLK) {
        static const short types[3] = {F_RDLCK, F_WRLCK, F_UNLCK};
        static const short whences[3] = {SEEK_SET, SEEK_CUR, SEEK_END};
        fl->l_type = types[lk.type >= 0 && lk.type <= 2 ? lk.type : 2];
        fl->l_whence = whences[lk.whence >= 0 && lk.whence <= 2 ? lk.whence : 0];
        fl->l_start = lk.start;
        fl->l_len = lk.len;
        fl->l_pid = lk.pid;
    }
    return 0;
}
#endif

/* fcntl: with process.spawn the close-on-exec flag (F_GETFD, F_SETFD) is
 * the host's, which hands a command the descriptors that are not marked
 * under their numbers; F_DUPFD and F_DUPFD_CLOEXEC duplicate the descriptor
 * under the lowest free number from the one given. With filesystem.locking
 * the record locks are the host's. Every other request is libc's. (The link
 * routes fcntl here: -Wl,--wrap=fcntl.) */
int __wrap_fcntl(int fd, int cmd, ...) {
    va_list ap;
    va_start(ap, cmd);
#if defined(WASMIFY_HOST_FILESYSTEM_LOCKING) && defined(F_GETLK)
    if (cmd == F_GETLK || cmd == F_SETLK || cmd == F_SETLKW) {
        struct flock *fl = va_arg(ap, struct flock *);
        va_end(ap);
        return __wasmify_fcntl_lock(fd, cmd, fl);
    }
#endif
    const int arg = va_arg(ap, int);
    va_end(ap);
#if defined(WASMIFY_HOST_PROCESS_SPAWN) && defined(F_DUPFD)
    if (cmd == F_DUPFD || cmd == F_DUPFD_CLOEXEC) {
        if (arg < 0) { errno = EINVAL; return -1; }
        int nfd = -1;
        int rc = __wasmify_host_fd_dupfd(fd, arg, &nfd);
        if (rc != 0) { errno = (rc < 0) ? -rc : rc; return -1; }
        if (cmd == F_DUPFD_CLOEXEC) {
            int old = 0;
            rc = __wasmify_host_fd_cloexec(nfd, 1, 1, &old);
            if (rc != 0) {
                /* The duplicate the caller does not get is closed. The call
                 * fails with the flag's error, which is what the caller
                 * has to know; an error of the close has no way out. */
                (void)close(nfd);
                errno = (rc < 0) ? -rc : rc;
                return -1;
            }
        }
        return nfd;
    }
#endif
#ifdef WASMIFY_HOST_PROCESS_SPAWN
    if (cmd == F_GETFD || cmd == F_SETFD) {
        int old = 0;
        const int rc = __wasmify_host_fd_cloexec(fd, cmd == F_SETFD, (arg & FD_CLOEXEC) != 0, &old);
        /* The host keeps the flag for the descriptors of its table. A
         * preopened directory is none of them (EBADF), and no command
         * inherits it: libc answers for it as for any WASI descriptor, and
         * fails with EBADF itself for a number that names nothing. */
        if (rc == -EBADF) return __real_fcntl(fd, cmd, arg);
        if (rc != 0) { errno = (rc < 0) ? -rc : rc; return -1; }
        return cmd == F_GETFD ? (old ? FD_CLOEXEC : 0) : 0;
    }
#endif
    return __real_fcntl(fd, cmd, arg);
}

} /* extern "C" */

#endif
