#!/usr/bin/env bash
# Reproducible local build/test/demo entry point for macOS (Darwin).
# No external services are required; everything runs against the local FS.
set -euo pipefail

cd "$(dirname "$0")/.."

echo "== go version =="
go version

echo "== go mod tidy =="
go mod tidy

echo "== go build ./... =="
go build ./...

echo "== go vet ./... =="
go vet ./...

echo "== go test ./... =="
go test ./... -count=1

echo
echo "== acceptance demo =="
WORK="${WORK:-./tmp/acceptance}"
SEED="${SEED:-17}"
go run ./cmd/mediad demo --work "$WORK" --seed "$SEED"

echo
echo "All steps completed successfully."
