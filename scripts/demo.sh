#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if command -v brew >/dev/null 2>&1; then
  BREW_GO="$(brew --prefix go@1.23 2>/dev/null || true)"
  if [[ -n "$BREW_GO" && -x "$BREW_GO/bin/go" ]]; then
    export PATH="$BREW_GO/bin:$PATH"
  fi
fi

exec go run ./cmd/archivecheck demo \
  --case all --seed 17 --queue 2 \
  --memory 67108864 --slow-ms 2 "$@"
