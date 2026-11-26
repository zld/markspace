#!/usr/bin/env bash
# Reproducible local acceptance verification for medcheck on macOS.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

go mod tidy
go build ./...
go test ./...

OUT="${MEDCHECK_OUT:-${TMPDIR:-/tmp}/media-evidence}"
mkdir -p "$OUT"
LOG="$OUT/terminal.txt"

set -o pipefail
go run ./cmd/medcheck demo --seed 17 --window 8 --out "$OUT" | tee "$LOG"

grep -q '^roundtrip=12/12$' "$LOG"
grep -q '^index_hit=12/12$' "$LOG"
grep -q '^segments=12$' "$LOG"
grep -q '^repeat_match=12/12$' "$LOG"
grep -q '^recovered=1$' "$LOG"
grep -q '^unrecoverable_gap=1$' "$LOG"
grep -q '^medcheck=PASS$' "$LOG"

hash_count="$(grep -Ec '^segment_[0-9]{2} sha256=[0-9a-f]{64} repeat_match=true$' "$LOG" || true)"
if [[ "$hash_count" -ne 12 ]]; then
  echo "expected 12 repeated 64-hex segment hashes, got $hash_count" >&2
  exit 1
fi

drift="$(sed -n 's/^av_offset_us=-*[0-9][0-9]* timeline_drift_max_us=\([0-9][0-9]*\) .*/\1/p' "$LOG" | head -1)"
if [[ -z "$drift" || "$drift" -gt 2000 ]]; then
  echo "timeline drift $drift exceeds 2000 us" >&2
  exit 1
fi

echo "verify.sh: PASS (evidence: $OUT/evidence.json)"
