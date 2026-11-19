/* Calls the libc functions wasmify's host shims implement and prints what
 * each did, under a host that runs the program as a process of its own
 * (not the host process): TestHostShimsRun compares the output. With the
 * argument "exec" it execs a command instead, whose exit status becomes
 * the program's. */
#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <netdb.h>
#include <poll.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ipc.h>
#include <sys/msg.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/time.h>
#include <sys/times.h>
#include <sys/utsname.h>
#include <syslog.h>
#include <termios.h>
#include <time.h>
#include <unistd.h>

#include "pwd.h"
#include "wasmify_posix.h"

/* functions the shims implement that wasi-libc's headers do not declare */
void closefrom(int low_fd);
pid_t getppid(void);
int getpriority(int which, id_t who);
void tzset(void);
extern char *tzname[2];
int chroot(const char *path);
int mkfifo(const char *path, mode_t mode);
int fchown(int fd, uid_t owner, gid_t group);
int chown(const char *path, uid_t owner, gid_t group);
int fchdir(int fd);
char *ctermid(char *buf);
char *ttyname(int fd);

/* the shims call this for a message no logger took */
static int undelivered = -1;
void __wasmify_syslog_undelivered(int error) { undelivered = error; }

static volatile int usr1;
static void on_usr1(int sig) {
    (void)sig;
    usr1++;
}

static const char *error_name(int e) {
    switch (e) {
    case 0: return "0";
    case ENOSYS: return "ENOSYS";
    case EPERM: return "EPERM";
    case ENOTTY: return "ENOTTY";
    case ENOENT: return "ENOENT";
    case EACCES: return "EACCES";
    case ENODEV: return "ENODEV";
    default: return "other";
    }
}

int main(int argc, char **argv) {
    if (argc > 1 && strcmp(argv[1], "exec") == 0) {
        char *args[] = {"sh", "-c", "exit 3", NULL};
        execv("/bin/sh", args);
        printf("execv: %s\n", error_name(errno));
        return 1;
    }

    /* who the process is */
    printf("ids: %d %d %d %d\n", (int)getpid(), (int)getppid(), (int)getuid(), (int)getgid());
    printf("user 0: %s\n", getpwuid(0) ? "found" : "none");
    setenv("WASMIFY_TEST", "1", 1);
    printf("setenv: %s\n", getenv("WASMIFY_TEST"));
    unsetenv("WASMIFY_TEST");
    printf("unsetenv: %s\n", getenv("WASMIFY_TEST") ? "set" : "unset");
    struct utsname u;
    printf("uname: %d %s\n", uname(&u), u.sysname);
    errno = 0;
    const int prio = getpriority(0, 0);
    printf("getpriority: %d %s\n", prio, error_name(errno));

    /* signals */
    struct sigaction sa;
    memset(&sa, 0, sizeof(sa));
    sa.sa_handler = on_usr1;
    sigaction(SIGUSR1, &sa, NULL);
    raise(SIGUSR1);
    printf("raise: %d\n", usr1);
    sigset_t set, pending;
    sigemptyset(&set);
    sigaddset(&set, SIGUSR1);
    sigprocmask(SIG_BLOCK, &set, NULL);
    kill(getpid(), SIGUSR1);
    sigpending(&pending);
    printf("blocked: %d %d\n", usr1, sigismember(&pending, SIGUSR1));
    sigprocmask(SIG_UNBLOCK, &set, NULL);
    printf("unblocked: %d\n", usr1);
    struct itimerval it, cur;
    memset(&it, 0, sizeof(it));
    it.it_value.tv_sec = 10;
    setitimer(ITIMER_REAL, &it, NULL);
    getitimer(ITIMER_REAL, &cur);
    printf("itimer: %d\n", cur.it_value.tv_sec >= 9 && cur.it_value.tv_sec <= 10);
    printf("alarm: %u\n", alarm(0));
    struct tms process_times;
    printf("times: %d\n", times(&process_times) != (clock_t)-1);

    /* terminals */
    struct termios settings;
    const int got = tcgetattr(0, &settings);
    printf("tcgetattr: %d %s\n", got, error_name(errno));
    printf("ctermid: %s\n", ctermid(NULL));

    /* time zones */
    setenv("TZ", "UTC", 1);
    tzset();
    time_t epoch = 0;
    char text[64];
    strftime(text, sizeof(text), "%Y-%m-%d %H %Z", localtime(&epoch));
    printf("tz: %s %s\n", text, tzname[0]);

    /* syslog */
    openlog("t", 0, LOG_USER);
    printf("openlog: %s\n", undelivered < 0 ? "delivered" : error_name(undelivered));
    closelog();

    /* System V IPC */
    errno = 0;
    const int queue = msgget(IPC_PRIVATE, 0600);
    printf("msgget: %s\n", queue >= 0 ? "ok" : error_name(errno));
    if (queue >= 0) msgctl(queue, IPC_RMID, NULL);

    /* files */
    char name[] = "/wasmifyXXXXXX";
    const int fd = mkstemp(name);
    printf("mkstemp: %d\n", fd >= 0);
    printf("fchown unchanged: %d\n", fchown(fd, (uid_t)-1, (gid_t)-1));
    errno = 0;
    printf("chown other: %d %s\n", chown(name, 12345, (gid_t)-1), error_name(errno));
    close(fd);
    unlink(name);
    errno = 0;
    printf("chroot: %d %s\n", chroot("/"), error_name(errno));
    const int dir = open("/", O_RDONLY | O_DIRECTORY);
    char cwd[256];
    printf("fchdir: %d %s\n", fchdir(dir), getcwd(cwd, sizeof(cwd)));
    close(dir);

    /* sockets and descriptors */
    struct protoent *tcp = getprotobyname("tcp");
    printf("getprotobyname: %d\n", tcp ? tcp->p_proto : -1);
    unsigned char mapped[16] = {0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 1, 2, 3, 4};
    printf("inet_ntop: %s\n", inet_ntop(AF_INET6, mapped, text, sizeof(text)));
    int pair[2];
    if (socketpair(AF_UNIX, SOCK_STREAM, 0, pair) == 0) {
        char c;
        printf("shutdown: %d %d\n", shutdown(pair[0], SHUT_WR), (int)read(pair[1], &c, 1));
        close(pair[0]);
        close(pair[1]);
    }
    int pipe_fds[2];
    pipe(pipe_fds);
    close(pipe_fds[1]);
    struct pollfd watched = {pipe_fds[0], 0, 0};
    printf("poll: %d %d\n", poll(&watched, 1, 1000), (watched.revents & POLLHUP) != 0);
    close(pipe_fds[0]);
    closefrom(100);
    return 0;
}
