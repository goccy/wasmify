/* host_fs.cc — generic filesystem-fidelity shim.
 *
 * This translation unit is deployed and compiled by wasmify itself, and only
 * when host filesystem fidelity is opted in (bridge.HostFS in wasmify.json,
 * which makes wasmify define WASMIFY_HOST_FS for this compile and add the
 * matching -Wl,--wrap= flags to every link). Without the opt-in the file is
 * excluded entirely and the wasm stays portable (standard wasi imports only).
 *
 * WASI preview1 has no way to change file modes, its filestat carries no
 * permission bits, and wasi-libc stores chdir's argument verbatim (".."
 * included) as its cwd. All three break real-world build tooling:
 *
 *   - chmod() fails outright (wasi-libc's is an ENOSYS stub);
 *   - stat()/lstat() report st_mode with the file type only, so code that
 *     round-trips modes (chmod((stat(f)).st_mode | 0111) to mark a file
 *     executable) strips every permission;
 *   - after `chdir("b"); chdir("..")` the cwd string keeps a live reference
 *     through b, and deleting b then breaks every cwd-relative operation
 *     (a recursive-delete's chdir-down/up dance does exactly this).
 *
 * The wrappers below fix each: chmod goes to the host via the path_chmod
 * import, stat/lstat merge real permission bits from path_filestat_mode
 * into the libc result, and chdir collapses "." and ".." lexically before
 * wasi-libc records it (wasi-libc's own resolution is lexical, so nothing
 * is lost). Paths cross the imports preopen-relative, like path_open's. */
#ifdef WASMIFY_HOST_FS

#include <sys/stat.h>
#include <sys/types.h>
#include <unistd.h>
#include <cerrno>
#include <cstdio>
#include <cstring>

extern "C" {

__attribute__((import_module("wasi_snapshot_preview1"), import_name("path_chmod")))
extern int __wasmify_host_path_chmod(const char *path, int len, unsigned int mode);

__attribute__((import_module("wasi_snapshot_preview1"), import_name("path_filestat_mode")))
extern int __wasmify_host_path_filestat_mode(const char *path, int len,
                                             int follow, unsigned int *mode_out);

extern int __real_chmod(const char *path, mode_t mode);
extern int __real_stat(const char *path, struct stat *st);
extern int __real_lstat(const char *path, struct stat *st);
extern int __real_chdir(const char *path);

/* Absolutize against the guest cwd and strip the leading '/' so the path
 * crosses the imports preopen-relative. Returns path or buf. */
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
    if (path == nullptr) {
        errno = EFAULT;
        return -1;
    }
    char buf[4096];
    const char *p = __wasmify_fs_import_path(path, buf, sizeof(buf));
    int rc = __wasmify_host_path_chmod(p, (int)strlen(p), (unsigned int)mode);
    if (rc != 0) {
        errno = (rc < 0) ? -rc : rc;
        return -1;
    }
    return 0;
}

static int __wasmify_stat_common(const char *path, struct stat *st, int follow) {
    int rc = follow ? __real_stat(path, st) : __real_lstat(path, st);
    if (rc != 0) return rc;
    char buf[4096];
    const char *p = __wasmify_fs_import_path(path, buf, sizeof(buf));
    unsigned int mode = 0;
    if (__wasmify_host_path_filestat_mode(p, (int)strlen(p), follow, &mode) == 0)
        st->st_mode = (st->st_mode & S_IFMT) | (mode_t)(mode & 07777);
    return 0;
}

int __wrap_stat(const char *path, struct stat *st) {
    return __wasmify_stat_common(path, st, 1);
}

int __wrap_lstat(const char *path, struct stat *st) {
    return __wasmify_stat_common(path, st, 0);
}

int __wrap_chdir(const char *path) {
    if (path == nullptr) {
        errno = EFAULT;
        return -1;
    }
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
    return __real_chdir(out);
}

} /* extern "C" */

#endif /* WASMIFY_HOST_FS */
