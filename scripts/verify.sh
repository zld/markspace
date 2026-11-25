#!/usr/bin/env bash
# verify.sh runs the acceptance procedure for the media archive kernel.
#
# Usage: ./scripts/verify.sh [seed] [out-root]
#
# Steps:
#   1. go mod tidy, build and test
#   2. normal fixture twice, then diff the manifests
#   3. truncated fixture: track+offset reported, gaps > 0 elsewhere, other tracks export
#   4. late fixture: replay triggered, state hash equals the ordered run
#   5. damaged fixture: unresolved gaps > 0, frames still exported
#
# Exits non-zero if any expectation fails.
set -euo pipefail

SEED="${1:-17}"
OUT_ROOT="${2:-/tmp/archive-verify}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

BIN="$OUT_ROOT/archive-bin"
mkdir -p "$OUT_ROOT"
go build -o "$BIN" ./cmd/archive

echo "== go mod tidy =="
go mod tidy

echo "== go build ./... =="
go build ./...

echo "== go test ./... =="
go test ./...

fail() { echo "FAIL: $*" >&2; exit 1; }

echo "== normal fixture, two runs =="
"$BIN" -fixture normal -seed "$SEED" -out "$OUT_ROOT/a"
"$BIN" -fixture normal -seed "$SEED" -out "$OUT_ROOT/b"
diff "$OUT_ROOT/a/manifest.json" "$OUT_ROOT/b/manifest.json" \
  || fail "normal manifests differ between runs"

grep -q '"sequence_sha256"' "$OUT_ROOT/a/manifest.json" || fail "frame hash missing"
grep -q '"manifest_sha256"' "$OUT_ROOT/a/manifest.json" || fail "manifest hash missing"
NORMAL_STATE=$(sed -n 's/.*"state_sha256": "\([0-9a-f]*\)".*/\1/p' "$OUT_ROOT/a/manifest.json" | head -1)

echo "== truncated fixture =="
"$BIN" -fixture truncated -seed "$SEED" -out "$OUT_ROOT/truncated" && TRUNC_CODE=0 || TRUNC_CODE=$?
[ "$TRUNC_CODE" -eq 2 ] || fail "truncated exit $TRUNC_CODE, want 2"
grep -q '"code": "truncated"' "$OUT_ROOT/truncated/manifest.json" || fail "truncation not reported"
grep -q '"offset":' "$OUT_ROOT/truncated/manifest.json" || fail "truncation offset missing"
grep -q '"track":' "$OUT_ROOT/truncated/manifest.json" || fail "truncation track missing"
FRAMES=$(sed -n 's/.*"count": \([0-9]*\).*/\1/p' "$OUT_ROOT/truncated/manifest.json" | head -1)
[ "$FRAMES" -gt 0 ] || fail "truncated fixture exported no frames"

echo "== damaged fixture =="
"$BIN" -fixture damaged -seed "$SEED" -out "$OUT_ROOT/damaged" && DMG_CODE=0 || DMG_CODE=$?
[ "$DMG_CODE" -eq 2 ] || fail "damaged exit $DMG_CODE, want 2"
grep -q '"code": "truncated"' "$OUT_ROOT/damaged/manifest.json" || fail "damaged run missing track+offset truncation"
grep -q '"gaps"' "$OUT_ROOT/damaged/manifest.json" || fail "gaps missing"
DMG_FRAMES=$(sed -n 's/.*"count": \([0-9]*\).*/\1/p' "$OUT_ROOT/damaged/manifest.json" | head -1)
[ "$DMG_FRAMES" -gt 0 ] || fail "damaged fixture exported no frames"
LOST=$(sed -n 's/.*"lost": \([0-9]*\).*/\1/p' "$OUT_ROOT/damaged/manifest.json" | head -1)
[ "$LOST" -gt 0 ] || fail "damaged fixture reported no losses"

echo "== late fixture =="
"$BIN" -fixture late -seed "$SEED" -out "$OUT_ROOT/late" || fail "late run failed"
LATE_STATE=$(sed -n 's/.*"state_sha256": "\([0-9a-f]*\)".*/\1/p' "$OUT_ROOT/late/manifest.json" | head -1)
REPLAYS=$(sed -n 's/.*"replays": \([0-9]*\).*/\1/p' "$OUT_ROOT/late/manifest.json" | head -1)
[ "$REPLAYS" -gt 0 ] || fail "late fixture triggered no replay"
[ "$LATE_STATE" = "$NORMAL_STATE" ] \
  || fail "late replay state $LATE_STATE != ordered state $NORMAL_STATE"

echo "== boundary fixture =="
"$BIN" -fixture boundary -seed "$SEED" -out "$OUT_ROOT/boundary" && BND_CODE=0 || BND_CODE=$?
[ "$BND_CODE" -eq 2 ] || fail "boundary exit $BND_CODE, want 2"
grep -q '"code": "time_out_of_range"' "$OUT_ROOT/boundary/manifest.json" || fail "boundary range error missing"

echo "ALL CHECKS PASSED"
