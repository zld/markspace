#!/usr/bin/env bash
# Reproduce the two acceptance demo paths.
set -euo pipefail
cd "$(dirname "$0")/.."

echo "== success path (slow consumer backpressure) =="
go run ./cmd/mediad demo \
  --duration 12s --fps 25 --target-bitrate 2400000 \
  --queue-capacity 8 --consumer-delay 20ms \
  --out "${TMPDIR:-/tmp}/mediad-demo-ok"

echo
echo "== truncated input + checkpoint recovery =="
go run ./cmd/mediad demo --fixture truncated --recover \
  --out "${TMPDIR:-/tmp}/mediad-demo-recover"
