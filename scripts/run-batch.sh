#!/usr/bin/env bash
# Run the generated demo manifest directly. The demo manifest intentionally contains
# rejected and policy-failing items, so this command normally exits 1.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

go run ./cmd/mediactl run \
  --work ./tmp \
  --manifest ./tmp/manifest.json \
  "$@"
