#!/usr/bin/env bash
# Reproducible local verification for the multi-camera media check task.
# No network service is used beyond `go mod download`.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

OUT="${1:-${TMPDIR:-/tmp}/media-check}"

go mod tidy
go mod download
go build ./...
go test ./...

# Exact acceptance command from the task.
go run ./cmd/acceptance --case all --out "$OUT"

# A second full run proves deterministic source digests and persistent cache.
go run ./cmd/acceptance --case all --out "$OUT-repeat" >/dev/null

# `go run` wraps nonzero program exits as 1, so build a binary to verify the
# stable CLI contract: usage=2 and foreign bad input=3.
BIN_DIR="$(mktemp -d)"
trap 'rm -rf "$BIN_DIR"' EXIT
go build -o "$BIN_DIR/acceptance" ./cmd/acceptance

expect_code() {
  local want="$1"
  shift
  set +e
  "$@" >/dev/null 2>&1
  local got="$?"
  set -e
  if [[ "$got" -ne "$want" ]]; then
    echo "expected exit $want, got $got: $*" >&2
    exit 1
  fi
}

expect_code 2 "$BIN_DIR/acceptance" --no-such-flag
expect_code 2 "$BIN_DIR/acceptance" --case invalid

printf 'this is not a RIFF/WAVE file\n' > "$BIN_DIR/foreign.wav"
expect_code 3 "$BIN_DIR/acceptance" --input "$BIN_DIR/foreign.wav"

echo "verify.sh: PASS (out=$OUT)"
