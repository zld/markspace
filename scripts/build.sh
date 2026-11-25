#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT"

# Third-party Go modules are the supported distribution mechanism. The named
# go-mp4/pion/rtp dependencies are pinned in go.mod.
go mod download
go build ./...
mkdir -p "$ROOT/tmp/bin"
go build -o "$ROOT/tmp/bin/mediacore" ./cmd/mediacore
echo "built $ROOT/tmp/bin/mediacore"
