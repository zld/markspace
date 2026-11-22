#!/usr/bin/env bash
# Local demo wrapper. `go run` masks a child's non-zero exit code with
# its own exit 1, so build first and execute the binary directly: error
# exit codes (2=INVALID_TIMESCALE, 3=REPLAY_WINDOW_EXCEEDED) then reach
# the terminal unchanged. No network access is required.
set -euo pipefail

cd "$(dirname "$0")/.."
bin="$(mktemp -d)/mediadist"
trap 'rm -rf "$(dirname "$bin")"' EXIT
go build -o "$bin" ./cmd/mediadist
"$bin" "$@"
