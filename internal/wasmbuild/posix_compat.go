package wasmbuild

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

//go:embed stubs/include/sys/socket.h stubs/include/netdb.h stubs/include/signal.h stubs/include/stdio.h stubs/include/unistd.h stubs/include/stdlib.h stubs/include/__struct_in6_addr.h stubs/include/netinet/in.h stubs/include/time.h stubs/include/emscripten/version.h stubs/include/emscripten.h stubs/include/emscripten/console.h
var posixCompatFS embed.FS

// DeployPosixCompat extracts the always-on POSIX compatibility headers
// (sys/socket.h overlay, netdb.h) to <buildDir>/posix-compat/include/
// and returns the include directory path.
func DeployPosixCompat(buildDir string) (string, error) {
	includeDir := filepath.Join(buildDir, "posix-compat", "include")

	err := fs.WalkDir(posixCompatFS, "stubs/include", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel("stubs/include", path)
		if err != nil {
			return err
		}
		dst := filepath.Join(includeDir, rel)

		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}

		data, err := posixCompatFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read embedded file %s: %w", path, err)
		}

		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		return "", fmt.Errorf("failed to deploy POSIX compat headers: %w", err)
	}

	return includeDir, nil
}

// HeaderStub describes a POSIX header stub for wasip1 compatibility.
type HeaderStub struct {
	Description string // Human-readable explanation shown to the user
	Content     string // C header content
}

// LookupHeaderStub returns the stub definition for a missing header, if known.
func LookupHeaderStub(header string) (HeaderStub, bool) {
	stub, ok := headerStubRegistry[header]
	return stub, ok
}

// DeployStubHeader writes a single stub header file to the include directory.
func DeployStubHeader(includeDir, header, content string) error {
	dst := filepath.Join(includeDir, header)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("failed to create directory for %s: %w", header, err)
	}
	return os.WriteFile(dst, []byte(content), 0o644)
}

// missingHeaderRe matches clang's "file not found" error.
var missingHeaderRe = regexp.MustCompile(`fatal error: '([^']+)' file not found`)

// DetectMissingHeaders parses compiler stderr output and returns
// the list of missing header paths (deduplicated).
func DetectMissingHeaders(stderr string) []string {
	matches := missingHeaderRe.FindAllStringSubmatch(stderr, -1)
	seen := make(map[string]bool)
	var headers []string
	for _, m := range matches {
		h := m[1]
		if !seen[h] {
			seen[h] = true
			headers = append(headers, h)
		}
	}
	return headers
}

// headerStubRegistry maps header paths to their stub definitions.
// These are headers that do not exist in the wasip1 sysroot.
// The always-deployed headers (sys/socket.h overlay, netdb.h) are not listed here.
var headerStubRegistry = map[string]HeaderStub{
	"grp.h": {
		Description: "UNIX group database API (getgrnam, getgrgid, struct group).\n" +
			"Used by server software to change process groups for privilege separation.",
		Content: strings.TrimLeft(`
#ifndef _WASMIFY_GRP_H
#define _WASMIFY_GRP_H

#include <sys/types.h>

#ifdef __cplusplus
extern "C" {
#endif

struct group {
    char   *gr_name;
    char   *gr_passwd;
    gid_t   gr_gid;
    char  **gr_mem;
};

struct group *getgrnam(const char *);
struct group *getgrgid(gid_t);
int getgrnam_r(const char *, struct group *, char *, size_t, struct group **);
int getgrgid_r(gid_t, struct group *, char *, size_t, struct group **);
void endgrent(void);
struct group *getgrent(void);
void setgrent(void);

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_GRP_H */
`, "\n"),
	},

	"pwd.h": {
		Description: "UNIX password/user database API (getpwnam, getpwuid, struct passwd).\n" +
			"Used by server software to look up user information and drop privileges.",
		Content: strings.TrimLeft(`
#ifndef _WASMIFY_PWD_H
#define _WASMIFY_PWD_H

#include <sys/types.h>

#ifdef __cplusplus
extern "C" {
#endif

struct passwd {
    char   *pw_name;
    char   *pw_passwd;
    uid_t   pw_uid;
    gid_t   pw_gid;
    char   *pw_gecos;
    char   *pw_dir;
    char   *pw_shell;
};

struct passwd *getpwnam(const char *);
struct passwd *getpwuid(uid_t);
int getpwnam_r(const char *, struct passwd *, char *, size_t, struct passwd **);
int getpwuid_r(uid_t, struct passwd *, char *, size_t, struct passwd **);
void endpwent(void);
struct passwd *getpwent(void);
void setpwent(void);

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_PWD_H */
`, "\n"),
	},

	"poll.h": {
		Description: "I/O multiplexing API (poll, struct pollfd).\n" +
			"Used for non-blocking I/O and event-driven network programming.",
		Content: strings.TrimLeft(`
#ifndef _WASMIFY_POLL_H
#define _WASMIFY_POLL_H

#ifdef __cplusplus
extern "C" {
#endif

typedef unsigned int nfds_t;

struct pollfd {
    int   fd;
    short events;
    short revents;
};

#define POLLIN     0x0001
#define POLLPRI    0x0002
#define POLLOUT    0x0004
#define POLLERR    0x0008
#define POLLHUP    0x0010
#define POLLNVAL   0x0020
#define POLLRDNORM 0x0040
#define POLLRDBAND 0x0080
#define POLLWRNORM POLLOUT
#define POLLWRBAND 0x0100

int poll(struct pollfd *, nfds_t, int);

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_POLL_H */
`, "\n"),
	},

	"netinet/tcp.h": {
		Description: "TCP protocol constants (TCP_NODELAY, TCP_KEEPIDLE, etc.).\n" +
			"Used for fine-tuning TCP socket behavior.",
		Content: strings.TrimLeft(`
#ifndef _WASMIFY_NETINET_TCP_H
#define _WASMIFY_NETINET_TCP_H

#ifndef TCP_NODELAY
#define TCP_NODELAY     1
#endif
#ifndef TCP_MAXSEG
#define TCP_MAXSEG      2
#endif
#ifndef TCP_KEEPIDLE
#define TCP_KEEPIDLE    4
#endif
#ifndef TCP_KEEPINTVL
#define TCP_KEEPINTVL   5
#endif
#ifndef TCP_KEEPCNT
#define TCP_KEEPCNT     6
#endif
#ifndef TCP_CORK
#define TCP_CORK        3
#endif
#ifndef TCP_DEFER_ACCEPT
#define TCP_DEFER_ACCEPT 9
#endif
#ifndef TCP_FASTOPEN
#define TCP_FASTOPEN    23
#endif

#endif /* _WASMIFY_NETINET_TCP_H */
`, "\n"),
	},

	"arpa/inet.h": {
		Description: "Internet address manipulation API (inet_ntop, inet_pton, inet_addr).\n" +
			"Used for converting between text and binary network address formats.",
		Content: strings.TrimLeft(`
#ifndef _WASMIFY_ARPA_INET_H
#define _WASMIFY_ARPA_INET_H

#include <stdint.h>
#include <netinet/in.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef uint32_t in_addr_t;

in_addr_t inet_addr(const char *);
char *inet_ntoa(struct in_addr);
const char *inet_ntop(int, const void *__restrict, char *__restrict, socklen_t);
int inet_pton(int, const char *__restrict, void *__restrict);

uint32_t htonl(uint32_t);
uint16_t htons(uint16_t);
uint32_t ntohl(uint32_t);
uint16_t ntohs(uint16_t);

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_ARPA_INET_H */
`, "\n"),
	},

	"sys/un.h": {
		Description: "UNIX domain socket address structure (struct sockaddr_un).\n" +
			"Used for local inter-process communication via filesystem paths.",
		Content: strings.TrimLeft(`
#ifndef _WASMIFY_SYS_UN_H
#define _WASMIFY_SYS_UN_H

#include <sys/socket.h>

struct sockaddr_un {
    sa_family_t sun_family;
    char        sun_path[108];
};

#endif /* _WASMIFY_SYS_UN_H */
`, "\n"),
	},

	"sys/utsname.h": {
		Description: "System identification API (uname, struct utsname).\n" +
			"Used for querying OS name, version, and machine architecture.",
		Content: strings.TrimLeft(`
#ifndef _WASMIFY_SYS_UTSNAME_H
#define _WASMIFY_SYS_UTSNAME_H

#ifdef __cplusplus
extern "C" {
#endif

#define _UTSNAME_LENGTH 65

struct utsname {
    char sysname[_UTSNAME_LENGTH];
    char nodename[_UTSNAME_LENGTH];
    char release[_UTSNAME_LENGTH];
    char version[_UTSNAME_LENGTH];
    char machine[_UTSNAME_LENGTH];
};

int uname(struct utsname *);

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_SYS_UTSNAME_H */
`, "\n"),
	},

	"ifaddrs.h": {
		Description: "Network interface address enumeration API (getifaddrs, freeifaddrs).\n" +
			"Used to list all network interfaces and their addresses on the system.",
		Content: strings.TrimLeft(`
#ifndef _WASMIFY_IFADDRS_H
#define _WASMIFY_IFADDRS_H

#include <sys/socket.h>

#ifdef __cplusplus
extern "C" {
#endif

struct ifaddrs {
    struct ifaddrs  *ifa_next;
    char            *ifa_name;
    unsigned int     ifa_flags;
    struct sockaddr *ifa_addr;
    struct sockaddr *ifa_netmask;
    union {
        struct sockaddr *ifu_broadaddr;
        struct sockaddr *ifu_dstaddr;
    } ifa_ifu;
    void            *ifa_data;
};

#define ifa_broadaddr ifa_ifu.ifu_broadaddr
#define ifa_dstaddr   ifa_ifu.ifu_dstaddr

int getifaddrs(struct ifaddrs **);
void freeifaddrs(struct ifaddrs *);

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_IFADDRS_H */
`, "\n"),
	},

	"sys/ioctl.h": {
		Description: "Device I/O control API (ioctl).\n" +
			"Used for device-specific operations like querying terminal size or network interface configuration.",
		Content: strings.TrimLeft(`
#ifndef _WASMIFY_SYS_IOCTL_H
#define _WASMIFY_SYS_IOCTL_H

#ifdef __cplusplus
extern "C" {
#endif

#define FIONREAD  0x541B
#define FIONBIO   0x5421
#define SIOCGIFCONF 0x8912

int ioctl(int, unsigned long, ...);

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_SYS_IOCTL_H */
`, "\n"),
	},

	"pthread.h": {
		Description: "POSIX threads API (pthread_create, pthread_mutex_*, etc.).\n" +
			"Used for multi-threading. In wasm single-threaded mode, thread operations\n" +
			"will be no-ops or return errors at runtime.",
		Content: strings.TrimLeft(`
#ifndef _WASMIFY_PTHREAD_H
#define _WASMIFY_PTHREAD_H

#include <stddef.h>
#include <sys/types.h>
#include <time.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef unsigned long pthread_t;
typedef unsigned int pthread_key_t;
typedef int pthread_once_t;

typedef struct { int __attr; } pthread_attr_t;
typedef struct { int __lock; } pthread_mutex_t;
typedef struct { int __attr; } pthread_mutexattr_t;
typedef struct { int __cond; } pthread_cond_t;
typedef struct { int __attr; } pthread_condattr_t;
typedef struct { int __lock; int __count; } pthread_rwlock_t;
typedef struct { int __attr; } pthread_rwlockattr_t;

#define PTHREAD_MUTEX_INITIALIZER  {0}
#define PTHREAD_COND_INITIALIZER   {0}
#define PTHREAD_RWLOCK_INITIALIZER {0, 0}
#define PTHREAD_ONCE_INIT          0

#define PTHREAD_CREATE_JOINABLE 0
#define PTHREAD_CREATE_DETACHED 1
#define PTHREAD_MUTEX_NORMAL    0
#define PTHREAD_MUTEX_RECURSIVE 1
#define PTHREAD_MUTEX_ERRORCHECK 2
#define PTHREAD_MUTEX_DEFAULT   PTHREAD_MUTEX_NORMAL

int pthread_create(pthread_t *, const pthread_attr_t *, void *(*)(void *), void *);
int pthread_join(pthread_t, void **);
int pthread_detach(pthread_t);
pthread_t pthread_self(void);
int pthread_equal(pthread_t, pthread_t);
void pthread_exit(void *);

int pthread_attr_init(pthread_attr_t *);
int pthread_attr_destroy(pthread_attr_t *);
int pthread_attr_setdetachstate(pthread_attr_t *, int);
int pthread_attr_getdetachstate(const pthread_attr_t *, int *);
int pthread_attr_setstacksize(pthread_attr_t *, size_t);
int pthread_attr_getstacksize(const pthread_attr_t *, size_t *);

int pthread_mutex_init(pthread_mutex_t *, const pthread_mutexattr_t *);
int pthread_mutex_destroy(pthread_mutex_t *);
int pthread_mutex_lock(pthread_mutex_t *);
int pthread_mutex_trylock(pthread_mutex_t *);
int pthread_mutex_unlock(pthread_mutex_t *);

int pthread_mutexattr_init(pthread_mutexattr_t *);
int pthread_mutexattr_destroy(pthread_mutexattr_t *);
int pthread_mutexattr_settype(pthread_mutexattr_t *, int);
int pthread_mutexattr_gettype(const pthread_mutexattr_t *, int *);

int pthread_cond_init(pthread_cond_t *, const pthread_condattr_t *);
int pthread_cond_destroy(pthread_cond_t *);
int pthread_cond_wait(pthread_cond_t *, pthread_mutex_t *);
int pthread_cond_timedwait(pthread_cond_t *, pthread_mutex_t *, const struct timespec *);
int pthread_cond_signal(pthread_cond_t *);
int pthread_cond_broadcast(pthread_cond_t *);

int pthread_rwlock_init(pthread_rwlock_t *, const pthread_rwlockattr_t *);
int pthread_rwlock_destroy(pthread_rwlock_t *);
int pthread_rwlock_rdlock(pthread_rwlock_t *);
int pthread_rwlock_tryrdlock(pthread_rwlock_t *);
int pthread_rwlock_wrlock(pthread_rwlock_t *);
int pthread_rwlock_trywrlock(pthread_rwlock_t *);
int pthread_rwlock_unlock(pthread_rwlock_t *);

int pthread_key_create(pthread_key_t *, void (*)(void *));
int pthread_key_delete(pthread_key_t);
void *pthread_getspecific(pthread_key_t);
int pthread_setspecific(pthread_key_t, const void *);

int pthread_once(pthread_once_t *, void (*)(void));

int pthread_setname_np(pthread_t, const char *);

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_PTHREAD_H */
`, "\n"),
	},

	"dlfcn.h": {
		Description: "Dynamic linking API (dlopen, dlsym, dlclose).\n" +
			"Used for loading shared libraries at runtime. In wasm, these will be\n" +
			"unresolved imports since dynamic loading is not supported.",
		Content: strings.TrimLeft(`
#ifndef _WASMIFY_DLFCN_H
#define _WASMIFY_DLFCN_H

#ifdef __cplusplus
extern "C" {
#endif

#define RTLD_LAZY    0x0001
#define RTLD_NOW     0x0002
#define RTLD_GLOBAL  0x0100
#define RTLD_LOCAL   0x0000
#define RTLD_DEFAULT ((void *)0)
#define RTLD_NEXT    ((void *)-1)

void *dlopen(const char *, int);
void *dlsym(void *, const char *);
int dlclose(void *);
char *dlerror(void);

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_DLFCN_H */
`, "\n"),
	},

	"sys/wait.h": {
		Description: "Process wait API (waitpid, wait, WEXITSTATUS macros).\n" +
			"Used for waiting on child process termination.",
		Content: strings.TrimLeft(`
#ifndef _WASMIFY_SYS_WAIT_H
#define _WASMIFY_SYS_WAIT_H

#include <sys/types.h>

#ifdef __cplusplus
extern "C" {
#endif

#define WNOHANG   1
#define WUNTRACED 2

#define WEXITSTATUS(s) (((s) >> 8) & 0xff)
#define WTERMSIG(s)    ((s) & 0x7f)
#define WSTOPSIG(s)    WEXITSTATUS(s)
#define WIFEXITED(s)   (WTERMSIG(s) == 0)
#define WIFSIGNALED(s) (((signed char)(((s) & 0x7f) + 1) >> 1) > 0)
#define WIFSTOPPED(s)  (((s) & 0xff) == 0x7f)

pid_t wait(int *);
pid_t waitpid(pid_t, int *, int);

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_SYS_WAIT_H */
`, "\n"),
	},

	"sys/resource.h": {
		Description: "Resource usage and limits API (getrlimit, setrlimit, getrusage).\n" +
			"Used for querying and setting process resource limits (file descriptors, memory, etc.).",
		Content: strings.TrimLeft(`
#ifndef _WASMIFY_SYS_RESOURCE_H
#define _WASMIFY_SYS_RESOURCE_H

#include <sys/types.h>
#include <time.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef unsigned long long rlim_t;

#define RLIM_INFINITY (~0ULL)

#define RLIMIT_CPU     0
#define RLIMIT_FSIZE   1
#define RLIMIT_DATA    2
#define RLIMIT_STACK   3
#define RLIMIT_CORE    4
#define RLIMIT_NOFILE  7
#define RLIMIT_AS      9

#define RUSAGE_SELF     0
#define RUSAGE_CHILDREN (-1)

struct rlimit {
    rlim_t rlim_cur;
    rlim_t rlim_max;
};

struct rusage {
    struct timeval ru_utime;
    struct timeval ru_stime;
    long ru_maxrss;
    long ru_ixrss;
    long ru_idrss;
    long ru_isrss;
    long ru_minflt;
    long ru_majflt;
    long ru_nswap;
    long ru_inblock;
    long ru_oublock;
    long ru_msgsnd;
    long ru_msgrcv;
    long ru_nsignals;
    long ru_nvcsw;
    long ru_nivcsw;
};

int getrlimit(int, struct rlimit *);
int setrlimit(int, const struct rlimit *);
int getrusage(int, struct rusage *);

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_SYS_RESOURCE_H */
`, "\n"),
	},

	"termios.h": {
		Description: "Terminal I/O control API (tcgetattr, tcsetattr, struct termios).\n" +
			"Used for configuring serial/terminal device parameters.",
		Content: strings.TrimLeft(`
#ifndef _WASMIFY_TERMIOS_H
#define _WASMIFY_TERMIOS_H

#ifdef __cplusplus
extern "C" {
#endif

typedef unsigned int tcflag_t;
typedef unsigned char cc_t;
typedef unsigned int speed_t;

#define NCCS 32

struct termios {
    tcflag_t c_iflag;
    tcflag_t c_oflag;
    tcflag_t c_cflag;
    tcflag_t c_lflag;
    cc_t     c_cc[NCCS];
};

#define TCSANOW   0
#define TCSADRAIN 1
#define TCSAFLUSH 2

int tcgetattr(int, struct termios *);
int tcsetattr(int, int, const struct termios *);

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_TERMIOS_H */
`, "\n"),
	},

	"sys/mman.h": {
		Description: "Memory mapping API (mmap, munmap, mprotect).\n" +
			"Used for mapping files into memory and managing memory protection.\n" +
			"Note: wasi-sdk provides a partial emulation via _WASI_EMULATED_MMAN.",
		Content: strings.TrimLeft(`
#ifndef _WASMIFY_SYS_MMAN_H
#define _WASMIFY_SYS_MMAN_H

#include <stddef.h>
#include <sys/types.h>

#ifdef __cplusplus
extern "C" {
#endif

#define PROT_NONE   0x0
#define PROT_READ   0x1
#define PROT_WRITE  0x2
#define PROT_EXEC   0x4

#define MAP_SHARED    0x01
#define MAP_PRIVATE   0x02
#define MAP_FIXED     0x10
#define MAP_ANONYMOUS 0x20
#define MAP_ANON      MAP_ANONYMOUS
#define MAP_FAILED    ((void *)-1)

#define MS_ASYNC      1
#define MS_SYNC       4
#define MS_INVALIDATE 2

void *mmap(void *, size_t, int, int, int, off_t);
int munmap(void *, size_t);
int mprotect(void *, size_t, int);
int msync(void *, size_t, int);
int madvise(void *, size_t, int);

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_SYS_MMAN_H */
`, "\n"),
	},

	"net/if.h": {
		Description: "Network interface identification API (if_nametoindex, struct if_nameindex).\n" +
			"Used for mapping between network interface names and indices.",
		Content: strings.TrimLeft(`
#ifndef _WASMIFY_NET_IF_H
#define _WASMIFY_NET_IF_H

#ifdef __cplusplus
extern "C" {
#endif

#define IF_NAMESIZE 16

struct if_nameindex {
    unsigned int if_index;
    char        *if_name;
};

unsigned int if_nametoindex(const char *);
char *if_indextoname(unsigned int, char *);
struct if_nameindex *if_nameindex(void);
void if_freenameindex(struct if_nameindex *);

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_NET_IF_H */
`, "\n"),
	},

	"sys/select.h": {
		Description: "Synchronous I/O multiplexing API (select, fd_set, FD_SET macros).\n" +
			"Used for monitoring multiple file descriptors for readiness.",
		Content: strings.TrimLeft(`
#ifndef _WASMIFY_SYS_SELECT_H
#define _WASMIFY_SYS_SELECT_H

#include <sys/types.h>
#include <time.h>

#ifdef __cplusplus
extern "C" {
#endif

#ifndef FD_SETSIZE
#define FD_SETSIZE 1024
#endif

typedef struct {
    unsigned long fds_bits[FD_SETSIZE / (8 * sizeof(unsigned long))];
} fd_set;

#define FD_ZERO(s)   do { unsigned long *_p = (s)->fds_bits; int _n = FD_SETSIZE / (8 * sizeof(unsigned long)); while (_n) { *_p++ = 0; _n--; } } while(0)
#define FD_SET(d, s)   ((s)->fds_bits[(d) / (8 * sizeof(unsigned long))] |= (1UL << ((d) % (8 * sizeof(unsigned long)))))
#define FD_CLR(d, s)   ((s)->fds_bits[(d) / (8 * sizeof(unsigned long))] &= ~(1UL << ((d) % (8 * sizeof(unsigned long)))))
#define FD_ISSET(d, s) ((s)->fds_bits[(d) / (8 * sizeof(unsigned long))] & (1UL << ((d) % (8 * sizeof(unsigned long)))))

int select(int, fd_set *, fd_set *, fd_set *, struct timeval *);

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_SYS_SELECT_H */
`, "\n"),
	},

	"syslog.h": {
		Description: "System logging API (openlog, syslog, closelog).\n" +
			"Used for writing messages to the system log daemon.",
		Content: strings.TrimLeft(`
#ifndef _WASMIFY_SYSLOG_H
#define _WASMIFY_SYSLOG_H

#ifdef __cplusplus
extern "C" {
#endif

#define LOG_EMERG   0
#define LOG_ALERT   1
#define LOG_CRIT    2
#define LOG_ERR     3
#define LOG_WARNING 4
#define LOG_NOTICE  5
#define LOG_INFO    6
#define LOG_DEBUG   7

#define LOG_KERN     (0<<3)
#define LOG_USER     (1<<3)
#define LOG_DAEMON   (3<<3)
#define LOG_LOCAL0   (16<<3)
#define LOG_LOCAL7   (23<<3)

#define LOG_PID    0x01
#define LOG_CONS   0x02
#define LOG_NDELAY 0x08
#define LOG_NOWAIT 0x10

void openlog(const char *, int, int);
void syslog(int, const char *, ...);
void closelog(void);

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_SYSLOG_H */
`, "\n"),
	},

	"semaphore.h": {
		Description: "POSIX semaphore API (sem_init, sem_wait, sem_post).\n" +
			"Used for inter-thread/inter-process synchronization.",
		Content: strings.TrimLeft(`
#ifndef _WASMIFY_SEMAPHORE_H
#define _WASMIFY_SEMAPHORE_H

#include <time.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct { int __val; } sem_t;

#define SEM_FAILED ((sem_t *)0)

int sem_init(sem_t *, int, unsigned int);
int sem_destroy(sem_t *);
int sem_wait(sem_t *);
int sem_trywait(sem_t *);
int sem_timedwait(sem_t *, const struct timespec *);
int sem_post(sem_t *);
int sem_getvalue(sem_t *, int *);
sem_t *sem_open(const char *, int, ...);
int sem_close(sem_t *);
int sem_unlink(const char *);

#ifdef __cplusplus
}
#endif

#endif /* _WASMIFY_SEMAPHORE_H */
`, "\n"),
	},
}
