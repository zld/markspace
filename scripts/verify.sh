#!/usr/bin/env bash
# Full local verification for mediaflow. Run from macOS Terminal:
#   ./scripts/verify.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

go mod tidy
go build ./...
go test ./...
go run ./cmd/mediactl demo \
  --work ./tmp \
  --budget-mbps 18 \
  --quality-min 0.92 \
  --chunk-samples 64
