#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

FIRST="${1:-/tmp/media-audit-repeat-1.json}"
SECOND="${2:-/tmp/media-audit-repeat-2.json}"

go run ./cmd/media-audit --self-test --out "$FIRST" >/dev/null
go run ./cmd/media-audit --self-test --out "$SECOND" >/dev/null
cmp "$FIRST" "$SECOND"
echo "repeat manifests identical: $FIRST == $SECOND"
