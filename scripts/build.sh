#!/usr/bin/env bash
# Build all mediad packages. Runs from the project root.
set -euo pipefail
cd "$(dirname "$0")/.."
go mod tidy
go build ./...
echo "build OK"
