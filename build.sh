#!/usr/bin/env bash
#
# build.sh compiles the mediapipeline acceptance binary into ./bin.
#
# Why a script: the acceptance contract distinguishes exit code 2 (damaged
# input, fully reported) from exit code 1 (budget/output failure).  The Go
# toolchain's `go run` deliberately does NOT propagate a child binary's exit
# code — see cmd/go/internal/run/run.go ("We ignore exit status"): every
# non-zero exit is surfaced by `go run` as 1 with an "exit status N" line.
# Running the compiled binary directly preserves the real 0/1/2 codes, so this
# script is the supported way to observe the full contract.
set -euo pipefail

cd "$(dirname "$0")"
mkdir -p bin
go build -o bin/mediapipeline ./cmd/mediapipeline
echo "built bin/mediapipeline"
