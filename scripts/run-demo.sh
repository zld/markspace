#!/usr/bin/env bash
# Reproducible offline forensic restore demo. No network access is used;
# every input is generated in-process by the Go fixture layer.
set -euo pipefail

cd "$(dirname "$0")/.."

SEED="${SEED:-42}"
FRAMES="${FRAMES:-120}"
LOSS="${LOSS:-0.08}"
OUT="${OUT:-/tmp/archive-restore}"

go mod tidy
go build ./...
go test ./...
go run ./cmd/restore \
  --self-test \
  --seed "${SEED}" \
  --frames "${FRAMES}" \
  --packet-loss "${LOSS}" \
  --output "${OUT}"
