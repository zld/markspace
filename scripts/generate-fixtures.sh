#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
root="${1:-.tmp/fixtures}"
go run ./cmd/verify -scenario normal -root "$root" -checkpoint-frames 4
