#!/bin/sh
# Acceptance check for the documented demo commands. It runs the three
# required invocations and asserts the quantitative contract:
#   * target/actual bitrate within 10%
#   * psnr_db at or above the quality floor
#   * manifest_time_error_ms at most 1
#   * recovered_packets equals the requested loss count
#   * identical inputs and parameters yield the same digest
#   * a corrupt index exits 2 and commits no manifest
#   * an interrupted migration exits 3, then resume exits 0 with the same
#     entry count, alignment and digest as an uninterrupted run.
set -eu
cd "$(dirname "$0")/.."
GO="${GO:-go}"
WORK="${WORK:-$(mktemp -d "${TMPDIR:-/tmp}/mediacore-accept.XXXXXX")}"
echo "workdir=$WORK"

fail() { echo "FAIL: $*" >&2; exit 1; }

$GO build -o "$WORK/mediacore" ./cmd/mediacore

echo "== demo av1 --seed 17 --loss 3 --reorder 2 --budget-ms 5000 --quality-min 30 --target-kbps 900"
d1="$WORK/run-a"; d2="$WORK/run-b"
"$WORK/mediacore" demo --codec av1 --seed 17 --loss 3 --reorder 2 \
  --budget-ms 5000 --quality-min 30 --target-kbps 900 --dir "$d1" >"$WORK/a1.txt" 2>&1 || fail "demo exited non-zero"
"$WORK/mediacore" demo --codec av1 --seed 17 --loss 3 --reorder 2 \
  --budget-ms 5000 --quality-min 30 --target-kbps 900 --dir "$d2" >"$WORK/a2.txt" 2>&1 || fail "repeat demo exited non-zero"
cat "$WORK/a1.txt"

get() { sed -n "s/.*$1=\([^ ]*\).*/\1/p" "$WORK/a1.txt" | head -1; }
target=$(get target_kbps); actual=$(get actual_kbps); psnr=$(get psnr_db)
mterr=$(get manifest_time_error_ms); recovered=$(get recovered_packets)
decision=$(get decision)

# Bitrate deviation must not exceed 10%.
awk -v t="$target" -v a="$actual" 'BEGIN {
  if (t <= 0) exit 1
  d = (a - t) / t; if (d < 0) d = -d
  exit (d > 0.10)
}' || fail "bitrate deviation exceeds 10% (target=$target actual=$actual)"

awk -v p="$psnr" 'BEGIN { exit (p < 30) }' || fail "psnr_db $psnr below 30"
awk -v e="$mterr" 'BEGIN { exit (e > 1) }' || fail "manifest time error $mterr ms exceeds 1 ms"
[ "$recovered" = "3" ] || fail "recovered_packets $recovered != 3"
[ "$decision" = "accept" ] || fail "decision $decision != accept"

digest1=$(get digest)
digest2=$(sed -n 's/.*digest=\([^ ]*\).*/\1/p' "$WORK/a2.txt" | head -1)
[ "$digest1" = "$digest2" ] || fail "digest differs between identical runs"

echo "== demo --corrupt-index --seed 17"
d3="$WORK/run-corrupt"
set +e
"$WORK/mediacore" demo --corrupt-index --seed 17 --dir "$d3" >"$WORK/c.txt" 2>&1
code=$?
set -e
[ "$code" = "2" ] || fail "corrupt-index exit $code != 2"
grep -q "offset=" "$WORK/c.txt" || fail "corrupt-index output lacks a byte offset"
grep -q "code=MC_SCAN_" "$WORK/c.txt" || fail "corrupt-index output lacks a stable MC_SCAN_ code"
if [ -s "$d3/archive/archive.json" ]; then
  fail "corrupt input committed an archive manifest"
fi

echo "== demo --interrupt-migration --resume --seed 17"
d4="$WORK/run-cut"; d5="$WORK/run-clean"
set +e
"$WORK/mediacore" demo --interrupt-migration --seed 17 --dir "$d4" >"$WORK/i1.txt" 2>&1
code=$?
set -e
[ "$code" = "3" ] || fail "interrupt exit $code != 3"
"$WORK/mediacore" demo --interrupt-migration --resume --seed 17 --dir "$d4" >"$WORK/i2.txt" 2>&1 \
  || fail "resume exited non-zero"
"$WORK/mediacore" demo --seed 17 --dir "$d5" >"$WORK/i3.txt" 2>&1 \
  || fail "uninterrupted run exited non-zero"
cat "$WORK/i2.txt"

entries_resumed=$(sed -n 's/.*manifest_entries=\([0-9]*\).*/\1/p' "$WORK/i2.txt" | head -1)
entries_clean=$(sed -n 's/.*manifest_entries=\([0-9]*\).*/\1/p' "$WORK/i3.txt" | head -1)
[ "$entries_resumed" = "$entries_clean" ] || fail "entry count changed after resume ($entries_resumed != $entries_clean)"
err_resumed=$(sed -n 's/.*manifest_time_error_ms=\([0-9.]*\).*/\1/p' "$WORK/i2.txt" | head -1)
awk -v e="$err_resumed" 'BEGIN { exit (e > 1) }' || fail "resumed alignment $err_resumed ms exceeds 1 ms"
d_resumed=$(sed -n 's/.*digest=\([^ ]*\).*/\1/p' "$WORK/i2.txt" | head -1)
d_clean=$(sed -n 's/.*digest=\([^ ]*\).*/\1/p' "$WORK/i3.txt" | head -1)
[ "$d_resumed" = "$d_clean" ] || fail "resumed digest differs from uninterrupted run"

echo "PASS: all acceptance checks satisfied"
