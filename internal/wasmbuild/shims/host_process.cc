/* host_process.cc — who the process is: its ids, its user and groups, and
 * the user and group databases.
 *
 * WASI has none of this, and wasi-libc declares none of it. The host
 * answers through imports: a guest that runs as the host process (a command
 * line tool) gets the host's ids and databases, and an isolated one gets a
 * fixed identity — pid 42, parent 1, user and group 0 — and empty
 * databases. A process made by fork has the pid the host forked it under,
 * and for a parent the process that forked it, or 1 once that one is gone.
 *
 * Nothing is cached here: an instance can be a copy (a memory snapshot) of
 * one that asked a different host.
 *
 * Records cross from the host in binary form: little-endian u32 fields, and
 * strings as a u32 length followed by the bytes.
 *
 * Also here: process groups and sessions, the scheduling priority, the
 * system's name (uname), and the environment, whose changes the host is
 * told about.
 */
#ifdef WASMIFY_HOST_SUBPROCESS

#include <sys/types.h>
#include <cerrno>
#include <cstdint>
#include <cstdlib>
#include <cstring>
#include <unistd.h>

#include <signal.h>
#include <sys/utsname.h>

/* wasi-libc has no <pwd.h> and <grp.h>; wasmify's stubs are beside this file */
#include "pwd.h"
#include "grp.h"

#include "wasmify_posix.h"

namespace {

enum { ID_PID, ID_PPID, ID_UID, ID_EUID, ID_GID, ID_EGID };

uint32_t process_id(int which) {
    uint32_t ids[6] = {0, 0, 0, 0, 0, 0};
    __wasmify_host_proc_ids(ids);
    return ids[which];
}

/* A record fetched from the host. The lookup functions return pointers
 * into it, which stay valid until the next lookup of the same kind — the
 * contract of getpwuid and friends. */
struct Record {
    char *data;
    uint32_t cap;
    uint32_t len;
    uint32_t pos;
};

/* Runs one lookup, growing the buffer to the size the host asks for.
 * Returns true when a record was found. */
template <typename Lookup>
bool fetch(Record &rec, Lookup lookup) {
    for (;;) {
        uint32_t needed = 0;
        const int rc = lookup(rec.data, (int)rec.cap, &needed);
        if (rc == 0) {
            rec.len = needed;
            rec.pos = 0;
            return true;
        }
        if (rc != -ERANGE || needed <= rec.cap) return false;
        char *grown = (char *)realloc(rec.data, needed);
        if (!grown) {
            errno = ENOMEM;
            return false;
        }
        rec.data = grown;
        rec.cap = needed;
    }
}

bool next_u32(Record &rec, uint32_t *out) {
    if (rec.len - rec.pos < 4) return false;
    memcpy(out, rec.data + rec.pos, 4);
    rec.pos += 4;
    return true;
}

/* Returns the next string of the record as a C string. The terminator is
 * written over the first byte of the length field that preceded the string
 * (already consumed), after the string has been moved down onto it. */
char *next_string(Record &rec) {
    uint32_t n = 0;
    if (!next_u32(rec, &n) || rec.len - rec.pos < n) return NULL;
    char *s = rec.data + rec.pos - 4;
    memmove(s, rec.data + rec.pos, n);
    s[n] = '\0';
    rec.pos += n;
    return s;
}

Record g_user_rec;
struct passwd g_user;
char g_empty[1] = "";

struct passwd *decode_user(bool found) {
    if (!found) return NULL;
    uint32_t uid = 0, gid = 0;
    if (!next_u32(g_user_rec, &uid) || !next_u32(g_user_rec, &gid)) return NULL;
    g_user.pw_uid = (uid_t)uid;
    g_user.pw_gid = (gid_t)gid;
    g_user.pw_name = next_string(g_user_rec);
    g_user.pw_gecos = next_string(g_user_rec);
    g_user.pw_dir = next_string(g_user_rec);
    g_user.pw_shell = next_string(g_user_rec);
    /* The host does not hand out password fields. */
    g_user.pw_passwd = g_empty;
    if (!g_user.pw_name || !g_user.pw_gecos || !g_user.pw_dir || !g_user.pw_shell)
        return NULL;
    return &g_user;
}

Record g_group_rec;
struct group g_group;
char *g_no_members[1] = {NULL};

struct group *decode_group(bool found) {
    if (!found) return NULL;
    uint32_t gid = 0;
    if (!next_u32(g_group_rec, &gid)) return NULL;
    g_group.gr_gid = (gid_t)gid;
    g_group.gr_name = next_string(g_group_rec);
    g_group.gr_passwd = g_empty;
    /* The host reports a group's id and name, not its members. */
    g_group.gr_mem = g_no_members;
    return g_group.gr_name ? &g_group : NULL;
}

/* The requests of the proc_setids import. */
enum {
    SET_UID, SET_EUID, SET_REUID, SET_RESUID, SET_GID, SET_EGID, SET_REGID, SET_RESGID
};

/* Changes ids of the process. A process that is the host process asks the
 * host, which decides as it does for any process. An isolated one has no
 * privileges: changing an id to the value it already has is all it may do.
 * (uid_t)-1 leaves an id alone; the saved id starts out as the effective
 * one. */
int set_ids(int request, uid_t a, uid_t b, uid_t c, uid_t want_real, uid_t want_effective,
            uid_t want_saved, int real_which, int effective_which) {
    const int rc = __wasmify_host_proc_setids(request, (int)a, (int)b, (int)c);
    if (rc == 0) return 0;
    if (rc != -ENOSYS) {
        errno = -rc;
        return -1;
    }
    if ((want_real != (uid_t)-1 && want_real != process_id(real_which)) ||
        (want_effective != (uid_t)-1 && want_effective != process_id(effective_which)) ||
        (want_saved != (uid_t)-1 && want_saved != process_id(effective_which))) {
        errno = EPERM;
        return -1;
    }
    return 0;
}

const uid_t kKeep = (uid_t)-1;

} /* namespace */

extern "C" {

pid_t getpid(void) { return (pid_t)process_id(ID_PID); }
pid_t getppid(void) { return (pid_t)process_id(ID_PPID); }

uid_t getuid(void) { return (uid_t)process_id(ID_UID); }
uid_t geteuid(void) { return (uid_t)process_id(ID_EUID); }
gid_t getgid(void) { return (gid_t)process_id(ID_GID); }
gid_t getegid(void) { return (gid_t)process_id(ID_EGID); }

int setuid(uid_t uid) {
    return set_ids(SET_UID, uid, kKeep, kKeep, uid, uid, kKeep, ID_UID, ID_EUID);
}
int seteuid(uid_t uid) {
    return set_ids(SET_EUID, uid, kKeep, kKeep, kKeep, uid, kKeep, ID_UID, ID_EUID);
}
int setreuid(uid_t ruid, uid_t euid) {
    return set_ids(SET_REUID, ruid, euid, kKeep, ruid, euid, kKeep, ID_UID, ID_EUID);
}
int setresuid(uid_t ruid, uid_t euid, uid_t suid) {
    return set_ids(SET_RESUID, ruid, euid, suid, ruid, euid, suid, ID_UID, ID_EUID);
}
int setgid(gid_t gid) {
    return set_ids(SET_GID, gid, kKeep, kKeep, gid, gid, kKeep, ID_GID, ID_EGID);
}
int setegid(gid_t gid) {
    return set_ids(SET_EGID, gid, kKeep, kKeep, kKeep, gid, kKeep, ID_GID, ID_EGID);
}
int setregid(gid_t rgid, gid_t egid) {
    return set_ids(SET_REGID, rgid, egid, kKeep, rgid, egid, kKeep, ID_GID, ID_EGID);
}
int setresgid(gid_t rgid, gid_t egid, gid_t sgid) {
    return set_ids(SET_RESGID, rgid, egid, sgid, rgid, egid, sgid, ID_GID, ID_EGID);
}

int getgroups(int size, gid_t list[]) {
    uint32_t count = 0;
    if (size < 0) {
        errno = EINVAL;
        return -1;
    }
    /* gid_t is the u32 the host writes */
    const int rc = __wasmify_host_proc_groups((uint32_t *)list, size, &count);
    if (rc == -ERANGE) {
        errno = EINVAL; /* what getgroups reports for a list that is too short */
        return -1;
    }
    if (rc != 0) {
        errno = -rc;
        return -1;
    }
    return (int)count;
}

int setgroups(size_t size, const gid_t *list) {
    /* gid_t is the u32 the host reads; a process that is not the host
     * process has no privilege to change its groups */
    const int rc = __wasmify_host_proc_setgroups((const uint32_t *)list, (int)size);
    if (rc != 0) {
        errno = rc == -ENOSYS ? EPERM : -rc;
        return -1;
    }
    return 0;
}

struct passwd *getpwuid(uid_t uid) {
    return decode_user(fetch(g_user_rec, [uid](void *buf, int len, uint32_t *needed) {
        return __wasmify_host_proc_user_by_id((uint32_t)uid, buf, len, needed);
    }));
}

struct passwd *getpwnam(const char *name) {
    if (!name) return NULL;
    return decode_user(fetch(g_user_rec, [name](void *buf, int len, uint32_t *needed) {
        return __wasmify_host_proc_user_by_name(name, (int)strlen(name), buf, len, needed);
    }));
}

struct group *getgrgid(gid_t gid) {
    return decode_group(fetch(g_group_rec, [gid](void *buf, int len, uint32_t *needed) {
        return __wasmify_host_proc_group_by_id((uint32_t)gid, buf, len, needed);
    }));
}

struct group *getgrnam(const char *name) {
    if (!name) return NULL;
    return decode_group(fetch(g_group_rec, [name](void *buf, int len, uint32_t *needed) {
        return __wasmify_host_proc_group_by_name(name, (int)strlen(name), buf, len, needed);
    }));
}

/* Walking the databases: the entries of the host's, one after the other,
 * from the first again after setpwent, setgrent or the end of the walk. A
 * process that is not the host process has none. */
static int g_user_walk = 0;
static int g_group_walk = 0;

void setpwent(void) { g_user_walk = 0; }
void endpwent(void) { g_user_walk = 0; }
struct passwd *getpwent(void) {
    const int index = g_user_walk++;
    struct passwd *pw = decode_user(fetch(g_user_rec, [index](void *buf, int len, uint32_t *needed) {
        return __wasmify_host_proc_user_at(index, buf, len, needed);
    }));
    if (!pw) g_user_walk = 0;
    return pw;
}
void setgrent(void) { g_group_walk = 0; }
void endgrent(void) { g_group_walk = 0; }
struct group *getgrent(void) {
    const int index = g_group_walk++;
    struct group *gr = decode_group(fetch(g_group_rec, [index](void *buf, int len, uint32_t *needed) {
        return __wasmify_host_proc_group_at(index, buf, len, needed);
    }));
    if (!gr) g_group_walk = 0;
    return gr;
}

/* The login name is that of the user the process runs as. */
char *getlogin(void) {
    struct passwd *pw = getpwuid(getuid());
    if (!pw) {
        errno = ENOENT;
        return NULL;
    }
    return pw->pw_name;
}

} /* extern "C" */

/* ---- process groups and sessions, priority, the system's name, the
 * environment ---------------------------------------------------------------- */
extern "C" {
int __real_setenv(const char *name, const char *value, int overwrite);
int __real_unsetenv(const char *name);
int __real_putenv(char *string);
int __real_clearenv(void);
extern char **environ;
} /* extern "C" */

namespace {

int fail(int error) {
    errno = error;
    return -1;
}

/* The fields of uname as the host reports them; false when the process is
 * a system of its own. */
bool host_uname(struct utsname *out) {
    char rec[2048];
    uint32_t needed = 0;
    if (__wasmify_host_proc_uname(rec, (int)sizeof(rec), &needed) != 0) return false;
    char *fields[5] = {out->sysname, out->nodename, out->release, out->version, out->machine};
    const size_t sizes[5] = {sizeof(out->sysname), sizeof(out->nodename), sizeof(out->release),
                             sizeof(out->version), sizeof(out->machine)};
    uint32_t pos = 0;
    for (int i = 0; i < 5; i++) {
        uint32_t n = 0;
        if (needed - pos < 4) return false;
        memcpy(&n, rec + pos, 4);
        pos += 4;
        if (needed - pos < n) return false;
        const size_t kept = n < sizes[i] - 1 ? n : sizes[i] - 1;
        memcpy(fields[i], rec + pos, kept);
        fields[i][kept] = '\0';
        pos += n;
    }
    return true;
}

void report_env(const char *name, size_t name_len, const char *value) {
    __wasmify_host_proc_setenv(name, (int)name_len, value ? value : "",
                          value ? (int)strlen(value) : 0, value != NULL);
}

} /* namespace */

extern "C" {

/* ---- the environment --------------------------------------------------------
 * The environment is libc's own table. Every change to it goes through
 * these (the link wraps the libc functions), which pass it on to the host
 * once libc has made it. */
int __wrap_setenv(const char *name, const char *value, int overwrite) {
    const int rc = __real_setenv(name, value, overwrite);
    if (rc == 0) report_env(name, strlen(name), getenv(name));
    return rc;
}

int __wrap_unsetenv(const char *name) {
    const int rc = __real_unsetenv(name);
    if (rc == 0) report_env(name, strlen(name), NULL);
    return rc;
}

int __wrap_putenv(char *string) {
    const int rc = __real_putenv(string);
    if (rc == 0) {
        const char *eq = strchr(string, '=');
        if (eq)
            report_env(string, (size_t)(eq - string), eq + 1);
        else
            report_env(string, strlen(string), NULL);
    }
    return rc;
}

int __wrap_clearenv(void) {
    /* the names first: they are gone once libc has cleared the table */
    if (environ) {
        for (char **e = environ; *e; e++) {
            const char *eq = strchr(*e, '=');
            report_env(*e, eq ? (size_t)(eq - *e) : strlen(*e), NULL);
        }
    }
    return __real_clearenv();
}

/* ---- process groups and sessions ---------------------------------------------
 * The host keeps them: the processes of a program are its to know. */
pid_t getpgid(pid_t pid) {
    int pgid = 0;
    const int rc = __wasmify_host_proc_getpgid((int)pid, &pgid);
    if (rc != 0) return (pid_t)fail(-rc);
    return (pid_t)pgid;
}

pid_t getpgrp(void) { return getpgid(0); }

int setpgid(pid_t pid, pid_t pgid) {
    const int rc = __wasmify_host_proc_setpgid((int)pid, (int)pgid);
    if (rc != 0) return fail(-rc);
    return 0;
}

pid_t setsid(void) {
    int sid = 0;
    const int rc = __wasmify_host_proc_setsid(&sid);
    if (rc != 0) return (pid_t)fail(-rc);
    return (pid_t)sid;
}

int killpg(pid_t pgrp, int sig) {
    if (pgrp < 0) return fail(EINVAL);
    /* group 0 is the caller's own, as kill has it */
    return kill(pgrp > 1 ? -pgrp : 0, sig);
}

/* ---- scheduling priority ---------------------------------------------------
 * A process of the host has the host process's; an isolated one has none
 * (ENOSYS). */
int getpriority(int which, id_t who) {
    int nice_value = 0;
    const int rc = __wasmify_host_proc_priority(which, (int)who, 0, 0, &nice_value);
    if (rc != 0) return fail(-rc);
    errno = 0; /* -1 is a legitimate value */
    return nice_value;
}

int setpriority(int which, id_t who, int value) {
    int nice_value = 0;
    const int rc = __wasmify_host_proc_priority(which, (int)who, 1, value, &nice_value);
    if (rc != 0) return fail(-rc);
    return 0;
}

int nice(int increment) {
    const int process = 0; /* PRIO_PROCESS, which wasi-libc does not define */
    int nice_value = 0;
    int rc = __wasmify_host_proc_priority(process, 0, 0, 0, &nice_value);
    if (rc == 0)
        rc = __wasmify_host_proc_priority(process, 0, 1, nice_value + increment, &nice_value);
    if (rc != 0) return fail(rc == -EACCES ? EPERM : -rc);
    errno = 0; /* -1 is a legitimate value */
    return nice_value;
}

/* ---- the system's name --------------------------------------------------------
 * A process that is the host process is on the host's system: uname and
 * gethostname (which libc answers from uname) report the host's. An
 * isolated one is a system of its own, with the names wasi-libc gives. */
int uname(struct utsname *out) {
    if (!out) return fail(EFAULT);
    if (host_uname(out)) return 0;
    strcpy(out->sysname, "wasi");
    strcpy(out->nodename, "(none)");
    strcpy(out->release, "0.0.0");
    strcpy(out->version, "0.0.0");
#ifdef __wasm64__
    strcpy(out->machine, "wasm64");
#else
    strcpy(out->machine, "wasm32");
#endif
    return 0;
}

/* The login name, as getlogin has it (host_process.cc). */
char *cuserid(char *buf) {
    static char name[64];
    const char *login = getlogin();
    if (!buf) buf = name;
    if (!login || strlen(login) >= sizeof(name)) {
        buf[0] = '\0';
        return NULL;
    }
    strcpy(buf, login);
    return buf;
}

} /* extern "C" */

#endif /* WASMIFY_HOST_SUBPROCESS */
