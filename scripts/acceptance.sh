#!/usr/bin/env bash
# macOS acceptance entry point: install Go if needed, verify, and run both demos.
set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v go >/dev/null 2>&1; then
  brew install go
fi

go mod tidy
go build ./...
go test ./...

go run ./cmd/mediacore demo \
  --fixture generated \
  --range 00:10:00,00:10:12 \
  --crash-after publish \
  --rerun

go run ./cmd/mediacore demo --fixture corrupt --all
