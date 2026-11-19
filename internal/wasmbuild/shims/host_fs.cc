/* host_fs.cc — generic filesystem-fidelity shim.
 *
 * This translation unit is deployed and compiled by wasmify itself, and only
 * when the host capability filesystem is given (wasmify.json's "host",
 * which makes wasmify define WASMIFY_HOST_FILESYSTEM for this compile and
 * add the matching -Wl,--wrap= flags to every link). Without it the file is
 * excluded entirely and the wasm stays portable (standard wasi imports only).
 * The file system itself brings the working directory as the kernel keeps
 * it, the refusal of the empty path, fchdir, chroot and mkstemp; the modes,
 * owners, umask and timestamps come with filesystem.attributes, flock with
 * filesystem.locking (the record locks of fcntl are host_fcntl.cc's), and
 * mkfifo with filesystem.fifo.
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
 * fd_flock; and chdir finds the directory as the kernel does, following
 * each symbolic link on the way and taking ".." from what it reached,
 * before wasi-libc records it (wasi-libc keeps the path as written, so
 * that getcwd named a directory reached through a link by the link, and
 * ".." after one led to the link's parent).
 * Paths cross the imports preopen-relative, like path_open's. The empty
 * path is refused where every libc path function resolves its argument
 * (__wasilibc_find_relpath_alloc), and in the wrappers here that resolve a
 * path themselves. */
#ifdef WASMIFY_HOST_FILESYSTEM

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

#include "wasmify_posix.h"

extern "C" {

#ifdef WASMIFY_HOST_FILESYSTEM_ATTRIBUTES
/* What the host adds to a WASI filestat: the POSIX mode — the file type in
 * the bits every Unix uses for it (0170000) and the low twelve mode bits —
 * the owner, the preferred block size for I/O (0 when the host names none),
 * the device a special file stands for, and the number of 512-byte blocks
 * allocated to the file. */
struct __wasmify_filestat_ext {
    unsigned int mode;
    unsigned int uid;
    unsigned int gid;
    unsigned int blksize;
    unsigned long long rdev;
    unsigned long long blocks;
};
#endif

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

/* Every import returns 0 or a negative errno (wasmify_posix.h). */
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

/* The number the preopened root directory is named by in every call that
 * resolves a path against it. WASI has a libc find its preopened
 * directories from descriptor 3 up, and the host has the root there; but 3
 * is the number a program's first own descriptor gets, where a program's
 * descriptors are an operating system's (a guest that is the host process).
 * The host knows the root under this second number as well, one no
 * descriptor ever has, and keeps 3 out of it when the path calls and the
 * questions libc asks about the directory (fd_fdstat_get in openat and
 * faccessat) come under this one. */
#define WASMIFY_ROOT_FD 0x7ffffff0
#define WASMIFY_PREOPEN_FD 3

/* The one place wasi-libc turns a path into a preopened directory and a
 * path relative to it: open, stat, opendir, unlink, rename, ... all come
 * through here. Its own code makes the empty path the current directory. */
int __wrap___wasilibc_find_relpath_alloc(const char *path, const char **abs_prefix,
                                         char **relative, size_t *relative_len,
                                         int can_realloc) {
    if (__wasmify_fs_no_path(path)) return -1;
    int fd = __real___wasilibc_find_relpath_alloc(path, abs_prefix, relative, relative_len,
                                                  can_realloc);
    return fd == WASMIFY_PREOPEN_FD ? WASMIFY_ROOT_FD : fd;
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

#ifdef WASMIFY_HOST_FILESYSTEM_ATTRIBUTES
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
#endif

/* access(2) and faccessat(2) ask the host (path_access), which checks the
 * file's mode, owner and access control lists for the process's ids, as
 * the host's own access(2) does. libc's faccessat looks at the rights of
 * the preopened directory alone, which every file under it shares, and
 * never at X_OK: it said a file of mode 0600 could be run and one of mode
 * 0400 written, which perl's `use filetest 'access'` (-r, -w and -x then
 * ask access(2)) reported. The import's flags bit 0 asks with the effective
 * ids, as AT_EACCESS does. A path relative to a directory descriptor other
 * than the working directory's, and a host that cannot say (ENOSYS), are
 * left to libc. */
static const int WASMIFY_ACCESS_EFFECTIVE = 1;

int __real_access(const char *path, int amode);
int __real_faccessat(int fd, const char *path, int amode, int flag);

static int __wasmify_fs_access(const char *path, int amode, int effective) {
    char buf[4096];
    const char *p = __wasmify_fs_import_path(path, buf, sizeof(buf));
    return __wasmify_host_path_access(p, (int)strlen(p), amode,
                                      effective ? WASMIFY_ACCESS_EFFECTIVE : 0);
}

#ifdef WASMIFY_HOST_FILESYSTEM_ATTRIBUTES
int __wrap_access(const char *path, int amode) {
    if (__wasmify_fs_no_path(path)) return -1;
    int rc = __wasmify_fs_access(path, amode, 0);
    if (rc == -ENOSYS) return __real_access(path, amode);
    return __wasmify_fs_result(rc);
}

int __wrap_faccessat(int fd, const char *path, int amode, int flag) {
    if (fd != AT_FDCWD) return __real_faccessat(fd, path, amode, flag);
    if (__wasmify_fs_no_path(path)) return -1;
    /* AT_EACCESS is 0 in some versions of the C library's headers, which
     * then cannot ask for the effective ids. */
    int rc = __wasmify_fs_access(path, amode, AT_EACCESS != 0 && (flag & AT_EACCESS) != 0);
    if (rc == -ENOSYS) return __real_faccessat(fd, path, amode, flag);
    return __wasmify_fs_result(rc);
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
    st->st_rdev = (dev_t)ext->rdev;
    st->st_blocks = (blkcnt_t)ext->blocks;
    if (ext->blksize != 0) st->st_blksize = (blksize_t)ext->blksize;
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

#endif
#ifdef WASMIFY_HOST_FILESYSTEM_LOCKING
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

#endif
#ifdef WASMIFY_HOST_FILESYSTEM_ATTRIBUTES
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
#endif

/* The directory path names, as the kernel finds it and getcwd(2) names
 * it: from the root, each component in turn, a symbolic link replaced by
 * its target (from the root for an absolute one), ".." the parent of what
 * has been reached. A component that cannot be looked at ends the walk,
 * and the rest is taken as written, for chdir to say what is wrong with
 * it. Returns 0, or -1 with errno set (ELOOP, ENAMETOOLONG). */
static int __wasmify_physical_dir(const char *path, char *out, size_t outsz) {
    char rest[4096];
    if (snprintf(rest, sizeof(rest), "%s", path) >= (int)sizeof(rest)) {
        errno = ENAMETOOLONG;
        return -1;
    }
    size_t o = 0;
    int links = 0, walking = 1;
    const char *p = rest;
    for (;;) {
        while (*p == '/') p++;
        if (!*p) break;
        const char *c = p;
        while (*p && *p != '/') p++;
        const size_t l = (size_t)(p - c);
        if (l == 1 && c[0] == '.') continue;
        if (l == 2 && c[0] == '.' && c[1] == '.' && walking) {
            while (o > 0 && out[o - 1] != '/') o--;
            if (o > 0) o--;
            continue;
        }
        if (o + 1 + l + 1 > outsz) {
            errno = ENAMETOOLONG;
            return -1;
        }
        out[o] = '/';
        memcpy(out + o + 1, c, l);
        out[o + 1 + l] = 0;
        struct stat st;
        if (walking && lstat(out, &st) != 0) {
            walking = 0;
        } else if (walking && S_ISLNK(st.st_mode)) {
            if (++links > 40) {
                errno = ELOOP;
                return -1;
            }
            char target[4096];
            const ssize_t tl = readlink(out, target, sizeof(target) - 1);
            if (tl > 0) {
                target[tl] = 0;
                /* the target, then what is left after the link */
                char next[4096];
                if (snprintf(next, sizeof(next), "%s/%s", target, p) >= (int)sizeof(next)) {
                    errno = ENAMETOOLONG;
                    return -1;
                }
                memcpy(rest, next, strlen(next) + 1);
                p = rest;
                if (target[0] == '/') o = 0;
                continue;
            }
            walking = 0;
        }
        o += 1 + l;
    }
    if (o == 0) out[o++] = '/';
    out[o] = 0;
    return 0;
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
    char out[4096];
    if (__wasmify_physical_dir(joined, out, sizeof(out)) != 0) return -1;
    const size_t o = strlen(out);
    /* chdir(2) needs search permission on the directory, which libc's
     * chdir does not ask about (it opens the directory, which reading
     * permission allows): a directory of mode 0644 is refused with EACCES.
     * A path that is no directory is left to libc, which says what it is. */
    const int searchable = __wasmify_fs_access(out, X_OK, 1);
    if (searchable != 0 && searchable != -ENOSYS) {
        struct stat st;
        if (stat(out, &st) == 0 && S_ISDIR(st.st_mode)) return __wasmify_fs_result(searchable);
    }
    const int rc = __real_chdir(out);
    /* The working directory is libc's to keep; the host is told where the
     * guest now is. What it makes of that does not change the outcome. */
    if (rc == 0) (void)__wasmify_host_proc_chdir(out, (int)o);
    return rc;
}

} /* extern "C" */

/* ---- owners, FIFOs, temporary files, fchdir -------------------------------
 * Each of these either does its job with what the host has, or fails with
 * the errno of the thing that is missing; a libc function left undefined
 * would be an import answered with "success" for something that never
 * happened. */
namespace {

int fail(int error) {
    errno = error;
    return -1;
}

#ifdef WASMIFY_HOST_FILESYSTEM_ATTRIBUTES
/* Where files have no owners to change (the host says ENOSYS), a request
 * to change a file's owner is granted when it changes nothing — each id is
 * -1 ("leave it") or the one the file already has — and refused otherwise:
 * the process has no privilege to give files away. */
int change_owner(const struct stat &st, uid_t owner, gid_t group) {
    if ((owner != (uid_t)-1 && owner != st.st_uid) || (group != (gid_t)-1 && group != st.st_gid))
        return fail(EPERM);
    return 0;
}

#endif

#if defined(WASMIFY_HOST_FILESYSTEM_ATTRIBUTES) || defined(WASMIFY_HOST_FILESYSTEM_FIFO)
/* A path as the host imports take it: absolute, without the leading
 * slash. Returns NULL with errno set when the path cannot be named. */
const char *root_relative(const char *path, char *buf, size_t size) {
    if (!path) {
        errno = EFAULT;
        return NULL;
    }
    if (!*path) {
        errno = ENOENT;
        return NULL;
    }
    const char *p = path;
    if (*p != '/') {
        char cwd[3584];
        if (!getcwd(cwd, sizeof(cwd))) return NULL;
        if (snprintf(buf, size, "%s/%s", cwd, path) >= (int)size) {
            errno = ENAMETOOLONG;
            return NULL;
        }
        p = buf;
    }
    while (*p == '/') p++;
    return p;
}

#endif

#ifdef WASMIFY_HOST_FILESYSTEM_ATTRIBUTES
int change_path_owner(const char *path, uid_t owner, gid_t group, int follow) {
    char buf[4096];
    const char *p = root_relative(path, buf, sizeof(buf));
    if (!p) return -1;
    const int rc = __wasmify_host_path_chown(p, (int)strlen(p), (int)owner, (int)group, follow);
    if (rc == 0) return 0;
    if (rc != -ENOSYS) return fail(-rc);
    struct stat st;
    if ((follow ? stat(path, &st) : lstat(path, &st)) != 0) return -1;
    return change_owner(st, owner, group);
}

#endif

} /* namespace */

extern "C" {

#ifdef WASMIFY_HOST_FILESYSTEM_ATTRIBUTES
/* ---- file ownership ------------------------------------------------------ */
int chown(const char *path, uid_t owner, gid_t group) {
    return change_path_owner(path, owner, group, 1);
}

int lchown(const char *path, uid_t owner, gid_t group) {
    return change_path_owner(path, owner, group, 0);
}

int fchown(int fd, uid_t owner, gid_t group) {
    const int rc = __wasmify_host_fd_chown(fd, (int)owner, (int)group);
    if (rc == 0) return 0;
    if (rc != -ENOSYS) return fail(-rc);
    struct stat st;
    if (fstat(fd, &st) != 0) return -1;
    return change_owner(st, owner, group);
}

#endif

/* ---- temporary files ------------------------------------------------------
 * mkstemp: replaces the trailing XXXXXX of the template with characters
 * that make a name no file has, and creates that file, for this process
 * only. */
int mkstemp(char *tmpl) {
    static const char alphabet[] =
        "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789";
    const size_t len = tmpl ? strlen(tmpl) : 0;
    if (len < 6 || memcmp(tmpl + len - 6, "XXXXXX", 6) != 0) return fail(EINVAL);
    for (int attempt = 0; attempt < 100; attempt++) {
        unsigned char random[6];
        if (getentropy(random, sizeof(random)) != 0) return -1;
        for (int i = 0; i < 6; i++)
            tmpl[len - 6 + i] = alphabet[random[i] % (sizeof(alphabet) - 1)];
        const int fd = open(tmpl, O_RDWR | O_CREAT | O_EXCL, 0600);
        if (fd >= 0) return fd;
        if (errno != EEXIST) break;
    }
    memcpy(tmpl + len - 6, "XXXXXX", 6);
    return -1;
}

/* ---- the working directory by descriptor ----------------------------------- */

/* The working directory is a path kept by libc, so a directory that is
 * known by its descriptor has to be named first. The host knows the path the
 * descriptor was opened under (fd_path). That name may lead somewhere else
 * by now — the directory was renamed, or replaced — and fchdir is about the
 * directory, not the name: when the two no longer agree the directory
 * cannot be named, and the call fails rather than go elsewhere. */
int fchdir(int fd) {
    struct stat by_descriptor;
    if (fstat(fd, &by_descriptor) != 0) return -1;
    if (!S_ISDIR(by_descriptor.st_mode)) return fail(ENOTDIR);
    char path[4096];
    int length = 0;
    const int rc = __wasmify_host_fd_path(fd, path, (int)sizeof(path) - 1, &length);
    if (rc != 0) return fail(rc == -ERANGE ? ENAMETOOLONG : -rc);
    path[length] = '\0';
    struct stat by_name;
    if (stat(path, &by_name) != 0) return -1;
    if (by_name.st_dev != by_descriptor.st_dev || by_name.st_ino != by_descriptor.st_ino)
        return fail(ENOENT);
    return chdir(path);
}

/* ---- what a WASI process cannot do ---------------------------------------- */

/* The root of the filesystem is the host's to set. */
int chroot(const char *path) {
    (void)path;
    return fail(EPERM);
}

#ifdef WASMIFY_HOST_FILESYSTEM_FIFO
/* FIFOs are the host filesystem's to create; WASI has no call for it. */
int mkfifo(const char *path, mode_t mode) {
    char buf[4096];
    const char *p = root_relative(path, buf, sizeof(buf));
    if (!p) return -1;
    const int rc = __wasmify_host_path_mkfifo(p, (int)strlen(p), (unsigned int)mode);
    if (rc != 0) return fail(-rc);
    return 0;
}
#endif

} /* extern "C" */

#endif /* WASMIFY_HOST_FILESYSTEM */
