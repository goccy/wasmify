/* host_syslog.cc — syslog(3) for a libc that has none.
 *
 * openlog, syslog, closelog and setlogmask. What a libc keeps for them is
 * kept here (the ident, the option flags, the default facility, the
 * priority mask); delivery is the host's, through three imports that carry
 * the values as they are: severity, facility number, option flags, ident
 * and message text.
 *
 * The functions of syslog(3) return nothing, so a real one loses a message
 * silently when no logger takes it. Here the host says whether there is a
 * logger at all (an embedder has to enable it), and a program that must be
 * able to tell defines __wasmify_syslog_undelivered (wasmify_posix.h): it
 * is called, from openlog or syslog, for a message that was not delivered.
 *
 * Compiled when the host capability system.logging is given (WASMIFY_HOST_SYSTEM_LOGGING).
 */
#ifdef WASMIFY_HOST_SYSTEM_LOGGING

#include <cerrno>
#include <cstdarg>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <unistd.h>

#include <syslog.h>

#include "wasmify_posix.h"

namespace {

char *g_ident = NULL;         /* openlog's ident, copied; NULL: none set */
int g_options = 0;            /* openlog's option flags */
int g_facility = LOG_USER;    /* the facility of a message that names none */
int g_mask = LOG_UPTO(LOG_DEBUG); /* the priorities that are logged */

size_t ident_length() { return g_ident ? strlen(g_ident) : 0; }

} /* namespace */

extern "C" {

/* An application that must know defines its own. */
__attribute__((weak)) void __wasmify_syslog_undelivered(int error) { (void)error; }

void openlog(const char *ident, int option, int facility) {
    free(g_ident);
    g_ident = ident ? strdup(ident) : NULL;
    g_options = option;
    if (facility != 0 && (facility & ~LOG_FACMASK) == 0) g_facility = facility;
    const int rc =
        __wasmify_host_proc_syslog_open(g_ident, (int)ident_length(), g_options, LOG_FAC(g_facility));
    if (rc != 0) __wasmify_syslog_undelivered(-rc);
}

void vsyslog(int priority, const char *format, va_list args) {
    const int saved_errno = errno; /* what %m means */
    if (!(g_mask & LOG_MASK(LOG_PRI(priority)))) return;
    int facility = priority & LOG_FACMASK;
    if (facility == 0) facility = g_facility;

    va_list measure;
    va_copy(measure, args);
    errno = saved_errno;
    const int length = vsnprintf(NULL, 0, format, measure);
    va_end(measure);
    if (length < 0) return;
    char *message = (char *)malloc((size_t)length + 1);
    if (!message) return;
    errno = saved_errno;
    vsnprintf(message, (size_t)length + 1, format, args);

    if (g_options & LOG_PERROR) {
        const bool ends_line = length > 0 && message[length - 1] == '\n';
        if (g_ident && (g_options & LOG_PID))
            dprintf(STDERR_FILENO, "%s[%d]: ", g_ident, (int)getpid());
        else if (g_ident)
            dprintf(STDERR_FILENO, "%s: ", g_ident);
        dprintf(STDERR_FILENO, "%s%s", message, ends_line ? "" : "\n");
    }
    const int rc = __wasmify_host_proc_syslog(LOG_PRI(priority), LOG_FAC(facility), g_options, g_ident,
                                         (int)ident_length(), message, length);
    free(message);
    errno = saved_errno;
    if (rc != 0) __wasmify_syslog_undelivered(-rc);
}

void syslog(int priority, const char *format, ...) {
    va_list args;
    va_start(args, format);
    vsyslog(priority, format, args);
    va_end(args);
}

void closelog(void) {
    /* nothing to report: without a logger there was nothing to close */
    __wasmify_host_proc_syslog_close();
    free(g_ident);
    g_ident = NULL;
    g_options = 0;
    g_facility = LOG_USER;
}

int setlogmask(int mask) {
    const int previous = g_mask;
    if (mask != 0) g_mask = mask;
    return previous;
}

} /* extern "C" */

#endif /* WASMIFY_HOST_SYSTEM_LOGGING */
