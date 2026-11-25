#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

go mod tidy
go build ./...
go test ./...
go run ./cmd/media-audit --self-test --out /tmp/media-audit.json
