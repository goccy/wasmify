/* fcntl.h — the C library's, with O_CLOEXEC a flag of its own when the host
 * capability process.spawn is given: wasi-libc's is 0, which open, openat,
 * pipe2 and dup3 cannot see. process.spawn's shims take the flag off and
 * mark the new descriptor close-on-exec with the host, so that a command
 * does not inherit it. The bit is none of the access modes, descriptor
 * flags or open flags wasi-libc's own open reads, which would pass it over.
 * The descriptors wasi-libc opens without these functions (fopen's "e"
 * mode, opendir) are not marked. */
#include_next <fcntl.h>

#ifdef WASMIFY_HOST_PROCESS_SPAWN
#undef O_CLOEXEC
#define O_CLOEXEC 0x40000000
#endif
