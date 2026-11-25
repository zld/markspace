#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ -x /opt/homebrew/opt/go@1.23/bin/go ]]; then
  GO=/opt/homebrew/opt/go@1.23/bin/go
else
  GO="$(command -v go)"
fi

GOTOOLCHAIN=local "$GO" mod tidy
GOTOOLCHAIN=local "$GO" build ./...
GOTOOLCHAIN=local "$GO" test ./...
