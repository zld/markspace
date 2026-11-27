#!/usr/bin/env bash
#
# verify.sh — run the full mediasync acceptance chain locally on macOS.
# It is self-contained: no external services, all fixtures are code-generated.
# Exit status mirrors the demo: 0 only when every acceptance check passes.
set -euo pipefail

cd "$(dirname "$0")/.."

SEED="${SEED:-17}"
BUDGET_MS="${BUDGET_MS:-200}"
MAX_BITRATE="${MAX_BITRATE:-96000}"

echo "== go version =="
go version

echo "== go mod tidy =="
go mod tidy

echo "== go build ./... =="
go build ./...

echo "== go test ./... =="
go test -count=1 ./...

echo "== demo run #1 =="
go run ./cmd/mediasync demo \
  --seed "$SEED" --budget-ms "$BUDGET_MS" --max-bitrate "$MAX_BITRATE" \
  | tee /tmp/mediasync-run1.txt

echo "== demo run #2 (must match #1) =="
go run ./cmd/mediasync demo \
  --seed "$SEED" --budget-ms "$BUDGET_MS" --max-bitrate "$MAX_BITRATE" \
  | tee /tmp/mediasync-run2.txt

if diff -q /tmp/mediasync-run1.txt /tmp/mediasync-run2.txt >/dev/null; then
  echo "STABLE: identical manifest and summary across reruns"
else
  echo "ERROR: rerun output differs" >&2
  exit 1
fi

echo "ALL ACCEPTANCE STEPS PASSED"
