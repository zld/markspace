#!/usr/bin/env bash
set -euo pipefail

# Reproducible local build entry point for macOS/Terminal.
cd "$(dirname "$0")/.."

go mod tidy
mkdir -p bin
go build -o bin/replaycheck ./cmd/replaycheck

echo "built: bin/replaycheck"
