#!/usr/bin/env bash
set -euo pipefail

# Reproduce the local macOS acceptance run from the repository root.
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

go mod tidy
go build ./...
go test ./...
go run ./cmd/verify \
  --self-generate \
  --max-memory 8MiB \
  --checkpoint .tmp/checkpoint.json
