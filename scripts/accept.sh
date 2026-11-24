#!/usr/bin/env bash
#
# accept.sh — single-command acceptance gate for the media processing kernel.
# Runs the exact sequence from the acceptance brief and fails loudly on the
# first problem. Designed for macOS Terminal, pure local execution.
set -euo pipefail

# Resolve the repository root from this script's location so the gate works
# regardless of the caller's current directory.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

SEED="${SEED:-42}"
LATE_WINDOW_MS="${LATE_WINDOW_MS:-120}"
BUFFER_BYTES="${BUFFER_BYTES:-65536}"
CONSUMER_DELAY_MS="${CONSUMER_DELAY_MS:-8}"

echo "==> go mod tidy"
go mod tidy

echo "==> go build ./..."
go build ./...

echo "==> go vet ./..."
go vet ./...

echo "==> go test ./..."
go test ./...

echo "==> go test -race (concurrency packages)"
go test -race ./internal/stream/ ./internal/timeline/ ./internal/rtp/

echo "==> demo (seed=${SEED} window=${LATE_WINDOW_MS}ms buffer=${BUFFER_BYTES} delay=${CONSUMER_DELAY_MS}ms)"
go run ./cmd/mediad demo \
  --seed "${SEED}" \
  --late-window-ms "${LATE_WINDOW_MS}" \
  --buffer-bytes "${BUFFER_BYTES}" \
  --consumer-delay-ms "${CONSUMER_DELAY_MS}"

echo
echo "acceptance gate passed"
