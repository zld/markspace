#!/usr/bin/env bash
# Canonical local acceptance check for medpipe.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

go mod tidy
go build ./...
go test ./...
go run ./cmd/medpipe --selftest --seed 17 --budget-ms 5000 --max-bytes 1048576
