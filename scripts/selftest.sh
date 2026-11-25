#!/usr/bin/env bash
# macOS Terminal entry point for the full local acceptance check.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if ! command -v go >/dev/null 2>&1; then
  if [[ "$(uname -s)" == "Darwin" ]] && command -v brew >/dev/null 2>&1; then
    brew install go
  else
    echo "Go 1.23+ is required. On macOS install Homebrew and run: brew install go" >&2
    exit 127
  fi
fi

go mod tidy
go build ./...
go test ./...
go run ./cmd/mediacore selftest --generate --work ./tmp --report text
