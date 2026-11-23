/* time.h of the host capabilities: the C library's, and the functions and
 * variables the host shims define that it does not declare. */
#include_next <time.h>

#ifndef _WASMIFY_HOST_TIME_H
#define _WASMIFY_HOST_TIME_H

#ifdef __cplusplus
extern "C" {
#endif

#ifdef WASMIFY_HOST_SYSTEM_TIME_ZONES
/* host_time.cc */
void tzset(void);
extern char *tzname[2];
extern long timezone;
extern int daylight;
#endif

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_HOST_TIME_H */
