#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

OUT="${1:-/tmp/mediarc-out.mp4}"
if [[ -e "$OUT" ]]; then
  echo "refusing to remove or overwrite existing output: $OUT" >&2
  echo "pass a new path or remove the file explicitly first" >&2
  exit 2
fi

exec go run ./cmd/mediarc demo \
  --seed "${SEED:-7}" \
  --loss "${LOSS:-8}" \
  --late-ms "${LATE_MS:-120}" \
  --out "$OUT"
