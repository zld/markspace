#!/bin/sh
# Acceptance driver. Builds the binary so real exit codes are observable
# (`go run` collapses every non-zero status to 1), regenerates fixtures, runs
# every case and asserts the documented counters and exit codes.
set -eu

root="${1:-/tmp/live-regression}"
bin="${TMPDIR:-/tmp}/liveregress-acceptance"

cd "$(dirname "$0")/.."
go build -o "$bin" ./cmd/regress

rm -rf "$root"
"$bin" fixture --out "$root" --case all >/dev/null

fail=0
expect() {
    name="$1"; want_exit="$2"; shift 2
    out="$("$bin" check --root "$root/$name" --case "$name" "$@" 2>&1)" && code=0 || code=$?
    printf '%s exit=%s  ' "$name" "$code"
    for field in "$@"; do :; done
    printf '%s\n' "$(printf '%s\n' "$out" | grep -E '^(parse_roundtrip|keyframe_hit|segment_sha256_match|continuity_gap)=' | tr '\n' ' ')"
    if [ "$code" != "$want_exit" ]; then
        echo "  FAIL: expected exit $want_exit, got $code"
        fail=1
    fi
    printf '%s\n' "$out" > "$root/$name.report"
}

expect clean 0
expect truncated 2 --recover
expect corrupt 2 --recover
expect no-index 0 --rebuild-index
expect clock-gap 2 --recover
expect bad-mapping 1
expect bad-path 1

# Recoverable cases must report code 2 and reruns must be identical.
grep -q '^continuity_gap=2' "$root/clock-gap.report" || { echo "FAIL: clock-gap"; fail=1; }
grep -q '^keyframe_hit=5/5' "$root/truncated.report" || { echo "FAIL: truncated prefix"; fail=1; }
grep -q '^keyframe_hit=6/6' "$root/no-index.report" || { echo "FAIL: rebuilt index"; fail=1; }

"$bin" check --root "$root/truncated" --case truncated --recover | grep -v '^case=' > "$root/t1"
"$bin" check --root "$root/truncated" --case truncated --recover | grep -v '^case=' > "$root/t2"
cmp -s "$root/t1" "$root/t2" || { echo "FAIL: truncated rerun differs"; fail=1; }
rm -f "$root/t1" "$root/t2" "$root"/*.report

if [ "$fail" -ne 0 ]; then
    echo "acceptance: FAILED"
    exit 1
fi
echo "acceptance: PASS"
