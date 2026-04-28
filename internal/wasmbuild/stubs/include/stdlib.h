/*
 * POSIX stdlib compatibility overlay for wasip1.
 *
 * wasi-sdk's stdlib.h guards mkstemp/mkostemp/mkdtemp behind
 * __wasilibc_unmodified_upstream. This overlay adds them.
 */
#include_next <stdlib.h>

#ifndef _WASMIFY_STDLIB_OVERLAY_H
#define _WASMIFY_STDLIB_OVERLAY_H

#ifdef __cplusplus
extern "C" {
#endif

int mkstemp(char *);
int mkostemp(char *, int);
char *mkdtemp(char *);
int mkstemps(char *, int);
int mkostemps(char *, int, int);
char *mktemp(char *);

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_STDLIB_OVERLAY_H */
