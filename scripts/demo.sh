#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

DIR="${CONTINUITY_DEMO_DIR:-/tmp/pwf-demo}"

exec go run ./cmd/continuity demo \
  --dir "$DIR" \
  --seed 17 \
  --min-coverage-ppm 999000 \
  --max-av-skew-ms 20 \
  --max-bitrate-bps 2400000 \
  --budget-ms 2000
