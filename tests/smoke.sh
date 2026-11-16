#!/bin/sh
# smoke.sh - terminal verification for subtitle-audit.
# Usage: sh tests/smoke.sh ./build/subtitle-audit
set -u

BIN=${1:?usage: sh tests/smoke.sh /path/to/subtitle-audit}
TMP=$(mktemp -d "${TMPDIR:-/tmp}/subtitle-audit.XXXXXX")
trap 'rm -rf "$TMP"' EXIT

fail() {
    echo "smoke: FAIL: $*" >&2
    exit 1
}

# --- demo generation -------------------------------------------------------
"$BIN" demo --output "$TMP/fixture.ass" >/dev/null || fail "demo exited $?"
[ -s "$TMP/fixture.ass" ] || fail "fixture not written"

# --- happy path ------------------------------------------------------------
"$BIN" analyze --input "$TMP/fixture.ass" --summary "$TMP/summary.json" \
    --canvas 1280x720 --jobs 4 >"$TMP/stdout.txt" || fail "analyze exited $?"
[ -s "$TMP/summary.json" ] || fail "summary.json not written"

get() { awk -v k="$1" '$0 ~ "^" k ": " {print $2; exit}' "$TMP/stdout.txt"; }
[ "$(get style_chains)" = 3 ] || fail "style_chains != 3"
[ "$(get recognized_tags)" -ge 12 ] || fail "recognized_tags < 12"
[ "$(get unknown_tags)" = 1 ] || fail "unknown_tags != 1"
[ "$(get malformed_tags)" = 1 ] || fail "malformed_tags != 1"
[ "$(get event_regions)" = 6 ] || fail "event_regions != 6"

grep -q '"event_regions": 6' "$TMP/summary.json" || fail "json event_regions"
grep -q '"unknown_tags": 1' "$TMP/summary.json" || fail "json unknown_tags"
grep -q '"malformed_tags": 1' "$TMP/summary.json" || fail "json malformed_tags"

# every region inside the canvas, digest is 4 hex chars
grep '^event [0-9]' "$TMP/stdout.txt" | awk '
    { split($3,a,"="); split($4,b,"="); split($5,c,"="); split($6,d,"=")
      if (a[2]<0 || b[2]<0 || a[2]+c[2]>1280 || b[2]+d[2]>720) bad=1
      if ($7 !~ /^pixel_digest=[0-9a-f][0-9a-f][0-9a-f][0-9a-f]$/) bad=1 }
    END { exit bad }' || fail "region outside canvas or bad digest"

# --- thread-count invariance ----------------------------------------------
for j in 1 8; do
    "$BIN" analyze --input "$TMP/fixture.ass" --summary "$TMP/j$j.json" \
        --canvas 1280x720 --jobs "$j" >/dev/null || fail "jobs=$j exited $?"
    grep pixel_digest "$TMP/j$j.json" >"$TMP/d$j.txt"
done
diff "$TMP/d1.txt" "$TMP/d8.txt" >/dev/null || fail "digests differ between jobs 1 and 8"

# --- error exit codes ------------------------------------------------------
cat >"$TMP/cycle.ass" <<'EOF'
[Script Info]
ScriptType: v4.00+
PlayResX: 640
PlayResY: 360

[V4+ Styles]
Format: Name, Fontname, Fontsize, Parent
Style: A,Arial,40,B
Style: B,Arial,40,A

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:00.00,0:00:01.00,A,,0,0,0,,loop
EOF
"$BIN" analyze --input "$TMP/cycle.ass" --summary "$TMP/c.json" 2>/dev/null
[ $? -eq 2 ] || fail "inheritance cycle should exit 2"

sed 's/,B$/,Nope/' "$TMP/cycle.ass" >"$TMP/orphan.ass"
"$BIN" analyze --input "$TMP/orphan.ass" --summary "$TMP/o.json" 2>/dev/null
[ $? -eq 2 ] || fail "unknown parent should exit 2"

# negative-duration event (no inheritance involved)
cat >"$TMP/negdur.ass" <<'EOF'
[Script Info]
ScriptType: v4.00+
PlayResX: 640
PlayResY: 360

[V4+ Styles]
Format: Name, Fontname, Fontsize
Style: A,Arial,40

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:02.00,0:00:01.00,A,,0,0,0,,backwards
EOF
"$BIN" analyze --input "$TMP/negdur.ass" --summary "$TMP/n.json" 2>/dev/null
[ $? -eq 2 ] || fail "negative duration should exit 2"

"$BIN" analyze --input "$TMP/does-not-exist.ass" --summary "$TMP/x.json" 2>/dev/null
[ $? -eq 4 ] || fail "missing input should exit 4"

"$BIN" analyze --input "$TMP/fixture.ass" --summary "$TMP/x.json" --jobs 99 2>/dev/null
[ $? -eq 64 ] || fail "jobs out of range should exit 64"

"$BIN" frobnicate 2>/dev/null
[ $? -eq 64 ] || fail "unknown command should exit 64"

# --- BOM/CRLF input and zero-visible-pixel event ---------------------------
printf '\357\273\277[Script Info]\r\nScriptType: v4.00+\r\nPlayResX: 640\r\nPlayResY: 360\r\n\r\n[V4+ Styles]\r\nFormat: Name, Fontname, Fontsize\r\nStyle: D,Arial,40\r\n\r\n[Events]\r\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\r\nDialogue: 0,0:00:00.00,0:00:02.00,D,,0,0,0,,{\\alpha&HFF&}fully transparent\r\n' \
    >"$TMP/ghost.ass"
"$BIN" analyze --input "$TMP/ghost.ass" --summary "$TMP/g.json" \
    --canvas 640x360 --jobs 2 >"$TMP/g.txt" || fail "ghost analyze exited $?"
grep -q '^event 1: x=0 y=0 w=0 h=0 pixel_digest=0000$' "$TMP/g.txt" \
    || fail "transparent event should yield an empty region"

echo "smoke: OK"
