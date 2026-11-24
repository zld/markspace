#!/bin/bash
set -euo pipefail

# Build all terminal binaries for local macOS use.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

mkdir -p bin
go build -o bin/medpipe ./cmd/medpipe
go build -o bin/genfixture ./cmd/genfixture

echo "built: $ROOT/bin/medpipe"
echo "built: $ROOT/bin/genfixture"
