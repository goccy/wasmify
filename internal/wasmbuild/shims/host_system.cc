/* host_system.cc — the system's name.
 *
 * Compiled when the host capability system.info is given
 * (WASMIFY_HOST_SYSTEM_INFO).
 */
#ifdef WASMIFY_HOST_SYSTEM_INFO

#include <sys/utsname.h>
#include <cerrno>
#include <cstdint>
#include <cstring>

#include "wasmify_posix.h"

namespace {

int fail(int error) {
    errno = error;
    return -1;
}

/* The fields of uname as the host reports them; false when the process is
 * a system of its own. */
bool host_uname(struct utsname *out) {
    char rec[2048];
    uint32_t needed = 0;
    if (__wasmify_host_proc_uname(rec, (int)sizeof(rec), &needed) != 0) return false;
    char *fields[5] = {out->sysname, out->nodename, out->release, out->version, out->machine};
    const size_t sizes[5] = {sizeof(out->sysname), sizeof(out->nodename), sizeof(out->release),
                             sizeof(out->version), sizeof(out->machine)};
    uint32_t pos = 0;
    for (int i = 0; i < 5; i++) {
        uint32_t n = 0;
        if (needed - pos < 4) return false;
        memcpy(&n, rec + pos, 4);
        pos += 4;
        if (needed - pos < n) return false;
        const size_t kept = n < sizes[i] - 1 ? n : sizes[i] - 1;
        memcpy(fields[i], rec + pos, kept);
        fields[i][kept] = '\0';
        pos += n;
    }
    return true;
}

} /* namespace */

extern "C" {

/* ---- the system's name --------------------------------------------------------
 * A process that is the host process is on the host's system: uname and
 * gethostname (which libc answers from uname) report the host's. An
 * isolated one is a system of its own, with the names wasi-libc gives. */
int uname(struct utsname *out) {
    if (!out) return fail(EFAULT);
    if (host_uname(out)) return 0;
    strcpy(out->sysname, "wasi");
    strcpy(out->nodename, "(none)");
    strcpy(out->release, "0.0.0");
    strcpy(out->version, "0.0.0");
#ifdef __wasm64__
    strcpy(out->machine, "wasm64");
#else
    strcpy(out->machine, "wasm32");
#endif
    return 0;
}

} /* extern "C" */

#endif /* WASMIFY_HOST_SYSTEM_INFO */
