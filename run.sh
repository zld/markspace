#!/bin/sh
# run.sh - reproduce the acceptance flow from a clean checkout.
# Requires Go 1.23+ (installed from the go.dev macOS package). No external
# services or media are used; every sample is generated from --seed.
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$ROOT"

echo "==> go mod tidy"
go mod tidy

echo "==> go build ./..."
go build ./...

echo "==> go test ./..."
go test ./...

echo "==> demo"
go run ./cmd/mediaroute demo --seed 17 --queue 2 --target-bps 2000000

echo "==> verify"
go run ./cmd/mediaroute verify \
    --input ./tmp/demo-output.mp4 \
    --manifest ./tmp/demo-manifest.json \
    --index ./tmp/demo-index.json

echo "==> done"
