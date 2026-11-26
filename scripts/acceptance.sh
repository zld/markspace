#!/usr/bin/env bash
# Local end-to-end acceptance for pwflow. All artifacts stay under /tmp.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if command -v brew >/dev/null 2>&1; then
  BREW_GO_BIN="$(brew --prefix go@1.23 2>/dev/null)/bin"
  if [[ -x "$BREW_GO_BIN/go" ]]; then
    export PATH="$BREW_GO_BIN:$PATH"
  fi
fi

CASES="${CASES:-/tmp/pw-cases}"
OK="${OK:-/tmp/pw-ok}"
BAD="${BAD:-/tmp/pw-bad}"
RESUME="${RESUME:-/tmp/pw-resume}"
STATE="${STATE:-/tmp/pw-state}"
BIN="${BIN:-/tmp/pwflow}"

cd "$ROOT"
go mod tidy
go build -o "$BIN" ./cmd/pwflow
go test ./...

"$BIN" fixtures --dir "$CASES" --seed 23
rm -rf "$OK" "$BAD" "$RESUME" "$STATE"

expect_code() {
  local want="$1"; shift
  set +e
  "$@"
  local got=$?
  set -e
  if [[ "$got" -ne "$want" ]]; then
    echo "expected exit $want, got $got: $*" >&2
    exit 1
  fi
}

# Complete healthy sample and independent verification.
"$BIN" run \
  --input "$CASES/standard.webm" \
  --events "$CASES/events.jsonl" \
  --rtp "$CASES/trace.rtp" \
  --out "$OK" --budget-mib 64 --workers 4
"$BIN" verify --manifest "$OK/manifest.json"

# Recoverable EBML/RTP gaps must produce and verify, exiting 2.
expect_code 2 "$BIN" run \
  --input "$CASES/damaged.webm" \
  --events "$CASES/events.jsonl" \
  --rtp "$CASES/burst-loss.rtp" \
  --out "$BAD" --budget-mib 64 --workers 4
expect_code 2 "$BIN" verify --manifest "$BAD/manifest.json"

# Checkpoint interruption exits 75; resume manifest matches a clean run.
expect_code 75 "$BIN" run \
  --input "$CASES/standard.webm" \
  --events "$CASES/events.jsonl" \
  --rtp "$CASES/trace.rtp" \
  --out "$RESUME" --budget-mib 64 --workers 4 \
  --checkpoint "$STATE" --stop-after 1
"$BIN" run \
  --input "$CASES/standard.webm" \
  --events "$CASES/events.jsonl" \
  --rtp "$CASES/trace.rtp" \
  --out "$RESUME" --budget-mib 64 --workers 4 \
  --checkpoint "$STATE"
cmp "$OK/manifest.json" "$RESUME/manifest.json"

# Impossible memory budget is rejected.
expect_code 3 "$BIN" run \
  --input "$CASES/standard.webm" \
  --events "$CASES/events.jsonl" \
  --rtp "$CASES/trace.rtp" \
  --out /tmp/pw-budget-rejected --budget-mib 1 --workers 4

echo "ACCEPTANCE PASS: healthy=PASS, damaged=PASS_WITH_GAPS, checkpoint/resume hash matches"
