#!/usr/bin/env bash
# Reproducible acceptance run for the tracecheck media investigation kernel.
# Runs fully offline; generated samples are produced by the binary itself.
set -euo pipefail

cd "$(dirname "$0")/.."

run() {
	echo "==> $*"
	"$@"
}

run go mod tidy
run go build ./...
run go test ./...
run go run ./cmd/tracecheck --generate --profile clean --seed 7
run go run ./cmd/tracecheck --generate --profile damaged --seed 7 \
	--workers 4 --queue 8 --loss-budget 2 --keyframe-budget 3
run go run ./cmd/tracecheck --generate --profile cancel --seed 7 \
	--cancel-after 5 --workers 2 --queue 2

echo
echo "acceptance: all profiles completed (exit codes are asserted by accept_check.sh)"
