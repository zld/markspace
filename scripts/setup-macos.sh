#!/usr/bin/env bash
# Install the macOS prerequisites for the media pack kernel.
set -euo pipefail

if ! command -v brew >/dev/null 2>&1; then
  echo "Homebrew is required: https://brew.sh" >&2
  exit 1
fi

brew install go
go mod tidy
