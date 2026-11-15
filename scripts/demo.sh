#!/usr/bin/env bash
#
# demo.sh - end-to-end demonstration of the offline transcode scheduler.
#
# It builds the deterministic encoder test double and the transcode CLI,
# then runs:
#   1. a fresh multi-worker job (JSON report),
#   2. an immediate resume proving completed segments are not re-encoded,
#   3. a text-report run for human inspection.
#
# Everything is written under ./.demo (a single local workspace); no network
# or external service is used. The encoder double is a synthetic quantizer,
# not a real codec, so its PSNR numbers describe only this demo round trip.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

WORKDIR="$ROOT/.demo"
BIN="$WORKDIR/bin"
ENC="$BIN/stubencoder"
CLI="$BIN/transcode"
CP="$WORKDIR/job.json"
DURATION="${DURATION:-120}"
SEGMENT="${SEGMENT:-10}"
TARGET="${TARGET:-2400}"
WORKERS="${WORKERS:-3}"

ENCODER_CMD="$ENC --in {in} --out {out} --target-kbps {kbps} --duration-sec {duration}"

echo "==> building local binaries"
mkdir -p "$BIN"
go build -o "$ENC" ./cmd/stubencoder
go build -o "$CLI" ./cmd/transcode

echo "==> fresh run (JSON)"
rm -rf "$WORKDIR/job.json" "$WORKDIR/segments" "$WORKDIR/work"
"$CLI" demo \
  --duration "$DURATION" \
  --segment-seconds "$SEGMENT" \
  --target-kbps "$TARGET" \
  --workers "$WORKERS" \
  --checkpoint "$CP" \
  --encoder "$ENCODER_CMD" \
  --json

echo
echo "==> immediate resume (must reuse every completed segment)"
"$CLI" demo \
  --duration "$DURATION" \
  --segment-seconds "$SEGMENT" \
  --target-kbps "$TARGET" \
  --workers "$WORKERS" \
  --checkpoint "$CP" \
  --encoder "$ENCODER_CMD"

echo
echo "==> demo complete (workspace: $WORKDIR)"
