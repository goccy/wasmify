/*
 * POSIX stdio compatibility overlay for wasip1.
 *
 * wasi-sdk's stdio.h guards popen/pclose and flockfile/funlockfile
 * behind __wasilibc_unmodified_upstream. This overlay adds them.
 */
#include_next <stdio.h>

#ifndef _WASMIFY_STDIO_OVERLAY_H
#define _WASMIFY_STDIO_OVERLAY_H

#ifdef __cplusplus
extern "C" {
#endif

FILE *popen(const char *, const char *);
int pclose(FILE *);
void flockfile(FILE *);
void funlockfile(FILE *);
int getc_unlocked(FILE *);
int putc_unlocked(int, FILE *);

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_STDIO_OVERLAY_H */
