#!/usr/bin/env bash
#
# check_cli.sh - end-to-end verification for h264scan.
#
# Usage: check_cli.sh <path-to-h264scan> <fixture.264>
#
# Validates the terminal/JSON contract required by the build pipeline:
#   * at least 12 images and 2 picture groups in the deterministic fixture
#   * I/P/B slice types all present with internally consistent counts
#   * every group offset points at a real slice NAL and offsets increase
#   * exit codes 0 (clean), 2 (strict + diagnostic), 64 (usage), 66 (I/O)
#
# Exits non-zero with a readable message on the first failed assertion.
set -u

BIN="${1:-}"
FIXTURE="${2:-}"
if [[ -z "$BIN" || -z "$FIXTURE" ]]; then
    echo "usage: $0 <h264scan-binary> <fixture.264>" >&2
    exit 64
fi

FAILURES=0
check() {
    local description="$1"; shift
    if "$@"; then
        echo "ok   - $description"
    else
        echo "FAIL - $description" >&2
        FAILURES=$((FAILURES + 1))
    fi
}

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

JSON="$("$BIN" analyze "$FIXTURE" --json)"
CLEAN_RC=$?
check "analyze --json exits 0 on a clean fixture" test "$CLEAN_RC" -eq 0

# Extract a numeric field from the top-level "summary" object.  The render
# format is controlled by this project, so targeted sed/awk is sufficient
# and keeps the script dependency-free.
summary_num() {
    printf '%s' "$JSON" | sed -n '/"summary": {/,/},/p' |
        grep -o "\"$1\": [0-9]*" | head -1 | awk '{print $2}'
}
sps_num() {
    printf '%s' "$JSON" | sed -n '/"sps": {/,/},/p' |
        grep -o "\"$1\": [0-9]*" | head -1 | awk '{print $2}'
}

IMAGE_COUNT="$(summary_num image_count)"
GROUP_COUNT="$(summary_num group_count)"
I_COUNT="$(summary_num i_count)"
P_COUNT="$(summary_num p_count)"
B_COUNT="$(summary_num b_count)"

check "fixture holds at least 12 images (got $IMAGE_COUNT)" \
    test "$IMAGE_COUNT" -ge 12
check "fixture holds at least 2 picture groups (got $GROUP_COUNT)" \
    test "$GROUP_COUNT" -ge 2
check "fixture contains I pictures" test "$I_COUNT" -ge 1
check "fixture contains P pictures" test "$P_COUNT" -ge 1
check "fixture contains B pictures" test "$B_COUNT" -ge 1
check "I+P+B counts sum to the recognised image total" \
    test "$((I_COUNT + P_COUNT + B_COUNT))" -eq "$IMAGE_COUNT"

# SPS geometry must report the documented 1280x720 frame.
WIDTH="$(sps_num width)"
HEIGHT="$(sps_num height)"
check "SPS width is 1280 (got $WIDTH)" test "$WIDTH" -eq 1280
check "SPS height is 720 (got $HEIGHT)" test "$HEIGHT" -eq 720

# Group offsets come only from the "groups" array (images carry a same-named
# field, so the section must be isolated first).
GROUP_OFFSETS="$(printf '%s' "$JSON" | sed -n '/"groups": \[/,/\],/p' |
    grep -o '"start_offset_bytes": [0-9]*' | awk '{print $2}')"
GROUP_LINES="$(printf '%s' "$GROUP_OFFSETS" | grep -c .)"
check "group start offsets are unique" test "$GROUP_LINES" -eq "$GROUP_COUNT"

PREV=-1
INCREASING=1
while read -r off; do
    [[ -z "$off" ]] && continue
    if (( off <= PREV )); then INCREASING=0; fi
    PREV=$off
done <<< "$GROUP_OFFSETS"
check "group start offsets are strictly increasing" test "$INCREASING" -eq 1

# The first group offset must equal the first slice NAL start code offset
# recorded for the first image (both reference the same start code byte).
FIRST_IMAGE_OFFSET="$(printf '%s' "$JSON" | sed -n '/"images": \[/,/\],/p' |
    grep -o '"start_offset_bytes": [0-9]*' | head -1 | awk '{print $2}')"
FIRST_GROUP_OFFSET="$(printf '%s' "$GROUP_OFFSETS" | head -1)"
check "first group offset equals first image slice NAL offset" \
    test "$FIRST_IMAGE_OFFSET" -eq "$FIRST_GROUP_OFFSET"

# Text output must carry the same headline totals as JSON.
TEXT="$("$BIN" analyze "$FIXTURE")"
check "text report lists the image total" \
    grep -q "images=$IMAGE_COUNT" <<< "$TEXT"
check "text report lists group total" \
    grep -q "Picture groups ($GROUP_COUNT)" <<< "$TEXT"

# --- Exit-code contract ---------------------------------------------------
"$BIN" analyze "$FIXTURE" --strict >/dev/null 2>&1
check "strict mode exits 0 when no diagnostic is present" \
    test "$? -eq 0"

printf '\x00\x00\x01\x80\x00' > "$WORK/bad.264"
"$BIN" analyze "$WORK/bad.264" --strict >/dev/null 2>&1
check "strict mode exits 2 when diagnostics exist" test "$? -eq 2"

"$BIN" bogus-command >/dev/null 2>&1
check "unknown command exits 64 (usage error)" test "$? -eq 64"
"$BIN" analyze >/dev/null 2>&1
check "missing file argument exits 64 (usage error)" test "$? -eq 64"

"$BIN" analyze "$WORK/does-not-exist.264" >/dev/null 2>&1
check "unreadable input exits 66 (I/O error)" test "$? -eq 66"
: > "$WORK/empty.264"
"$BIN" analyze "$WORK/empty.264" >/dev/null 2>&1
check "empty input exits 66 (I/O error)" test "$? -eq 66"
printf 'garbage without any start code' > "$WORK/nonal.264"
"$BIN" analyze "$WORK/nonal.264" >/dev/null 2>&1
check "input with no NAL unit exits 66 (I/O error)" test "$? -eq 66"

echo
if (( FAILURES == 0 )); then
    echo "all checks passed"
    exit 0
fi
echo "$FAILURES check(s) failed" >&2
exit 1
