#!/usr/bin/env bash
# acceptance.sh — runs the full local acceptance sequence for mediasync.
# Purely local: no network access and no external media files are used.
set -euo pipefail

cd "$(dirname "$0")/.."

echo "== [1/5] go mod tidy =="
go mod tidy

echo "== [2/5] go build ./... =="
go build ./...

echo "== [3/5] go test ./... =="
go test ./...

echo "== [4/5] demo (interrupt + resume) =="
rm -rf work
go run ./cmd/mediasync demo \
  --seed 23 --chunk-records 256 --interrupt-after 2 --resume

echo "== [5/5] verify (repeatability) =="
rm -rf work
go run ./cmd/mediasync verify --seed 23 --chunk-records 256

echo
echo "all acceptance steps completed successfully"
