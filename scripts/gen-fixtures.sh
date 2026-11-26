#!/bin/zsh
set -euo pipefail
export PATH="$(brew --prefix go@1.23 2>/dev/null)/bin:$PATH"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${1:-${TMPDIR:-/tmp}/assetpack-fixtures}"
cd "$ROOT"
go run ./cmd/genfixtures --out "$OUT"
