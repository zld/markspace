#!/bin/sh
# Acceptance walk-through for the tracecheck demo. Run from the project root:
#   sh scripts/accept.sh
# Builds, tests, runs the demo twice (fresh then resume), checks the documented
# invariants and prints the observed exit codes.
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
WORK=${WORK:-./tmp}
CHUNK=${CHUNK:-2}
FIRST="$WORK.first.txt"
RESUME="$WORK.resume.txt"

fail() { echo "FAIL: $*"; exit 1; }

echo "== go build =="
go build ./...

echo "== go test =="
go test ./...

echo "== demo (fresh) =="
rm -rf "$WORK"
set +e
go run ./cmd/tracecheck demo --work "$WORK" --chunk "$CHUNK" > "$FIRST" 2>&1
first=$?
go run ./cmd/tracecheck demo --work "$WORK" --chunk "$CHUNK" --resume > "$RESUME" 2>&1
resume=$?
set -e
echo "fresh_exit=$first resume_exit=$resume"
[ "$first" = 0 ] || fail "fresh demo exited $first"
[ "$resume" = 0 ] || fail "resume demo exited $resume"

grep -q '^roundtrip_rate=1.000$' "$FIRST" || fail "roundtrip_rate is not 1.000"
rate=$(grep '^keyframe_hit_rate=' "$FIRST" | head -1 | cut -d= -f2)
awk -v r="$rate" 'BEGIN { exit !(r+0 >= 0.990) }' || fail "keyframe_hit_rate $rate < 0.990"
grep -q '^resume_reprocessed_chunks=0$' "$RESUME" || fail "resume reprocessed chunks"

# The resume run must reproduce the fresh index digest and every segment hash.
fresh_digest=$(grep '^index_digest=' "$FIRST" | head -1)
resume_digest=$(grep '^index_digest=' "$RESUME" | head -1)
[ "$fresh_digest" = "$resume_digest" ] || fail "index digest changed on resume"
grep '^segment_sha256_' "$FIRST" > "$WORK.first.hashes"
grep '^segment_sha256_' "$RESUME" > "$WORK.resume.hashes"
cmp -s "$WORK.first.hashes" "$WORK.resume.hashes" || fail "segment hashes changed on resume"

echo "== damaged samples =="
grep '^error_' "$FIRST"

echo "== check exit codes =="
go build -o "$WORK/tracecheck" ./cmd/tracecheck
set +e
"$WORK/tracecheck" check "$WORK/truncated-block.webm" >/dev/null 2>&1; t=$?
"$WORK/tracecheck" check "$WORK/boundary-conflict.webm" >/dev/null 2>&1; b=$?
"$WORK/tracecheck" check "$WORK/healthy.webm" >/dev/null 2>&1; h=$?
"$WORK/tracecheck" check "$WORK/missing.webm" >/dev/null 2>&1; m=$?
set -e
echo "damaged_truncated=$t damaged_boundary=$b healthy=$h missing=$m"
[ "$t" = 2 ] || fail "truncated check exit $t, want 2"
[ "$b" = 2 ] || fail "boundary check exit $b, want 2"
[ "$h" = 0 ] || fail "healthy check exit $h, want 0"
[ "$m" = 1 ] || fail "missing check exit $m, want 1"

echo "OK"
