#!/usr/bin/env bash
# verify-build-resource-matrix.sh — run `go build ./...` against
# go-googlesql with a swapped-in copy of googlesql.go inside a
# memory/CPU-constrained Docker container, capture exit code, wall
# time and peak RSS, and print a markdown table.
#
# Usage:
#   verify-build-resource-matrix.sh \
#     --label baseline --googlesql /abs/path/to/googlesql.go \
#     [--label after  --googlesql /abs/path/to/googlesql.go] \
#     [--memory 512m,1g,2g]
#
# Repeating --label / --googlesql adds another snapshot to the
# matrix. The script always runs each (snapshot × memory) cell three
# times and reports the median wall time. Exits non-zero if any cell
# fails for a snapshot whose label starts with "after" (the
# refactored side must succeed everywhere the baseline succeeded).
#
# Prerequisites: Docker daemon reachable; cgroup v2 (the script reads
# /sys/fs/cgroup/memory.peak inside the container). On Linux runners
# this is the default; on macOS Docker Desktop / colima this works
# transparently because the Linux container always sees cgroup v2.
#
# Intentionally simple: no parallelism, no caching across cells. Each
# run starts with empty GOCACHE+GOMODCACHE so the measurement is the
# pure cold-build cost — exactly what regressions or wins need to be
# reported in.
#
# The script does NOT rebuild the wasmify protoc plugin or regenerate
# googlesql.go itself; it only measures `go build ./...` cost on a
# pre-generated snapshot. Pair with the gen-proto / buf generate
# pipeline (or hand-copied googlesql.go files) outside this script.

set -euo pipefail

GO_REPO_DIR="${GO_REPO_DIR:-/Users/goccy/Development/goccy/go-googlesql}"
DOCKER_IMAGE="${DOCKER_IMAGE:-golang:1.25-bookworm}"
CPUS="${CPUS:-4}"
RUNS_PER_CELL="${RUNS_PER_CELL:-3}"

# Defaults; overridable via --memory.
MEMORY_LADDER=(512m 1g 2g)

# Parse args. Each --label N pairs with the immediately following
# --googlesql /path; we accept multiple pairs by collecting them in
# parallel arrays.
LABELS=()
SNAPSHOTS=()
while [[ $# -gt 0 ]]; do
  case $1 in
    --label)         LABELS+=("$2");    shift 2 ;;
    --googlesql)     SNAPSHOTS+=("$2"); shift 2 ;;
    --memory)        IFS=',' read -r -a MEMORY_LADDER <<<"$2"; shift 2 ;;
    --runs)          RUNS_PER_CELL="$2"; shift 2 ;;
    --cpus)          CPUS="$2"; shift 2 ;;
    --image)         DOCKER_IMAGE="$2"; shift 2 ;;
    --repo)          GO_REPO_DIR="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,30p' "$0"; exit 0 ;;
    *)
      echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

if [[ ${#LABELS[@]} -ne ${#SNAPSHOTS[@]} || ${#LABELS[@]} -eq 0 ]]; then
  echo "need at least one --label X --googlesql /path pair" >&2
  exit 2
fi

# run_cell <label> <snapshot> <memory> -> writes
#   <exit> <wall_seconds> <peak_bytes>
# to stdout, three lines per call (one per repeat).
run_cell() {
  local label=$1 snapshot=$2 mem=$3
  for ((i = 1; i <= RUNS_PER_CELL; i++)); do
    # The build container mounts the go-googlesql repo read-write so
    # `go build` can write its cache, but the googlesql.go we want to
    # measure is bind-mounted in over the in-tree copy so each
    # snapshot is exercised independently.
    local logfile peakfile
    logfile=$(mktemp)
    peakfile=$(mktemp)
    local start end
    start=$(date +%s.%N)
    set +e
    # Inside the container: run `go build` and ALWAYS write
    # /sys/fs/cgroup/memory.peak (or "?") to /tmp/peak before exiting,
    # then mirror that file to /work/_peak (host) so we can read it
    # even when go build is killed by oomkiller. Stderr/stdout from
    # go build itself goes to logfile so we can debug compile errors
    # without polluting the peak parser.
    docker run --rm \
      --memory="$mem" --memory-swap="$mem" \
      --cpus="$CPUS" \
      -v "$GO_REPO_DIR:/work" \
      -v "$snapshot:/work/googlesql.go:ro" \
      -v "$peakfile:/peak" \
      -e GOCACHE=/tmp/gocache \
      -e GOMODCACHE=/tmp/gomod \
      -e GOFLAGS="-mod=mod" \
      -w /work \
      "$DOCKER_IMAGE" \
      bash -c '
        ulimit -n 4096 || true
        record_peak() {
          if [[ -r /sys/fs/cgroup/memory.peak ]]; then
            cat /sys/fs/cgroup/memory.peak > /peak 2>/dev/null || echo "?" > /peak
          else
            echo "?" > /peak
          fi
        }
        trap record_peak EXIT
        /usr/local/go/bin/go build ./...
      ' >"$logfile" 2>&1
    local exit_code=$?
    set -e
    end=$(date +%s.%N)
    local wall
    wall=$(awk -v s="$start" -v e="$end" 'BEGIN{printf "%.1f", e-s}')
    local peak
    peak=$(tr -d '[:space:]' <"$peakfile" 2>/dev/null || echo "?")
    if [[ -z "$peak" ]]; then peak="?"; fi
    # OOM detection: docker reports 137 on cgroup OOM, but the go
    # build subprocess can also be killed by the in-container oomkiller
    # which surfaces as `signal: killed` in stderr while go itself
    # exits 1. Treat either as OOMKilled.
    local status="$exit_code"
    if [[ "$exit_code" -eq 137 ]]; then
      status="OOMKilled"
    elif grep -q -E "signal: killed|fatal error: runtime: out of memory|cannot allocate memory" "$logfile" 2>/dev/null; then
      status="OOMKilled"
    fi
    echo "$status $wall $peak"
    rm -f "$logfile" "$peakfile"
  done
}

# Collect rows: for each (label, mem) cell, take the median wall time
# and the worst exit + worst peak across the runs.
median() {
  python3 - "$@" <<'PY'
import sys, statistics
xs = [float(x) for x in sys.argv[1:]]
print(f"{statistics.median(xs):.1f}")
PY
}

format_bytes() {
  # Convert raw bytes to a human-readable form. Accept "?" as-is.
  if [[ "$1" == "?" ]]; then echo "?"; return; fi
  awk -v b="$1" 'BEGIN{
    units="B KB MB GB TB"; n = split(units, u, " "); v = b; i = 1;
    while (v >= 1024 && i < n) { v /= 1024; i++; }
    printf "%.0f %s", v, u[i];
  }'
}

# Print header columns: | mem | <label_1> exit | <label_1> wall |
# <label_1> peak | <label_2> ... |
header="| mem |"
sep="|-----|"
for L in "${LABELS[@]}"; do
  header+=" $L exit | $L wall | $L peak |"
  sep+="-------|-------|-------|"
done
echo "$header"
echo "$sep"

overall_status=0
for mem in "${MEMORY_LADDER[@]}"; do
  row="| $mem |"
  for idx in "${!LABELS[@]}"; do
    label=${LABELS[$idx]}
    snap=${SNAPSHOTS[$idx]}
    runs=()
    while IFS= read -r line; do runs+=("$line"); done < <(run_cell "$label" "$snap" "$mem")
    exits=()
    walls=()
    peaks=()
    for r in "${runs[@]}"; do
      read -r e w p <<<"$r"
      exits+=("$e")
      walls+=("$w")
      peaks+=("$p")
    done
    # Worst exit (any non-zero wins); median wall; max peak. Status
    # values are "0", "<integer>", or "OOMKilled" — the latter two
    # both count as failure but are reported separately.
    worst_exit=0
    for e in "${exits[@]}"; do
      if [[ "$e" != "0" ]]; then worst_exit="$e"; break; fi
    done
    median_wall=$(median "${walls[@]}")
    max_peak=0
    for p in "${peaks[@]}"; do
      case "$p" in
        ''|'?'|*[!0-9]*) continue ;;
      esac
      if (( p > max_peak )); then max_peak=$p; fi
    done
    peak_disp="?"
    if (( max_peak > 0 )); then peak_disp=$(format_bytes "$max_peak"); fi
    row+=" $worst_exit | ${median_wall}s | $peak_disp |"
    # If this is an "after" snapshot and the cell failed, mark overall failure.
    if [[ "$label" == after* && "$worst_exit" != "0" ]]; then
      overall_status=1
    fi
  done
  echo "$row"
done

exit "$overall_status"
