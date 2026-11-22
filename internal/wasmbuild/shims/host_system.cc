/* host_system.cc — the system's name.
 *
 * Compiled when the host capability system.info is given
 * (WASMIFY_HOST_SYSTEM_INFO).
 */
#ifdef WASMIFY_HOST_SYSTEM_INFO

#include <sys/utsname.h>
#include <cerrno>
#include <cstdint>
#include <cstdlib>
#include <cstring>

#include "wasmify_posix.h"

namespace {

int fail(int error) {
    errno = error;
    return -1;
}

/* The record of proc_uname, in a buffer of the size the host asks for.
 * Returns 0 with *rec (to be freed) and *size, or the import's negative
 * errno (-ENOSYS: the process is a system of its own), or -ENOMEM. */
int uname_record(char **rec, uint32_t *size) {
    char *buf = NULL;
    uint32_t cap = 0;
    for (;;) {
        uint32_t needed = 0;
        const int rc = __wasmify_host_proc_uname(buf, (int)cap, &needed);
        if (rc == 0) {
            *rec = buf;
            *size = needed;
            return 0;
        }
        if (rc != -ERANGE || needed <= cap) {
            free(buf);
            return rc;
        }
        char *grown = (char *)realloc(buf, needed);
        if (!grown) {
            free(buf);
            return -ENOMEM;
        }
        buf = grown;
        cap = needed;
    }
}

/* Fills the fields of uname from the record: five strings, each a u32
 * length and the bytes, cut to the room of its field. Returns false for a
 * record that does not hold five strings. */
bool decode_uname(const char *rec, uint32_t size, struct utsname *out) {
    char *fields[5] = {out->sysname, out->nodename, out->release, out->version, out->machine};
    const size_t sizes[5] = {sizeof(out->sysname), sizeof(out->nodename), sizeof(out->release),
                             sizeof(out->version), sizeof(out->machine)};
    uint32_t pos = 0;
    for (int i = 0; i < 5; i++) {
        uint32_t n = 0;
        if (size - pos < 4) return false;
        memcpy(&n, rec + pos, 4);
        pos += 4;
        if (size - pos < n) return false;
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
 * isolated one (the host says ENOSYS) is a system of its own, with the
 * names wasi-libc gives. */
int uname(struct utsname *out) {
    if (!out) return fail(EFAULT);
    char *rec = NULL;
    uint32_t size = 0;
    const int rc = uname_record(&rec, &size);
    if (rc == 0) {
        const bool decoded = decode_uname(rec, size, out);
        free(rec);
        return decoded ? 0 : fail(EIO);
    }
    if (rc != -ENOSYS) return fail(-rc);
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
