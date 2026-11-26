#!/usr/bin/env bash
# Reproduce the project acceptance checks from a macOS Terminal.
set -euo pipefail

cd "$(dirname "$0")/.."

go mod tidy
go build ./...
go test ./...

go run ./cmd/verify \
  -case clean \
  -range 2s:3s \
  -iterations 100 \
  -mem 8MiB \
  -queue 4

go run ./cmd/verify \
  -case faults \
  -range 2s:3s \
  -iterations 100 \
  -mem 8MiB \
  -queue 4 \
  -resume \
  -repeat 2
