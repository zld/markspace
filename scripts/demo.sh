#!/usr/bin/env bash
#
# demo.sh runs the full mediq acceptance sequence locally, with no
# network access or external media tools. It mirrors the commands in
# README.md and exits non-zero if any invariant fails.
set -euo pipefail

cd "$(dirname "$0")/.."

echo "== go mod tidy =="
go mod tidy

echo "== go build ./... =="
go build ./...

echo "== go test ./... =="
go test ./...

echo "== clean demo =="
rm -rf tmp/demo
go run ./cmd/mediq demo --work ./tmp/demo --seed 23 || true
go run ./cmd/mediq verify --work ./tmp/demo

echo "== damaged demo with an injected crash =="
rm -rf tmp/broken
go run ./cmd/mediq demo \
  --work ./tmp/broken --seed 23 \
  --inject truncate,drop-index,corrupt-unit,misalign \
  --fail-after commit || true

echo "== recovery =="
go run ./cmd/mediq recover --work ./tmp/broken

echo
echo "All acceptance steps completed. Inspect tmp/demo and tmp/broken."
