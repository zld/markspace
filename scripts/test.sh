#!/usr/bin/env bash
# Run the full test suite. All fixtures are self-generated, no network needed.
set -euo pipefail
cd "$(dirname "$0")/.."

echo ">> go vet ./..."
go vet ./...

echo ">> go test ./..."
go test ./...
