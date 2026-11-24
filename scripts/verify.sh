#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

echo "== go mod tidy =="
go mod tidy

echo "== go build =="
go build ./...

echo "== go test =="
go test ./...

OUT="/tmp/mediarc-out.mp4"
rm -f "$OUT"

echo "== demo =="
go run ./cmd/mediarc demo \
  --seed 7 --loss 8 --late-ms 120 \
  --out "$OUT"

# Build the CLI directly so the script can observe the program's exact exit
# status. `go run` wraps any nonzero child status as go-command exit 1.
BIN="$(mktemp -d)/mediarc"
go build -o "$BIN" ./cmd/mediarc

check_case() {
  local name="$1"
  local pattern="$2"
  local output
  set +e
  output="$("$BIN" verify --case "$name" 2>&1)"
  local code=$?
  set -e
  echo "$output"
  if [[ "$code" -ne 2 ]]; then
    echo "FAIL: $name exit code $code, want 2" >&2
    exit 1
  fi
  if ! grep -q "$pattern" <<<"$output"; then
    echo "FAIL: $name output missing $pattern" >&2
    exit 1
  fi
}

echo "== verify cases =="
check_case "truncated" "source_offset="
check_case "late-boundary" "rewind_count=2"
check_case "color-conflict" "conflict_fields="
check_case "loss-over-window" "unrecoverable_packets=5"

echo "ALL ACCEPTANCE CHECKS PASSED"
