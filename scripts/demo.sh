#!/usr/bin/env bash
# End-to-end demo: build, test, generate a remap+loss sample, inspect it and
# slice program 1. Run from the repository root on macOS.
set -euo pipefail

cd "$(dirname "$0")/.."

echo "== go mod download =="
go mod download

echo "== go test ./... =="
go test ./...

echo "== go vet ./... =="
go vet ./...

GEN=/tmp/rebuild.ts
SEG=/tmp/program.ts

echo "== generate =="
go run ./cmd/tsrebuild generate --out "$GEN" --scenario remap-loss

echo "== inspect =="
go run ./cmd/tsrebuild inspect --input "$GEN" --program 1 --json

echo "== segment =="
go run ./cmd/tsrebuild segment --input "$GEN" --program 1 --out "$SEG" --json

echo "== verify segment packet count =="
bytes=$(stat -f%z "$SEG")
packets=$((bytes / 188))
echo "segment file: $bytes bytes = $packets packets (each packet is 188 bytes)"
if [ "$((bytes % 188))" -ne 0 ]; then
  echo "segment output is not a multiple of 188 bytes" >&2
  exit 1
fi
echo "OK"
