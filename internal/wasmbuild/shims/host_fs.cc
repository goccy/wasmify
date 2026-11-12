/* host_fs.cc — generic filesystem-fidelity shim.
 *
 * This translation unit is deployed and compiled by wasmify itself, and only
 * when host filesystem fidelity is opted in (bridge.HostFS in wasmify.json,
 * which makes wasmify define WASMIFY_HOST_FS for this compile and add the
 * matching -Wl,--wrap= flags to every link). Without the opt-in the file is
 * excluded entirely and the wasm stays portable (standard wasi imports only).
 *
 * WASI preview1 has no file modes, owners, creation masks or locks: nothing
 * can change a mode, filestat carries neither permission bits nor an owner,
 * path_open and path_create_directory take no mode argument, and there is
 * no flock. wasi-libc also stores chdir's argument verbatim (".." included)
 * as its cwd, and resolves the empty path to the current directory. Each of
 * these breaks real-world tooling:
 *
 *   - chmod() and fchmod() fail outright (wasi-libc's are ENOSYS stubs);
 *   - stat()/lstat()/fstat() report st_mode with the file type only and no
 *     owner — and the type is wrong for what WASI has no type for: a FIFO or
 *     a pipe comes out as a socket or as nothing — so code that round-trips
 *     modes (chmod((stat(f)).st_mode | 0111)
 *     to mark a file executable) strips every permission, and code that
 *     compares fstat() with stat() to make sure a handle and a name are the
 *     same file is told they differ as soon as only one of them is fixed;
 *   - open(path, O_CREAT, mode) and mkdir(path, mode) drop mode, and there
 *     is no umask(), so a private file or directory cannot be created;
 *   - flock() is missing, so cooperating processes cannot exclude each
 *     other;
 *   - futimes() is missing although futimens() exists;
 *   - after `chdir("b"); chdir("..")` the cwd string keeps a live reference
 *     through b, and deleting b then breaks every cwd-relative operation
 *     (a recursive-delete's chdir-down/up dance does exactly this);
 *   - open(""), stat(""), opendir("") and the rest succeed on the current
 *     directory, where POSIX path resolution fails with ENOENT, so code
 *     that relies on a missing name being an error carries on with a
 *     directory handle.
 *
 * The wrappers below fix each: chmod/fchmod go to the host via the
 * path_chmod/fd_chmod imports; stat/lstat/fstat merge the real permission
 * bits and owner from path_filestat_ext/fd_filestat_ext into the libc
 * result; a creating open and mkdir hand their mode to the host through
 * path_open_mode/path_create_directory_mode, where the process's umask —
 * kept by the host, set through proc_umask — is taken out of it; flock is
 * fd_flock; and chdir collapses "." and ".." lexically before wasi-libc
 * records it (wasi-libc's own resolution is lexical, so nothing is lost).
 * Paths cross the imports preopen-relative, like path_open's. The empty
 * path is refused where every libc path function resolves its argument
 * (__wasilibc_find_relpath_alloc), and in the wrappers here that resolve a
 * path themselves. */
#ifdef WASMIFY_HOST_FS

#include <fcntl.h>
#include <sys/file.h>
#include <sys/stat.h>
#include <sys/time.h>
#include <sys/types.h>
#include <unistd.h>
#include <cerrno>
#include <cstdarg>
#include <cstdio>
#include <cstring>

extern "C" {

__attribute__((import_module("wasi_snapshot_preview1"), import_name("path_chmod")))
extern int __wasmify_host_path_chmod(const char *path, int len, unsigned int mode);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("fd_chmod")))
extern int __wasmify_host_fd_chmod(int fd, unsigned int mode);

/* What the host adds to a WASI filestat: the POSIX mode — the file type in
 * the bits every Unix uses for it (0170000) and the low twelve mode bits —
 * and the owner. */
struct __wasmify_filestat_ext {
    unsigned int mode;
    unsigned int uid;
    unsigned int gid;
};

__attribute__((import_module("wasi_snapshot_preview1"), import_name("path_filestat_ext")))
extern int __wasmify_host_path_filestat_ext(const char *path, int len, int follow,
                                            struct __wasmify_filestat_ext *out);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("fd_filestat_ext")))
extern int __wasmify_host_fd_filestat_ext(int fd, struct __wasmify_filestat_ext *out);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("proc_umask")))
extern int __wasmify_host_proc_umask(unsigned int mask);

/* path_open for a preopen-relative path, plus the mode of open(2). */
__attribute__((import_module("wasi_snapshot_preview1"), import_name("path_open_mode")))
extern int __wasmify_host_path_open_mode(int lookup_flags, const char *path, int len,
                                         int oflags, unsigned long long rights_base,
                                         int fdflags, unsigned int mode, int *fd_out);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("path_create_directory_mode")))
extern int __wasmify_host_path_create_directory_mode(const char *path, int len,
                                                     unsigned int mode);

/* Reports the working directory the guest has changed to (an absolute
 * path): the host process follows when the guest is it. */
__attribute__((import_module("wasi_snapshot_preview1"), import_name("proc_chdir")))
extern int __wasmify_host_proc_chdir(const char *path, int len);

/* lock: 0 unlock, 1 shared, 2 exclusive. wait: non-zero to block. */
__attribute__((import_module("wasi_snapshot_preview1"), import_name("fd_flock")))
extern int __wasmify_host_fd_flock(int fd, int lock, int wait);

extern int __real_chmod(const char *path, mode_t mode);
extern int __real_fchmod(int fd, mode_t mode);
extern int __real_stat(const char *path, struct stat *st);
extern int __real_lstat(const char *path, struct stat *st);
extern int __real_fstat(int fd, struct stat *st);
extern int __real_open(const char *path, int oflag, ...);
extern int __real_mkdir(const char *path, mode_t mode);
extern int __real_chdir(const char *path);
extern int __real___wasilibc_find_relpath_alloc(const char *path, const char **abs_prefix,
                                                char **relative, size_t *relative_len,
                                                int can_realloc);

/* Every import above returns 0 or a negative errno. */
static int __wasmify_fs_result(int rc) {
    if (rc != 0) {
        errno = (rc < 0) ? -rc : rc;
        return -1;
    }
    return 0;
}

/* Reports whether a path argument names nothing at all, with errno set:
 * a null pointer (EFAULT), or the empty string (ENOENT) — which POSIX path
 * resolution never takes for the current directory. */
static int __wasmify_fs_no_path(const char *path) {
    if (path == nullptr) {
        errno = EFAULT;
        return 1;
    }
    if (path[0] == '\0') {
        errno = ENOENT;
        return 1;
    }
    return 0;
}

/* The one place wasi-libc turns a path into a preopened directory and a
 * path relative to it: open, stat, opendir, unlink, rename, ... all come
 * through here. Its own code makes the empty path the current directory. */
int __wrap___wasilibc_find_relpath_alloc(const char *path, const char **abs_prefix,
                                         char **relative, size_t *relative_len,
                                         int can_realloc) {
    if (__wasmify_fs_no_path(path)) return -1;
    return __real___wasilibc_find_relpath_alloc(path, abs_prefix, relative, relative_len,
                                                can_realloc);
}

/* Absolutize against the guest cwd and strip the leading '/' so the path
 * crosses the imports preopen-relative. Returns path or buf. The callers
 * have refused the empty path, which would come out as the cwd itself. */
static const char *__wasmify_fs_import_path(const char *path, char *buf,
                                            size_t bufsz) {
    const char *p = path;
    if (p[0] != '/') {
        char cwd[3584];
        if (getcwd(cwd, sizeof(cwd)) != nullptr) {
            snprintf(buf, bufsz, "%s/%s", cwd, path);
            p = buf;
        }
    }
    while (*p == '/') p++;
    return p;
}

int __wrap_chmod(const char *path, mode_t mode) {
    if (__wasmify_fs_no_path(path)) return -1;
    char buf[4096];
    const char *p = __wasmify_fs_import_path(path, buf, sizeof(buf));
    return __wasmify_fs_result(
        __wasmify_host_path_chmod(p, (int)strlen(p), (unsigned int)mode));
}

int __wrap_fchmod(int fd, mode_t mode) {
    return __wasmify_fs_result(__wasmify_host_fd_chmod(fd, (unsigned int)mode));
}

/* The libc's constant for the file type in a host mode, or 0 when the host
 * named none (the type WASI reported then stands). */
static mode_t __wasmify_stat_type(unsigned int host_mode) {
    switch (host_mode & 0170000) {
    case 0010000: return S_IFIFO;
    case 0020000: return S_IFCHR;
    case 0040000: return S_IFDIR;
    case 0060000: return S_IFBLK;
    case 0100000: return S_IFREG;
    case 0120000: return S_IFLNK;
    case 0140000: return S_IFSOCK;
    }
    return 0;
}

/* Complete a libc stat result with what the host knows beyond WASI. */
static void __wasmify_stat_merge(struct stat *st,
                                 const struct __wasmify_filestat_ext *ext) {
    mode_t type = __wasmify_stat_type(ext->mode);
    if (type == 0) type = st->st_mode & S_IFMT;
    st->st_mode = type | (mode_t)(ext->mode & 07777);
    st->st_uid = (uid_t)ext->uid;
    st->st_gid = (gid_t)ext->gid;
}

static int __wasmify_stat_common(const char *path, struct stat *st, int follow) {
    int rc = follow ? __real_stat(path, st) : __real_lstat(path, st);
    if (rc != 0) return rc;
    char buf[4096];
    const char *p = __wasmify_fs_import_path(path, buf, sizeof(buf));
    struct __wasmify_filestat_ext ext;
    if (__wasmify_host_path_filestat_ext(p, (int)strlen(p), follow, &ext) == 0)
        __wasmify_stat_merge(st, &ext);
    return 0;
}

/* The host describes every descriptor, standard streams, pipes and sockets
 * included; should it have nothing to say, the libc result stands. */
int __wrap_fstat(int fd, struct stat *st) {
    int rc = __real_fstat(fd, st);
    if (rc != 0) return rc;
    struct __wasmify_filestat_ext ext;
    if (__wasmify_host_fd_filestat_ext(fd, &ext) == 0)
        __wasmify_stat_merge(st, &ext);
    return 0;
}

/* umask(), flock() and futimes() do not exist in wasi-libc, so they are
 * defined rather than wrapped — weakly, so that a project which already
 * brings its own definition keeps it. */
__attribute__((weak)) mode_t umask(mode_t mask) {
    return (mode_t)__wasmify_host_proc_umask((unsigned int)mask);
}

__attribute__((weak)) int flock(int fd, int operation) {
    int lock;
    switch (operation & ~LOCK_NB) {
    case LOCK_SH: lock = 1; break;
    case LOCK_EX: lock = 2; break;
    case LOCK_UN: lock = 0; break;
    default:
        errno = EINVAL;
        return -1;
    }
    return __wasmify_fs_result(
        __wasmify_host_fd_flock(fd, lock, (operation & LOCK_NB) == 0));
}

/* futimes() is futimens() at microsecond resolution. */
__attribute__((weak)) int futimes(int fd, const struct timeval tv[2]) {
    if (tv == nullptr) return futimens(fd, nullptr);
    struct timespec ts[2];
    for (int i = 0; i < 2; i++) {
        ts[i].tv_sec = tv[i].tv_sec;
        ts[i].tv_nsec = (long)tv[i].tv_usec * 1000;
    }
    return futimens(fd, ts);
}

/* open(): only a creating open has a mode to deliver; every other goes to
 * wasi-libc unchanged. The flag translation is wasi-libc's own (its O_*
 * values are the WASI oflags and fdflags, shifted into one word): the host
 * needs the read/write rights, the lookup flag, and both flag sets. */
int __wrap_open(const char *path, int oflag, ...) {
    /* An open without any access mode is a read-only open. Where O_RDONLY
     * is 0 — every common system — that is simply what such flags say, and
     * programs written there pass a literal 0 for "read only". wasi-libc's
     * O_RDONLY is a bit of its own, and its open rejects flags that have
     * none of the access bits (EINVAL). */
    if ((oflag & O_ACCMODE) == 0) oflag |= O_RDONLY;
    if ((oflag & O_CREAT) == 0) return __real_open(path, oflag);
    if (__wasmify_fs_no_path(path)) return -1;
    va_list ap;
    va_start(ap, oflag);
    mode_t mode = va_arg(ap, mode_t);
    va_end(ap);
    unsigned long long rights = 0;
    if ((oflag & O_RDONLY) != 0) rights |= 1ULL << 1; /* FD_READ */
    if ((oflag & O_WRONLY) != 0) rights |= 1ULL << 6; /* FD_WRITE */
    int lookup_flags = (oflag & O_NOFOLLOW) == 0 ? 1 : 0; /* SYMLINK_FOLLOW */
    char buf[4096];
    const char *p = __wasmify_fs_import_path(path, buf, sizeof(buf));
    int fd = -1;
    if (__wasmify_fs_result(__wasmify_host_path_open_mode(
            lookup_flags, p, (int)strlen(p), (oflag >> 12) & 0xfff, rights,
            oflag & 0xfff, (unsigned int)mode, &fd)) != 0)
        return -1;
    return fd;
}

int __wrap_mkdir(const char *path, mode_t mode) {
    if (__wasmify_fs_no_path(path)) return -1;
    char buf[4096];
    const char *p = __wasmify_fs_import_path(path, buf, sizeof(buf));
    return __wasmify_fs_result(__wasmify_host_path_create_directory_mode(
        p, (int)strlen(p), (unsigned int)mode));
}

int __wrap_stat(const char *path, struct stat *st) {
    return __wasmify_stat_common(path, st, 1);
}

int __wrap_lstat(const char *path, struct stat *st) {
    return __wasmify_stat_common(path, st, 0);
}

int __wrap_chdir(const char *path) {
    if (__wasmify_fs_no_path(path)) return -1;
    char joined[4096];
    if (path[0] != '/') {
        char cwd[3584];
        if (getcwd(cwd, sizeof(cwd)) == nullptr)
            return __real_chdir(path);
        if (snprintf(joined, sizeof(joined), "%s/%s", cwd, path) >= (int)sizeof(joined)) {
            errno = ENAMETOOLONG;
            return -1;
        }
    } else {
        if (snprintf(joined, sizeof(joined), "%s", path) >= (int)sizeof(joined)) {
            errno = ENAMETOOLONG;
            return -1;
        }
    }
    const char *comps[256];
    size_t lens[256];
    int n = 0;
    const char *p = joined;
    while (*p) {
        while (*p == '/') p++;
        if (!*p) break;
        const char *s = p;
        while (*p && *p != '/') p++;
        size_t l = (size_t)(p - s);
        if (l == 1 && s[0] == '.') continue;
        if (l == 2 && s[0] == '.' && s[1] == '.') {
            if (n > 0) n--;
            continue;
        }
        if (n >= (int)(sizeof(comps) / sizeof(comps[0]))) {
            errno = ENAMETOOLONG;
            return -1;
        }
        comps[n] = s;
        lens[n] = l;
        n++;
    }
    char out[4096];
    size_t o = 0;
    for (int i = 0; i < n; i++) {
        if (o + 1 + lens[i] + 1 >= sizeof(out)) {
            errno = ENAMETOOLONG;
            return -1;
        }
        out[o++] = '/';
        memcpy(out + o, comps[i], lens[i]);
        o += lens[i];
    }
    if (o == 0) out[o++] = '/';
    out[o] = 0;
    const int rc = __real_chdir(out);
    /* The working directory is libc's to keep; the host is told where the
     * guest now is. What it makes of that does not change the outcome. */
    if (rc == 0) (void)__wasmify_host_proc_chdir(out, (int)o);
    return rc;
}

} /* extern "C" */

#endif /* WASMIFY_HOST_FS */
