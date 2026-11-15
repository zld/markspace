#!/usr/bin/env bash
#
# scripts/demo.sh runs the full acceptance flow end to end:
#
#   1. synthesize two-bitrate, 12-second CMAF samples and inject reordering,
#      a missing fragment and timeline drift;
#   2. build 2-second HLS segments with 500ms low-latency parts;
#   3. verify the result, expecting exit code 2 (consistency failures).
#
# The script is deterministic: the same seed always reproduces the same bytes.
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${HLSFORGE_BIN:-/tmp/hlsforge}"
WORK="${HLSFORGE_WORK:-/tmp/hls-demo}"

echo ">> building hlsforge"
go build -o "$BIN" "$ROOT/cmd/hlsforge"

rm -rf "$WORK"
INBOX="$WORK/inbox"
HLS="$WORK/hls"

echo ">> generating deterministic CMAF samples with faults"
"$BIN" demo \
  --out "$INBOX" \
  --seed 7 \
  --bitrates 800000,1600000 \
  --duration 12s \
  --part 500ms \
  --fault missing,order,drift

echo ">> building low-latency HLS (2s segments, 500ms parts)"
"$BIN" build \
  --in "$INBOX" \
  --out "$HLS" \
  --from-seq 1 \
  --to-seq 24 \
  --segment 2s \
  --part 500ms \
  --window 12s

echo ">> verifying manifests, files, hashes and cross-bitrate drift"
"$BIN" verify \
  --in "$INBOX" \
  --hls "$HLS" \
  --from-seq 1 \
  --to-seq 24 \
  --segment 2s \
  --part 500ms \
  --window 12s \
  --duration 12s \
  --tolerance 50ms
rc=$?

echo ">> verify exit code: $rc (expected 2 = consistency failure)"
if [ "$rc" -ne 2 ]; then
  echo "demo FAILED: expected exit code 2" >&2
  exit 1
fi
echo ">> demo completed successfully"
