/* syslog.h — the system logger interface, which wasi-libc does not have.
 *
 * host_syslog.cc implements the five functions on host imports: whether a
 * message can be delivered is the host's to say (an embedder enables a
 * logger). The constants have the values every Unix gives them.
 */
#ifndef WASMIFY_SYSLOG_H
#define WASMIFY_SYSLOG_H

#include <stdarg.h>

/* the conventional path of the log socket, for code that asks for it */
#define _PATH_LOG "/dev/log"

/* levels */
#define LOG_EMERG   0
#define LOG_ALERT   1
#define LOG_CRIT    2
#define LOG_ERR     3
#define LOG_WARNING 4
#define LOG_NOTICE  5
#define LOG_INFO    6
#define LOG_DEBUG   7

#define LOG_PRIMASK 0x07
#define LOG_PRI(p) ((p) & LOG_PRIMASK)
#define LOG_MAKEPRI(fac, pri) ((fac) | (pri))

/* facilities */
#define LOG_KERN     (0 << 3)
#define LOG_USER     (1 << 3)
#define LOG_MAIL     (2 << 3)
#define LOG_DAEMON   (3 << 3)
#define LOG_AUTH     (4 << 3)
#define LOG_SYSLOG   (5 << 3)
#define LOG_LPR      (6 << 3)
#define LOG_NEWS     (7 << 3)
#define LOG_UUCP     (8 << 3)
#define LOG_CRON     (9 << 3)
#define LOG_AUTHPRIV (10 << 3)
#define LOG_FTP      (11 << 3)
#define LOG_LOCAL0   (16 << 3)
#define LOG_LOCAL1   (17 << 3)
#define LOG_LOCAL2   (18 << 3)
#define LOG_LOCAL3   (19 << 3)
#define LOG_LOCAL4   (20 << 3)
#define LOG_LOCAL5   (21 << 3)
#define LOG_LOCAL6   (22 << 3)
#define LOG_LOCAL7   (23 << 3)

#define LOG_NFACILITIES 24
#define LOG_FACMASK 0x03f8
#define LOG_FAC(p) (((p) & LOG_FACMASK) >> 3)

#define LOG_MASK(pri) (1 << (pri))
#define LOG_UPTO(pri) ((1 << ((pri) + 1)) - 1)

/* openlog options */
#define LOG_PID    0x01
#define LOG_CONS   0x02
#define LOG_ODELAY 0x04
#define LOG_NDELAY 0x08
#define LOG_NOWAIT 0x10
#define LOG_PERROR 0x20

#ifdef __cplusplus
extern "C" {
#endif

void openlog(const char *ident, int option, int facility);
void syslog(int priority, const char *format, ...);
void vsyslog(int priority, const char *format, va_list args);
void closelog(void);
int setlogmask(int mask);

#ifdef __cplusplus
}
#endif

#endif /* WASMIFY_SYSLOG_H */
