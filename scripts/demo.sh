#!/bin/sh
# Runs the documented demo command with the acceptance parameters.
set -eu
cd "$(dirname "$0")/.."
exec go run ./cmd/mediacore demo \
  --codec av1 --seed 17 --loss 3 --reorder 2 \
  --budget-ms 5000 --quality-min 30 --target-kbps 900 \
  --dir "${1:-.mediacore-demo}" "${@:2}"
