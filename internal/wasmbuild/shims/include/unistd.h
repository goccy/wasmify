/* unistd.h of the host capabilities: the C library's, and the functions
 * the host shims define that it does not declare. */
#include_next <unistd.h>

#ifndef _WASMIFY_HOST_UNISTD_H
#define _WASMIFY_HOST_UNISTD_H

#ifdef __cplusplus
extern "C" {
#endif

#ifdef WASMIFY_HOST_PROCESS_SPAWN
/* host_subprocess.cc */
void closefrom(int low_fd);
#endif

#ifdef WASMIFY_HOST_PROCESS_RESOURCES
/* host_process.cc */
pid_t getppid(void);
int nice(int increment);
#endif

#ifdef WASMIFY_HOST_SIGNALS
/* host_signal.cc */
int pause(void);
#endif

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_HOST_UNISTD_H */
