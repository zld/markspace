#!/usr/bin/env bash
# Generate deterministic MP4/RTP/EDL/VFR sample data and process it.
# Usage: scripts/generate_samples.sh [output-dir]
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

OUT="${1:-${OUT:-/tmp/mediad-generated}}"

go run ./cmd/mediad demo \
  --seed 23 \
  --drift-ppm 120 \
  --wrap-at 4294967000 \
  --vfr \
  --chunk 2s \
  --target-rate 48000 \
  --layout stereo \
  --loudness -16 \
  --peak-ceiling -1 \
  --out "$OUT"
