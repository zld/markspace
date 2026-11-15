#!/usr/bin/env bash
#
# End-to-end smoke test for subtitle-metrics. Runs the full acceptance
# flow plus error-path and determinism checks. Exits non-zero on the
# first failed assertion. No network access is required.
#
# usage: FONT=/path/to/font.ttf tests/cli_smoke.sh [build-dir]
set -euo pipefail

BUILD_DIR="${1:-build}"
BIN="$BUILD_DIR/subtitle-metrics"

# Pick a font with broad CJK coverage when FONT is not provided.
if [ -z "${FONT:-}" ]; then
    for candidate in \
        "/System/Library/Fonts/Supplemental/Arial Unicode.ttf" \
        "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf" \
        "/usr/share/fonts/truetype/noto/NotoSansCJK-Regular.ttc" \
        "/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc" \
        "/usr/share/fonts/dejavu/DejaVuSans.ttf"; do
        if [ -f "$candidate" ]; then FONT="$candidate"; break; fi
    done
fi
FONT="${FONT:-}"
WORK="$(mktemp -d -t subtitle-metrics-smoke.XXXXXX 2>/dev/null \
        || mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok: $*"; }

[ -x "$BIN" ] || fail "binary not found at $BIN (build the project first)"
[ -n "$FONT" ] || fail "no test font found; set FONT=/path/to/font.ttf"
[ -f "$FONT" ] || fail "test font not found: $FONT"

# 1. Fixture generation.
"$BIN" --make-fixture "$WORK/subs.json" >/dev/null
[ -s "$WORK/subs.json" ] || fail "fixture was not written"
pass "fixture generated"

# 2. Happy-path analysis; overflow is expected for the demo fixture.
set +e
"$BIN" --font "$FONT" --input "$WORK/subs.json" \
       --width 1280 --safe-left 96 --safe-right 96 \
       --font-size 42 --wrap punctuation \
       --json-out "$WORK/summary.json" >"$WORK/report.txt" 2>&1
rc=$?
set -e
[ "$rc" -eq 4 ] || fail "expected exit 4 for overflowing fixture, got $rc"
for key in subtitle_count bbox_count overflow_count missing_glyph_count; do
  grep -q "^$key:" "$WORK/report.txt" || fail "terminal output missing $key"
done
grep -q '^id:' "$WORK/report.txt" || fail "terminal output missing per-id lines"
grep -q 'bbox{' "$WORK/report.txt" || fail "terminal output missing bbox"
pass "analysis with overflow returns 4 and prints all counters"

# 3. Summary JSON has the stable, documented shape.
python3 -I - "$WORK/summary.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
for k in ("tool", "version", "parameters", "summary", "subtitles"):
    assert k in d, k
s = d["summary"]
assert s["subtitle_count"] == s["bbox_count"] == len(d["subtitles"])
assert isinstance(s["overflow_count"], int)
assert isinstance(s["missing_glyph_count"], int)
for sub in d["subtitles"]:
    for k in ("id", "sha256", "bbox", "lines", "overflow_entries",
              "missing_glyphs", "cluster_total"):
        assert k in sub, (sub.get("id"), k)
    assert len(sub["sha256"]) == 64
    bx = sub["bbox"]
    assert set(bx) == {"x", "y", "width", "height"}
    for line in sub["lines"]:
        assert set(line) >= {"line_index", "text", "first_cluster",
                             "cluster_count", "width", "x", "y"}
# Empty-text subtitle must carry a zero-size box.
empty = [x for x in d["subtitles"] if not x["lines"][0]["text"]]
assert empty and all(v == 0.0 for v in empty[0]["bbox"].values())
print("ok: summary JSON shape and zero-size empty box")
PY

# 4. Determinism: a second run must be byte-identical.
set +e
"$BIN" --font "$FONT" --input "$WORK/subs.json" --width 1280 \
       --safe-left 96 --safe-right 96 --font-size 42 \
       --wrap punctuation --json-out "$WORK/summary2.json" >/dev/null 2>&1
rc=$?
set -e
[ "$rc" -eq 4 ] || fail "second analysis expected exit 4, got $rc"
cmp -s "$WORK/summary.json" "$WORK/summary2.json" || fail "non-deterministic output"
pass "output is byte-stable across runs"

# 5. Atomic output: no leftover temporary file beside the target.
ls "$WORK" | grep -q '\.tmp\.' && fail "temporary output was not cleaned up"
pass "no temporary files left behind"

# 6. Clean input returns 0.
cat > "$WORK/clean.json" <<'JSON'
{"subtitles":[{"id":"a","text":"Hello world","lang":"en"},
              {"id":"b","text":"你好世界","lang":"zh"}]}
JSON
set +e
"$BIN" --font "$FONT" --input "$WORK/clean.json" --width 1280 \
       --safe-left 96 --safe-right 96 --font-size 42 \
       --wrap hard >/dev/null 2>&1
rc=$?
set -e
[ "$rc" -eq 0 ] || fail "clean input should exit 0, got $rc"
pass "clean input exits 0"

# 7. Input/config errors return 2 with positioned messages.
echo '{"subtitles":[{"id":"x","text":"1"},{"id":"x","text":"2"}]}' \
    > "$WORK/dup.json"
set +e
msg=$("$BIN" --font "$FONT" --input "$WORK/dup.json" --width 100 \
             --font-size 10 2>&1 >/dev/null)
rc=$?
set -e
[ "$rc" -eq 2 ] || fail "duplicate id should exit 2, got $rc"
echo "$msg" | grep -q "entry 2" || fail "duplicate message lacks entry position"
set +e
"$BIN" --font "$FONT" --input "$WORK/clean.json" --width 40 \
       --safe-left 30 --safe-right 30 --font-size 10 >/dev/null 2>&1
rc=$?
set -e
[ "$rc" -eq 2 ] || fail "invalid safe area should exit 2, got $rc"
pass "input and config errors return 2"

# 8. Font and output errors return 3.
set +e
"$BIN" --font "$WORK/missing.ttf" --input "$WORK/clean.json" \
       --width 100 --font-size 10 >/dev/null 2>&1
rc=$?
set -e
[ "$rc" -eq 3 ] || fail "missing font should exit 3, got $rc"
set +e
"$BIN" --font "$FONT" --input "$WORK/clean.json" --width 100 \
       --font-size 10 --json-out "$WORK/no/such/dir/out.json" \
       >/dev/null 2>&1
rc=$?
set -e
[ "$rc" -eq 3 ] || fail "unwritable output should exit 3, got $rc"
pass "font and output errors return 3"

# 9. Streaming to stdout keeps stdout pure JSON.
set +e
"$BIN" --font "$FONT" --input "$WORK/clean.json" --width 1280 \
       --safe-left 96 --safe-right 96 --font-size 42 \
       --json-out - 2>/dev/null | python3 -I -c 'import json,sys; json.load(sys.stdin)'
rc=$?
set -e
[ "$rc" -eq 0 ] || fail "stdout JSON was not parseable"
pass "stdout mode emits pure JSON"

# 10. All three wrap policies run without crashing (overflow exit 4 is fine).
for policy in space punctuation hard; do
  set +e
  "$BIN" --font "$FONT" --input "$WORK/subs.json" --width 640 \
         --safe-left 20 --safe-right 20 --font-size 36 \
         --wrap "$policy" >/dev/null 2>&1
  rc=$?
  set -e
  [ "$rc" -eq 0 ] || [ "$rc" -eq 4 ] || fail "policy $policy failed ($rc)"
done
pass "space/punctuation/hard wrap policies all run"

# 11. Adversarial samples: malformed inputs are rejected, edge cases kept.
if [ -x "$BUILD_DIR/fixture-samples" ]; then
  mkdir -p "$WORK/adv"
  "$BUILD_DIR/fixture-samples" "$WORK/adv" >/dev/null
  for bad in duplicate_id bad_id_type bad_text_type bad_lang_type \
             invalid_utf8 truncated_utf8 overlong_utf8 surrogate_utf8 \
             broken_json wrong_shape surrogate_escape; do
    set +e
    "$BIN" --font "$FONT" --input "$WORK/adv/$bad.json" \
           --width 100 --font-size 10 >/dev/null 2>&1
    rc=$?
    set -e
    [ "$rc" -eq 2 ] || fail "adversarial sample $bad should exit 2, got $rc"
  done
  # The combining-escape sample is valid and must stay one cluster.
  set +e
  "$BIN" --font "$FONT" --input "$WORK/adv/combining_escape.json" \
         --width 200 --font-size 42 --json-out - 2>/dev/null \
         | python3 -I -c 'import json,sys; d=json.load(sys.stdin); \
assert d["subtitles"][0]["lines"][0]["cluster_count"] == 1'
  rc=$?
  set -e
  [ "$rc" -eq 0 ] || fail "combining escape was not kept in one cluster"
  pass "adversarial samples rejected and combining cluster preserved"
fi

echo
echo "ALL SMOKE TESTS PASSED"
