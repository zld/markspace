#!/usr/bin/env bash
# Reproducible acceptance driver for the mediacheck offline kernel.
# Runs entirely locally with no network access; safe to re-run. A built binary
# (not `go run`) is used for fault cases so real exit codes are observable.
set -euo pipefail

cd "$(dirname "$0")/.."

echo "== go mod tidy =="
go mod tidy

echo "== go build ./... =="
go build ./...

bin="$(mktemp -t mediacheck.XXXXXX)"
go build -o "$bin" ./cmd/mediacheck
trap 'rm -f "$bin"' EXIT

echo "== go test ./... =="
go test ./...

echo "== demo: clean multi-representation run =="
"$bin" demo --seed 7 --rates 3 --segments 12 --checkpoint-every 4

echo "== demo: cluster truncation (byte-exact fault) =="
set +e
"$bin" demo --corrupt cluster-truncate
rc=$?
set -e
echo "exit_code=$rc (want 2)"
if [ "$rc" -ne 2 ]; then
  echo "FAIL: expected exit 2 for corrupt material" >&2
  exit 1
fi

echo "== demo: interrupt then resume (identical output hash) =="
work="$(mktemp -d)"
set +e
"$bin" demo --interrupt-after 8 --workdir "$work"
rc=$?
set -e
echo "interrupt_exit_code=$rc (want 3)"
if [ "$rc" -ne 3 ]; then
  echo "FAIL: expected exit 3 for interrupted run" >&2
  exit 1
fi
"$bin" demo --interrupt-after 8 --resume --workdir "$work"
rm -rf "$work"
