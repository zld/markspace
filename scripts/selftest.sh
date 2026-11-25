#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
exec go run ./cmd/replaycheck --self-test --chunk 4 --index ./tmp/index.json
