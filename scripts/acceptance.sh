#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

go mod tidy
go mod download
go build ./...
go test ./...

go run ./cmd/verify -scenario all -root .tmp/acceptance -checkpoint-frames 4

# Build the CLI for exact process-status checks. The go(1) driver reports
# any nonzero child status as exit status 1; the compiled binary preserves
# the application's 2/3/75 exit codes.
mkdir -p .tmp/bin
go build -o .tmp/bin/verify ./cmd/verify

rm -rf .tmp/recovery
set +e
.tmp/bin/verify -scenario interrupt -after-block 1 -root .tmp/recovery
interrupt_code=$?
set -e
if [[ "$interrupt_code" -ne 75 ]]; then
  echo "expected interrupt exit 75, got $interrupt_code" >&2
  exit 1
fi
if [[ ! -f .tmp/recovery/work/checkpoint.json ]]; then
  echo "committed checkpoint was not preserved" >&2
  exit 1
fi

.tmp/bin/verify -scenario resume -root .tmp/recovery
