/* bits/alltypes.h — the C library's types, with sigset_t a set of signals
 * when the host capability signals is given: 64 bits, signal n at bit n-1
 * (wasi-libc's own sigset_t is one byte, which cannot hold a set). Every
 * header that needs sigset_t (signal.h, pthread.h, poll.h, sys/select.h)
 * gets it from here, so a program has one sigset_t whatever it includes
 * first. No include guard: the C library includes this file once for each
 * type it needs. */
#if defined(WASMIFY_HOST_SIGNALS) && defined(__NEED_sigset_t) && !defined(__DEFINED_sigset_t)
typedef unsigned long long sigset_t;
#define __DEFINED_sigset_t
#endif
#include_next <bits/alltypes.h>
