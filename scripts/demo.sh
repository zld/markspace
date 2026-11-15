#!/usr/bin/env bash
# Offline build + demo for subtitle_audit. No network, no windows.
set -euo pipefail

cd "$(dirname "$0")/.."

BUILD=build
RESOLUTION="${1:-1920x1080}"
OUT="$BUILD/render.jsonl"

cmake -S . -B "$BUILD"
cmake --build "$BUILD"

echo "== demo render at ${RESOLUTION} =="
"$BUILD/subtitle_audit" --render --demo \
  --resolution "$RESOLUTION" --output "$OUT" --strict

echo
echo "== check-only mode (no rendering) =="
"$BUILD/subtitle_audit" --check --demo

echo
echo "JSONL image summaries written to $OUT ($(wc -l < "$OUT") records)"
