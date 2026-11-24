#!/usr/bin/env bash
# Generates deterministic local RTP/RTCP stream fixtures through the Go generator.
# Outputs stay under tmp/ by default and are ignored by git.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${1:-$ROOT/tmp/fixtures}"
case "$OUT" in
  /*) ;;
  *) OUT="$PWD/$OUT" ;;
esac

cd "$ROOT"

go run ./cmd/mediapipe demo \
  --out "$OUT/normal" \
  --target-bitrate 64000 \
  --segment-ms 2000 \
  --checkpoint-packets 128

go run ./cmd/mediapipe demo \
  --out "$OUT/damaged" \
  --inject truncation,corrupt,missing-index \
  --target-bitrate 64000 \
  --segment-ms 2000 \
  --checkpoint-packets 128

go run ./cmd/mediapipe resume --out "$OUT/damaged"
go run ./cmd/mediapipe verify --out "$OUT/damaged"

echo "fixtures generated under $OUT"
