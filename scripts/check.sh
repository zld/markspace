#!/usr/bin/env bash
# check.sh runs the acceptance procedure from the project root and reports
# pass/fail per expectation. It uses the compiled binary so exit codes are
# observed directly (go run swallows exit codes on some toolchains).
#
# Usage: ./scripts/check.sh [seed]
set -uo pipefail

SEED="${1:-17}"
OUT="/tmp/archive-check-$$"
BIN="$OUT/archive-bin"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
mkdir -p "$OUT"
cd "$ROOT"

pass=0
fail=0
ok()  { echo "PASS  $*"; pass=$((pass + 1)); }
bad() { echo "FAIL  $*"; fail=$((fail + 1)); }

echo "== go mod tidy =="
go mod tidy || { echo "FAIL go mod tidy"; exit 1; }
echo "== go build ./... =="
go build ./... || { echo "FAIL go build"; exit 1; }
echo "== go test ./... =="
go test ./... || { echo "FAIL go test"; exit 1; }
go build -o "$BIN" ./cmd/archive || { echo "FAIL build binary"; exit 1; }

json_get() { sed -n "s/.*\"$2\": \\([0-9]*\\).*/\\1/p" "$1" | head -1; }
state_of() { sed -n 's/.*"state_sha256": "\([0-9a-f]*\)".*/\1/p' "$1" | head -1; }

echo "== normal fixture twice =="
"$BIN" -fixture normal -seed "$SEED" -out "$OUT/a" >"$OUT/a.stdout"; A=$?
"$BIN" -fixture normal -seed "$SEED" -out "$OUT/a2" >/dev/null; B=$?
[ "$A" -eq 0 ] && ok "normal exit 0" || bad "normal exit $A"
[ "$B" -eq 0 ] && ok "normal rerun exit 0" || bad "normal rerun exit $B"
if diff -q "$OUT/a/manifest.json" "$OUT/a2/manifest.json" >/dev/null; then
  ok "manifests identical across runs"
else
  bad "manifests differ"
fi
NORMAL_STATE=$(state_of "$OUT/a/manifest.json")
[ -n "$NORMAL_STATE" ] && ok "state hash recorded" || bad "state hash missing"
grep -q '"sequence_sha256"' "$OUT/a/manifest.json" && ok "frame sequence hash recorded" || bad "frame hash missing"
MF_PRINTED=$(sed -n 's/.*manifest_file=\([0-9a-f]\{64\}\).*/\1/p' "$OUT/a.stdout" | head -1)
MF_FILE=$(shasum -a 256 "$OUT/a/manifest.json" 2>/dev/null | awk '{print $1}')
if [ -n "$MF_PRINTED" ] && [ "$MF_PRINTED" = "$MF_FILE" ]; then
  ok "printed manifest_file hash matches shasum"
else
  bad "manifest_file hash mismatch ('$MF_PRINTED' vs '$MF_FILE')"
fi

echo "== truncated fixture =="
"$BIN" -fixture truncated -seed "$SEED" -out "$OUT/truncated" >/dev/null; C=$?
[ "$C" -eq 2 ] && ok "truncated exit 2" || bad "truncated exit $C"
grep -q '"code": "truncated"' "$OUT/truncated/manifest.json" && ok "truncation reported" || bad "truncation missing"
grep -q '"track": "' "$OUT/truncated/manifest.json" && ok "truncation track reported" || bad "truncation track missing"
grep -q '"offset": [0-9]' "$OUT/truncated/manifest.json" && ok "truncation offset reported" || bad "truncation offset missing"
F=$(json_get "$OUT/truncated/manifest.json" count)
[ "${F:-0}" -gt 0 ] && ok "other tracks still export frames" || bad "no frames for truncated input"

echo "== damaged fixture =="
"$BIN" -fixture damaged -seed "$SEED" -out "$OUT/damaged" >/dev/null; D=$?
[ "$D" -eq 2 ] && ok "damaged exit 2" || bad "damaged exit $D"
if grep -q '"code": "truncated"' "$OUT/damaged/manifest.json" \
   && grep -Eq '"track": "[^"]+"' "$OUT/damaged/manifest.json" \
   && grep -Eq '"offset": [1-9][0-9]*' "$OUT/damaged/manifest.json"; then
  ok "damaged run also reports track+offset truncation"
else
  bad "damaged run missing track+offset truncation"
fi
LOST=$(sed -n 's/.*"lost": \([0-9]*\).*/\1/p' "$OUT/damaged/manifest.json" | head -1)
[ "${LOST:-0}" -gt 0 ] && ok "gap/loss count > 0 ($LOST)" || bad "no losses reported"
GAPS=$(grep -c '"reason"' "$OUT/damaged/manifest.json")
[ "${GAPS:-0}" -gt 0 ] && ok "unresolved gaps listed ($GAPS)" || bad "no unresolved gaps"
F=$(json_get "$OUT/damaged/manifest.json" count)
[ "${F:-0}" -gt 0 ] && ok "damaged run still exports frames" || bad "no frames for damaged input"

echo "== late fixture (replay) =="
"$BIN" -fixture late -seed "$SEED" -out "$OUT/late" >/dev/null; E=$?
[ "$E" -eq 0 ] && ok "late exit 0" || bad "late exit $E"
REPLAYS=$(sed -n 's/.*"replays": \([0-9]*\).*/\1/p' "$OUT/late/manifest.json" | head -1)
[ "${REPLAYS:-0}" -gt 0 ] && ok "replay triggered ($REPLAYS)" || bad "no replay triggered"
LATE_STATE=$(state_of "$OUT/late/manifest.json")
[ "$LATE_STATE" = "$NORMAL_STATE" ] && ok "late replay state == ordered state" \
  || bad "late state $LATE_STATE != ordered $NORMAL_STATE"

echo "== boundary fixture =="
"$BIN" -fixture boundary -seed "$SEED" -out "$OUT/boundary" >/dev/null; G=$?
[ "$G" -eq 2 ] && ok "boundary exit 2" || bad "boundary exit $G"
grep -q '"code": "time_out_of_range"' "$OUT/boundary/manifest.json" && ok "range violation reported" \
  || bad "range violation missing"

echo
echo "passed=$pass failed=$fail"
rm -rf "$OUT"
[ "$fail" -eq 0 ]
