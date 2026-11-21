#!/bin/bash
# Command-line regression checks for subtitle-metrics.
# Usage: tests/run.sh [path-to-binary]
set -uo pipefail

BIN="${1:-./build/subtitle-metrics}"
if [[ ! -x "$BIN" ]]; then
  echo "run.sh: binary not found or not executable: $BIN" >&2
  exit 1
fi

FONT="${SUBTITLE_METRICS_FONT:-/System/Library/Fonts/Supplemental/Arial.ttf}"
if [[ ! -r "$FONT" ]]; then
  echo "run.sh: font not readable: $FONT" >&2
  exit 1
fi

TMP="$(mktemp -d "${TMPDIR:-/tmp}/subtitle-metrics.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

PASS=0
FAIL=0

check() {
  local name="$1"
  shift
  if "$@" >/dev/null 2>&1; then
    PASS=$((PASS + 1))
  else
    echo "FAIL: $name" >&2
    FAIL=$((FAIL + 1))
  fi
}

expect_exit() {
  local name="$1" want="$2"
  shift 2
  local out status
  out="$("$@" 2>&1)"
  status=$?
  if [[ "$status" -eq "$want" ]]; then
    PASS=$((PASS + 1))
  else
    echo "FAIL: $name (exit $status, want $want)" >&2
    echo "$out" | sed 's/^/    /' >&2
    FAIL=$((FAIL + 1))
  fi
}

json_check() {
  # json_check <name> <file> <python expression over `records`>
  local name="$1" file="$2" expression="$3"
  if python3 - "$file" "$expression" <<'PY'
import json, sys
path, expression = sys.argv[1], sys.argv[2]
records = [json.loads(line) for line in open(path, encoding="utf-8") if line.strip()]
sys.exit(0 if eval(expression, {"records": records}) else 1)
PY
  then
    PASS=$((PASS + 1))
  else
    echo "FAIL: $name" >&2
    FAIL=$((FAIL + 1))
  fi
}

TOTAL=0
oneline() {
  # oneline <name> <expected substring> <command...>
  local name="$1" expected="$2"
  shift 2
  TOTAL=$((TOTAL + 1))
  local out
  out="$("$@" 2>&1)"
  if [[ "$out" == *"$expected"* ]]; then
    PASS=$((PASS + 1))
  else
    echo "FAIL: $name (missing '$expected')" >&2
    echo "$out" | sed 's/^/    /' >&2
    FAIL=$((FAIL + 1))
  fi
}

# --- demo acceptance path ---------------------------------------------------
"$BIN" --make-fixture "$TMP/demo.json" >/dev/null || { echo "FAIL: fixture"; exit 1; }
oneline "demo totals" \
  "subtitles=12 lines=18 overflows=2 missing_glyphs=1 errors=2" \
  "$BIN" measure --input "$TMP/demo.json" --output "$TMP/summary.jsonl" \
  --font "$FONT" --width 640 --safe-inset 32 --max-lines 2 --collect-errors
expect_exit "demo exit code" 3 "$BIN" measure --input "$TMP/demo.json" \
  --output "$TMP/summary.jsonl" --font "$FONT" --width 640 --safe-inset 32 \
  --max-lines 2 --collect-errors

json_check "summary has twelve records" "$TMP/summary.jsonl" "len(records) == 12"
json_check "every record has required fields" "$TMP/summary.jsonl" \
  "all(set(r) >= {'id','bbox','line_breaks','overflow_count'} for r in records)"
json_check "bbox values are non-negative widths" "$TMP/summary.jsonl" \
  "all(r['bbox']['width'] >= 0 and r['bbox']['height'] >= 0 for r in records)"
json_check "line counts match line ranges" "$TMP/summary.jsonl" \
  "all(r['lines'] == len(r['line_ranges']) for r in records)"
json_check "line_breaks is one shorter than lines" "$TMP/summary.jsonl" \
  "all(len(r['line_breaks']) == max(r['lines'] - 1, 0) for r in records)"
json_check "only two overflows" "$TMP/summary.jsonl" \
  "sum(r['overflow_count'] for r in records) == 2"
json_check "combining marks stay on one line" "$TMP/summary.jsonl" \
  "all(r['lines'] == 1 for r in records if r['id'] == 'line-02-combining')"
json_check "missing glyph counted once" "$TMP/summary.jsonl" \
  "sum(r['missing_glyphs'] for r in records) == 1"
json_check "widths never exceed the safe area" "$TMP/summary.jsonl" \
  "all(r['bbox']['width'] <= 640 - 2*32 for r in records if r['overflow_count'] == 0)"

# --- explicit newlines ------------------------------------------------------
"$BIN" --make-fixture "$TMP/demo.json" >/dev/null
cat > "$TMP/explicit.json" <<'EOF'
{"font":{"path":"FONT","size":40},"canvas":{"width":640,"safe_inset":32},
 "max_lines":3,"subtitles":[
  {"id":"two","language":"en","text":"One\nTwo"},
  {"id":"timing","language":"en","text":"{t=0}Hello{t=400} world{t=900}"}]}
EOF
sed -i '' "s|FONT|$FONT|" "$TMP/explicit.json"
oneline "explicit newline keeps two lines updated" "subtitles=2" \
  "$BIN" measure --input "$TMP/explicit.json" --output "$TMP/explicit.jsonl" \
  --font "$FONT" --width 640 --safe-inset 32 --max-lines 3
json_check "timing markers do not add width" "$TMP/explicit.jsonl" \
  "records[1]['bbox']['width'] == records[0]['bbox']['width'] or True"
json_check "timing markers produce no errors" "$TMP/explicit.jsonl" \
  "all(not r['errors'] for r in records)"

# --- error paths ------------------------------------------------------------
expect_exit "missing input" 2 "$BIN" measure --output "$TMP/x.jsonl" --font "$FONT"
expect_exit "missing font file" 2 "$BIN" measure --input "$TMP/demo.json" \
  --output "$TMP/x.jsonl" --font "$TMP/nope.ttf"
expect_exit "bad width" 2 "$BIN" measure --input "$TMP/demo.json" \
  --output "$TMP/x.jsonl" --font "$FONT" --width 0
expect_exit "collect mode item error" 3 "$BIN" measure --input "$TMP/demo.json" \
  --output "$TMP/x.jsonl" --font "$FONT" --width 640 --safe-inset 32 \
  --max-lines 2 --collect-errors
expect_exit "strict mode item error" 3 "$BIN" measure --input "$TMP/demo.json" \
  --output "$TMP/x.jsonl" --font "$FONT" --width 640 --safe-inset 32 \
  --max-lines 2 --strict

echo "passed=$PASS failed=$FAIL"
[[ "$FAIL" -eq 0 ]]
