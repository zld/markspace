#!/usr/bin/env bash
set -euo pipefail

# macOS local setup. Homebrew is the only system package manager assumed.
if ! command -v brew >/dev/null 2>&1; then
  echo "Homebrew is required: https://brew.sh/" >&2
  exit 1
fi
if ! command -v go >/dev/null 2>&1; then
  brew install go
fi

go version
go mod tidy
