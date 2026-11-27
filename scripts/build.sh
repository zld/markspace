#!/usr/bin/env bash
# Build every package and the media-check binary on macOS.
set -euo pipefail
cd "$(dirname "$0")/.."

echo ">> go build ./..."
go build ./...

mkdir -p bin
go build -o bin/media-check ./cmd/media-check
echo ">> binary written to bin/media-check"
