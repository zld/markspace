#!/usr/bin/env bash
# Build, vet and test the entire mediapack kernel.
# Usage: ./scripts/verify.sh
set -euo pipefail

cd "$(dirname "$0")/.."

echo "== go mod tidy =="
go mod tidy

echo "== go build ./... =="
go build ./...

echo "== go vet ./... =="
go vet ./...

echo "== go test ./... =="
go test ./... -count=1 -timeout 120s

echo "== acceptance summary =="
go test ./internal/acceptance/ -run TestAcceptanceReport -v -count=1 2>&1 | grep -E "acceptance|passed"

echo "ALL CHECKS PASSED"
