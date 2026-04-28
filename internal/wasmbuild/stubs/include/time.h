/*
 * POSIX time.h compatibility overlay for wasip1.
 *
 * wasip1 only defines CLOCK_REALTIME and CLOCK_MONOTONIC.
 * This overlay adds CLOCK_PROCESS_CPUTIME_ID and CLOCK_THREAD_CPUTIME_ID
 * which are guarded behind __wasilibc_unmodified_upstream in the sysroot.
 *
 * wasip1 uses a struct-pointer clockid_t (const struct __clockid *),
 * so we must declare extern const structs matching that pattern.
 */
#include_next <time.h>

#ifndef _WASMIFY_TIME_OVERLAY_H
#define _WASMIFY_TIME_OVERLAY_H

#ifdef __cplusplus
extern "C" {
#endif

#ifndef CLOCK_PROCESS_CPUTIME_ID
extern const struct __clockid _CLOCK_PROCESS_CPUTIME_ID;
#define CLOCK_PROCESS_CPUTIME_ID (&_CLOCK_PROCESS_CPUTIME_ID)
#endif

#ifndef CLOCK_THREAD_CPUTIME_ID
extern const struct __clockid _CLOCK_THREAD_CPUTIME_ID;
#define CLOCK_THREAD_CPUTIME_ID (&_CLOCK_THREAD_CPUTIME_ID)
#endif

#ifndef CLOCK_MONOTONIC_RAW
extern const struct __clockid _CLOCK_MONOTONIC_RAW;
#define CLOCK_MONOTONIC_RAW (&_CLOCK_MONOTONIC_RAW)
#endif

#ifndef CLOCK_MONOTONIC_COARSE
extern const struct __clockid _CLOCK_MONOTONIC_COARSE;
#define CLOCK_MONOTONIC_COARSE (&_CLOCK_MONOTONIC_COARSE)
#endif

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_TIME_OVERLAY_H */
