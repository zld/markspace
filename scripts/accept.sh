#!/usr/bin/env bash
# End-to-end acceptance. Generates synthetic fixtures under the project
# temporary directory, runs the shared pipeline, then resumes and confirms
# the index digest is stable across the interruption boundary.
#
# Exit codes (from cmd/verify):
#   0 processing and assertions passed
#   2 corrupted input (expected for the truncated fixture)
#   3 checkpoint or index inconsistency
#   1 any other failure
set -euo pipefail

cd "$(dirname "$0")/.."

OUT="${1:-.tmp/accept}"
CHUNK="${2:-2s}"

echo ">> race-enabled module tests"
go test -race ./...

echo ">> generate: ${OUT}"
rm -rf "${OUT}"
go run ./cmd/verify --generate --out "${OUT}" --chunk "${CHUNK}"

echo ">> resume: ${OUT}"
go run ./cmd/verify --resume --out "${OUT}"

echo "acceptance OK (artifacts in ${OUT})"
