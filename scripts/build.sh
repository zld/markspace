#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ -x /opt/homebrew/opt/go@1.23/bin/go ]]; then
  GO=/opt/homebrew/opt/go@1.23/bin/go
else
  GO="$(command -v go)"
fi

mkdir -p bin
GOTOOLCHAIN=local "$GO" build -o bin/archivecheck ./cmd/archivecheck
echo "built $ROOT/bin/archivecheck with $("$GO" version)"
