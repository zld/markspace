#!/usr/bin/env bash
#
# demo.sh - reproducible offline demo of the subsync pipeline.
#
# It vendors dependencies, generates the built-in UTF-16LE SRT fixture (8
# cues, one overlapping pair), converts it to WebVTT at 23.976 fps with the
# clip policy, and prints the nine JSONL records. Works on Linux and macOS;
# requires only the Go toolchain and a POSIX shell.
set -euo pipefail

# Resolve the repository root from this script's own location so the command
# works regardless of the caller's working directory.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

go mod download
go mod vendor

# shellcheck disable=SC2068
go run -mod=vendor ./cmd/subsync demo \
  --input-format srt \
  --output-format vtt \
  --fps 24000/1001 \
  --overlap clip \
  --jsonl "$@"
