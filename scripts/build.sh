#!/usr/bin/env bash
# Build every package and the verify command. Purely local; no network or
# external services are used beyond the module cache populated by
# `go mod download`.
set -euo pipefail

cd "$(dirname "$0")/.."

echo ">> go mod download"
go mod download

echo ">> go build ./..."
go build ./...

echo ">> go vet ./..."
go vet ./...

echo "build OK"
