#!/usr/bin/env bash
# Install the Go 1.23 toolchain on macOS via Homebrew and pin module deps.
# Re-running is safe: existing installs are left untouched.
set -euo pipefail

if ! command -v brew >/dev/null 2>&1; then
  echo "Homebrew is required: https://brew.sh" >&2
  exit 2
fi

if ! command -v go >/dev/null 2>&1; then
  brew install go
else
  echo "go already installed: $(go version)"
fi

# The module declares 'go 1.23'; any newer local toolchain builds it.
cd "$(dirname "$0")/.."
go mod tidy
echo "Dependencies installed and pinned in go.mod / go.sum"
