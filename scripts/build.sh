#!/usr/bin/env bash
# Build all packages and the local mediarc binary under ./bin.
set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v go >/dev/null 2>&1; then
  brew install go
fi

go mod tidy
go build ./...
mkdir -p ./bin
go build -o ./bin/mediarc ./cmd/mediarc
echo "built ./bin/mediarc"
