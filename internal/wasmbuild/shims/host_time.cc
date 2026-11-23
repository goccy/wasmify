/* host_time.cc — time zones, for a C library that has one (UTC).
 *
 * Compiled when the host capability system.time_zones is given (WASMIFY_HOST_SYSTEM_TIME_ZONES).
 */
#ifdef WASMIFY_HOST_SYSTEM_TIME_ZONES

#include <cstddef>
#include <cstdint>
#include <cstdlib>
#include <cstring>
#include <time.h>
#ifdef WASMIFY_HOST_PROCESS_THREADS
#include <pthread.h>
#endif

#include "wasmify_posix.h"

/* What holds in a time zone at one moment, as the host reports it: the
 * offset from UTC in seconds east, whether daylight saving time is in
 * effect, the offset under the opposite daylight-saving state, the offset
 * of standard time, and the abbreviations in use, of standard time and of
 * daylight saving time (empty when the zone has none). clock_zone fills it:
 * tz is the zone the program names (the value of TZ) when has_tz is set;
 * t is seconds since the epoch, or with local set a reading of the zone's
 * own clock. */
struct __wasmify_zone {
    int32_t offset;
    int32_t is_dst;
    int32_t other_offset;
    int32_t std_offset;
    char name[16];
    char std_name[16];
    char dst_name[16];
};

namespace {

/* The lock of the state below (musl's __tz.c has one too), where the
 * program has threads (process.threads). A default mutex fails only when it
 * is misused, which these pairs do not. */
#ifdef WASMIFY_HOST_PROCESS_THREADS
pthread_mutex_t zone_lock = PTHREAD_MUTEX_INITIALIZER;
struct zone_locked {
    zone_locked() { pthread_mutex_lock(&zone_lock); }
    ~zone_locked() { pthread_mutex_unlock(&zone_lock); }
};
#else
struct zone_locked {};
#endif

/* A struct tm points at its zone's abbreviation (tm_zone), so the
 * abbreviations a program has met stay where they are for its lifetime. */
char **zone_names = NULL;
size_t zone_name_count = 0;
size_t zone_name_cap = 0;

/* The kept copy of an abbreviation, or "" when there is no memory to keep
 * it: libc's callers of the zone functions return nothing that could carry
 * the error, and a struct tm without an abbreviation is still right. */
const char *zone_name(const char *name) {
    for (size_t i = 0; i < zone_name_count; i++)
        if (strcmp(zone_names[i], name) == 0) return zone_names[i];
    if (zone_name_count == zone_name_cap) {
        const size_t cap = zone_name_cap == 0 ? 1 : zone_name_cap * 2;
        char **grown = (char **)realloc(zone_names, cap * sizeof(char *));
        if (!grown) return "";
        zone_names = grown;
        zone_name_cap = cap;
    }
    char *kept = strdup(name);
    if (!kept) return "";
    zone_names[zone_name_count++] = kept;
    return kept;
}

/* Asks the host about the zone TZ names at t. clock_zone fails only for
 * memory out of range (EFAULT), which zone is not. */
void zone_at(long long t, int local, struct __wasmify_zone *zone) {
    const char *tz = getenv("TZ");
    (void)__wasmify_host_clock_zone(tz, tz ? (int)strlen(tz) : 0, tz != NULL, (int64_t)t, local,
                                    zone);
}

/* The value of TZ that tzname, timezone and daylight were last set for:
 * tz_known is whether they were set for a value that was kept, tz_unset
 * whether TZ was unset then, and tz_value its value otherwise. A value
 * there was no memory to keep leaves tz_known false, and the next call
 * sets them again. */
bool tz_known = false;
bool tz_unset = false;
char *tz_value = NULL;

/* Whether TZ is what tzname, timezone and daylight were last set for. */
bool tz_current() {
    if (!tz_known) return false;
    const char *tz = getenv("TZ");
    if (tz == NULL) return tz_unset;
    return !tz_unset && strcmp(tz, tz_value) == 0;
}

/* Remembers TZ as what tzname, timezone and daylight are set for. */
void tz_remember() {
    const char *tz = getenv("TZ");
    free(tz_value);
    tz_value = tz ? strdup(tz) : NULL;
    tz_unset = tz == NULL;
    tz_known = tz == NULL || tz_value != NULL;
}

/* tzset, with the lock held. */
void tzset_locked() {
    struct __wasmify_zone zone;
    zone_at((long long)time(NULL), 0, &zone);
    const char *standard = zone_name(zone.std_name);
    tzname[0] = (char *)standard;
    tzname[1] = (char *)(zone.dst_name[0] ? zone_name(zone.dst_name) : standard);
    timezone = -(long)zone.std_offset;
    daylight = zone.dst_name[0] != 0;
    tz_remember();
}

} /* namespace */

extern "C" {

/* ---- time zones ------------------------------------------------------------
 * wasi-libc has one time zone, UTC: its localtime, mktime and strftime ask
 * two internal functions what holds in "the" zone, and its own answers are
 * constant. The definitions below take their place (the object that holds
 * libc's is then not linked), so that the same libc functions follow TZ:
 * the zone's rules are the host's to know (the clock_zone import), named by
 * the value TZ has at the moment of the call. With TZ unset the host
 * decides where the program is. tzset, tzname, timezone and daylight,
 * which wasi-libc leaves out, are defined on the same answer. */

/* libc's name for UTC, which gmtime gives tm_zone. (Declared first: a
 * const object has to be declared extern to be visible to libc.) */
extern const char __utc[];
const char __utc[] = "UTC";

char *tzname[2] = {(char *)__utc, (char *)__utc};
long timezone = 0;
int daylight = 0;

/* What holds in the zone at t; libc's contract. With local set, t is a
 * reading of the zone's clock (mktime's starting point) and *oppoff
 * receives the offset under the opposite daylight-saving state. localtime
 * and mktime behave as though they called tzset: a TZ other than the one
 * tzname, timezone and daylight are set for sets them first. */
void __secs_to_zone(long long t, int local, int *isdst, long *offset, long *oppoff,
                    const char **zonename) {
    zone_locked locked;
    if (!tz_current()) tzset_locked();
    struct __wasmify_zone zone;
    zone_at(t, local, &zone);
    *isdst = zone.is_dst;
    *offset = zone.offset;
    if (oppoff) *oppoff = zone.other_offset;
    *zonename = zone_name(zone.name);
}

/* The abbreviation a struct tm carries, for strftime's %Z: tm_zone when it
 * is one this file (or gmtime) handed out, and nothing for a struct tm that
 * was filled in by hand. */
const char *__tm_to_tzname(const struct tm *tm) {
    const char *name = tm->tm_zone;
    if (name == __utc) return name;
    zone_locked locked;
    for (size_t i = 0; i < zone_name_count; i++)
        if (name == zone_names[i]) return name;
    return "";
}

void tzset(void) {
    zone_locked locked;
    tzset_locked();
}

} /* extern "C" */

#endif /* WASMIFY_HOST_SYSTEM_TIME_ZONES */
