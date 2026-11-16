#!/bin/sh
# Integration smoke tests for transition_lab.
# Usage: sh tests/smoke.sh [path/to/transition_lab]
set -u

BIN="${1:-/tmp/transition_lab}"
SPEEDS="0=1/2,24=2/1,40=1/1"
FAILURES=0

TMPROOT="$(mktemp -d "${TMPDIR:-/tmp}/transition_lab_smoke.XXXXXX")"
trap 'rm -rf "$TMPROOT"' EXIT

check() {
    # check <description> <expected_exit> <actual_exit>
    if [ "$2" -eq "$3" ]; then
        printf 'ok   - %s (exit %d)\n' "$1" "$3"
    else
        printf 'FAIL - %s: expected exit %d, got %d\n' "$1" "$2" "$3"
        FAILURES=$((FAILURES + 1))
    fi
}

if [ ! -x "$BIN" ]; then
    echo "FAIL - binary not found or not executable: $BIN" >&2
    exit 1
fi

OUT_A="$TMPROOT/demo_a"
OUT_B="$TMPROOT/demo_b"
mkdir -p "$OUT_A" "$OUT_B"

# 1. Happy path: 3 transitions x 48 frames = 144 P6 PPM files, exit 0.
OUT="$("$BIN" demo --out "$OUT_A" --fps 24 --frames 48 \
        --speed "$SPEEDS" --seed 17 --verify 2>&1)"
check "demo run succeeds" 0 $?
echo "$OUT"

PPM_COUNT=$(find "$OUT_A" -type f -name '*.ppm' | wc -l | tr -d ' ')
check "144 PPM frames written" 144 "$PPM_COUNT"

# Every file must carry the binary P6 magic.
MAGIC_OK=1
for f in "$OUT_A"/*.ppm; do
    [ "$(head -c 2 "$f")" = "P6" ] || MAGIC_OK=0
done
check "all frames use P6 PPM magic" 1 "$MAGIC_OK"
HEADER_OK=1
for f in "$OUT_A"/dissolve_0000.ppm "$OUT_A"/wipe_0000.ppm \
         "$OUT_A"/circle_reveal_0047.ppm; do
    head -n 2 "$f" | grep -q '^96 64$' || HEADER_OK=0
    grep -q '^255$' "$f" || HEADER_OK=0
done
check "PPM headers declare 96x64 and maxval 255" 1 "$HEADER_OK"

# 2. Every summary line carries a 16-digit lowercase hex hash pair and
#    reports zero mismatches.
HASH_LINES=$(echo "$OUT" | grep -cE 'first_hash=[0-9a-f]{16} last_hash=[0-9a-f]{16}')
check "three lines with 16-digit hashes" 3 "$HASH_LINES"
MISMATCH=$(echo "$OUT" | grep -c 'mismatch_frames=0')
check "all three lines report zero mismatches" 3 "$MISMATCH"

# 3. Cross-process reproducibility: identical invocation, identical output.
"$BIN" demo --out "$OUT_B" --fps 24 --frames 48 --speed "$SPEEDS" \
      --seed 17 --verify >"$TMPROOT/run_b.txt" 2>&1
check "second run succeeds" 0 $?
"$BIN" demo --out "$OUT_A" --fps 24 --frames 48 --speed "$SPEEDS" \
      --seed 17 --verify >"$TMPROOT/run_a.txt" 2>&1
if cmp -s "$TMPROOT/run_a.txt" "$TMPROOT/run_b.txt"; then
    echo "ok   - summary is reproducible across processes"
else
    echo "FAIL - summaries differ between runs"
    FAILURES=$((FAILURES + 1))
fi

# 4. Endpoint exactness: first frames of all transitions are identical,
#    and so are the last frames (exact source clip copies).
FIRST_UNIQUE=$(for f in "$OUT_A"/*_0000.ppm; do md5 -q "$f"; done |
               sort -u | wc -l | tr -d ' ')
check "all transition first frames identical" 1 "$FIRST_UNIQUE"
LAST_UNIQUE=$(for f in "$OUT_A"/*_0047.ppm; do md5 -q "$f"; done |
              sort -u | wc -l | tr -d ' ')
check "all transition last frames identical" 1 "$LAST_UNIQUE"

# 5. No temporary leftovers after a successful run.
LEFTOVERS=$(find "$OUT_A" -name '*.tmp' | wc -l | tr -d ' ')
check "no temporary files left behind" 0 "$LEFTOVERS"

# 6. Parameter errors -> exit code 2.
"$BIN" demo >/dev/null 2>&1
check "missing --out rejected" 2 $?
"$BIN" demo --out "$OUT_A" --frames 0 >/dev/null 2>&1
check "zero frames rejected" 2 $?
"$BIN" demo --out "$OUT_A" --speed 0=1/16,5=1/1 >/dev/null 2>&1
check "rate below 1/8 rejected" 2 $?
"$BIN" demo --out "$OUT_A" --speed 5=1/1 >/dev/null 2>&1
check "missing frame-0 speed node rejected" 2 $?
"$BIN" demo --out "$OUT_A" --speed 0=1/1,0=2/1 >/dev/null 2>&1
check "duplicate speed node rejected" 2 $?
"$BIN" demo --out "$OUT_A" --speed 0=2/1,10=1/2,5=1/1 >/dev/null 2>&1
check "backwards speed node rejected" 2 $?
"$BIN" demo --out "$OUT_A" --speed 0=9/1 >/dev/null 2>&1
check "rate above 8 rejected" 2 $?
"$BIN" demo --out "$OUT_A" --width 99999 --height 99999 >/dev/null 2>&1
check "oversized frame dimensions rejected" 2 $?
"$BIN" frobnicate >/dev/null 2>&1
check "unknown command rejected" 2 $?

# 7. Consistency failure -> exit code 3. The test-only hook corrupts one
#    rendered frame so the replay hash comparison must detect it.
mkdir -p "$TMPROOT/verify"
"$BIN" demo --out "$TMPROOT/verify" --verify >/dev/null 2>&1
check "verify mode passes on deterministic output" 0 $?
TL_FORCE_MISMATCH=7 "$BIN" demo --out "$TMPROOT/verify" --verify \
    >/dev/null 2>&1
check "corrupted frame detected as consistency failure" 3 $?

# 8. File errors -> exit code 4.
"$BIN" demo --out "$TMPROOT/does/not/exist" >/dev/null 2>&1
check "missing output directory rejected" 4 $?
"$BIN" demo --out "$OUT_A/../escape" >/dev/null 2>&1
check "parent traversal path rejected" 4 $?
if [ -e /dev/full ]; then
    "$BIN" demo --out /dev/full >/dev/null 2>&1
    check "unwritable target rejected" 4 $?
fi

echo
if [ "$FAILURES" -eq 0 ]; then
    echo "smoke tests: ALL PASSED"
    exit 0
fi
echo "smoke tests: $FAILURES FAILURE(S)"
exit 1
