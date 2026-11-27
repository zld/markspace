#!/usr/bin/env bash
#
# generate.sh — regenerate the self-contained synthetic fixtures used by the
# acceptance demo into a local directory. No external media or network access
# is required; every frame and RTP packet is produced by the Go code itself.
#
# Usage: scripts/generate.sh [OUT_DIR]
set -euo pipefail

OUT_DIR="${1:-./tmp-fixtures}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

( cd "$ROOT" && go run ./cmd/genfixtures "$OUT_DIR" )

echo "files:"
ls -1 "$OUT_DIR"
