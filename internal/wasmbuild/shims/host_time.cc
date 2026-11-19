/* host_time.cc — time zones, for a C library that has one (UTC).
 *
 * Compiled when host subprocess support is opted in (WASMIFY_HOST_SUBPROCESS).
 */
#ifdef WASMIFY_HOST_SUBPROCESS

#include <cstddef>
#include <cstdint>
#include <cstdlib>
#include <cstring>
#include <time.h>

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

/* A struct tm points at its zone's abbreviation (tm_zone), so the
 * abbreviations a program has met stay where they are for its lifetime.
 * There are few of them: a zone has one or two, and a program names few
 * zones. When the table is full the abbreviation is not kept, and the
 * struct tm gets none. */
const size_t zone_name_limit = 64;
char *zone_names[zone_name_limit];
size_t zone_name_count = 0;

const char *zone_name(const char *name) {
    for (size_t i = 0; i < zone_name_count; i++)
        if (strcmp(zone_names[i], name) == 0) return zone_names[i];
    if (zone_name_count == zone_name_limit) return "";
    char *kept = strdup(name);
    if (!kept) return "";
    zone_names[zone_name_count++] = kept;
    return kept;
}

/* Asks the host about the zone TZ names right now. */
bool zone_at(long long t, int local, struct __wasmify_zone *zone) {
    const char *tz = getenv("TZ");
    return __wasmify_host_clock_zone(tz, tz ? (int)strlen(tz) : 0, tz != NULL, (int64_t)t, local,
                                zone) == 0;
}

} /* namespace */

extern "C" {

/* ---- time zones ------------------------------------------------------------
 * wasi-libc has one time zone, UTC: its localtime, mktime and strftime ask
 * two internal functions what holds in "the" zone, and its own answers are
 * constant. The definitions below take their place (the object that holds
 * libc's is then not linked), so that the same libc functions follow TZ:
 * the zone's rules are the host's to know (the clock_zone import), named by
 * the value TZ has at the moment of the call — a program may change
 * $ENV{TZ} at any time. With TZ unset the host decides where the program
 * is. tzset, tzname, timezone and daylight, which wasi-libc leaves out, are
 * defined on the same answer. */

/* libc's name for UTC, which gmtime gives tm_zone. (Declared first: a
 * const object has to be declared extern to be visible to libc.) */
extern const char __utc[];
const char __utc[] = "UTC";

char *tzname[2] = {(char *)__utc, (char *)__utc};
long timezone = 0;
int daylight = 0;

/* What holds in the zone at t; libc's contract. With local set, t is a
 * reading of the zone's clock (mktime's starting point) and *oppoff
 * receives the offset under the opposite daylight-saving state. */
void __secs_to_zone(long long t, int local, int *isdst, long *offset, long *oppoff,
                    const char **zonename) {
    struct __wasmify_zone zone;
    if (!zone_at(t, local, &zone)) {
        *isdst = 0;
        *offset = 0;
        if (oppoff) *oppoff = 0;
        *zonename = __utc;
        return;
    }
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
    for (size_t i = 0; i < zone_name_count; i++)
        if (name == zone_names[i]) return name;
    return "";
}

void tzset(void) {
    struct __wasmify_zone zone;
    if (!zone_at((long long)time(NULL), 0, &zone)) return;
    const char *standard = zone_name(zone.std_name);
    tzname[0] = (char *)standard;
    tzname[1] = (char *)(zone.dst_name[0] ? zone_name(zone.dst_name) : standard);
    timezone = -(long)zone.std_offset;
    daylight = zone.dst_name[0] != 0;
}

} /* extern "C" */

#endif /* WASMIFY_HOST_SUBPROCESS */
