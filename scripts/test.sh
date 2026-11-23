#!/usr/bin/env bash
# Run the full test suite with the race detector for the streaming package.
set -euo pipefail
cd "$(dirname "$0")/.."
go test ./...
go test -race ./stream/ ./engine/
echo "all tests OK"
