#!/usr/bin/env bash
# Reproduce the local acceptance checks from the repository root.
set -euo pipefail

cd "$(dirname "$0")/.."

go mod tidy
go build ./...
go test ./...

# Normal sample exits 0 and prints the stable report.
go run ./cmd/mediacheck demo --seed 7 >/tmp/mediacheck-normal-1.json
go run ./cmd/mediacheck demo --seed 7 >/tmp/mediacheck-normal-2.json
cmp /tmp/mediacheck-normal-1.json /tmp/mediacheck-normal-2.json

# Input inconsistency/corruption exits 1.
! go run ./cmd/mediacheck demo --case truncated-cluster
! go run ./cmd/mediacheck demo --case audio-conflict
! go run ./cmd/mediacheck demo --case rtp-gap

# Bad arguments exit 2.
if go run ./cmd/mediacheck demo --case not-a-case >/dev/null 2>&1; then
  echo "expected invalid case to fail" >&2
  exit 1
fi

echo "acceptance checks passed"
