#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

go mod tidy
go build ./...
go test ./...
go run ./cmd/mediadump --self-check
