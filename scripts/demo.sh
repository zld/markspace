#!/bin/sh
# Reproducible demo: synthesizes fixtures, transcodes with per-segment
# bitrate caps, measures PSNR, prints the terminal summary and writes
# ./artifacts/report.json. Extra args are forwarded to the demo command.
set -eu
cd "$(dirname "$0")/.."
exec go run ./cmd/transcode demo --out ./artifacts --segment 2s --target-kbps 900 "$@"
