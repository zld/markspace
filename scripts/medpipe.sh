#!/usr/bin/env bash
# medpipe demo wrapper. `go run` exit-code propagation differs across Go
# toolchains (Go 1.23 forwards the child code; newer toolchains wrap it as 1),
# so this script builds a real binary and forwards its exit code unchanged:
# 0 success, 1 internal error, 2 located processing failure.
set -euo pipefail

cd "$(dirname "$0")/.."
BIN="$(go env GOCACHE)/medpipe-bin"
go build -o "$BIN" ./cmd/medpipe
exec "$BIN" "$@"
