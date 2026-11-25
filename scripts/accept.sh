#!/usr/bin/env bash
# Local acceptance: tidy, build, full tests, then run the demo into a temp dir.
set -euo pipefail

cd "$(dirname "$0")/.."

OUT="${1:-/tmp/mediad-demo}"

echo "== go mod tidy =="
go mod tidy
echo "== go build ./... =="
go build ./...
echo "== go test ./... =="
go test ./...
echo "== demo -> ${OUT} =="
rm -rf "${OUT}"
go run ./cmd/mediad demo --out "${OUT}"

echo
echo "== true exit codes (compiled binary) =="
go build -o /tmp/mediad-accept ./cmd/mediad
set +e
/tmp/mediad-accept demo --out "${OUT}-corrupt" --scenario corrupt >/dev/null 2>&1
rc=$?
set -e
echo "corrupt scenario exit code = ${rc} (want 2)"
[ "${rc}" -eq 2 ]

set +e
/tmp/mediad-accept demo 2>/dev/null
rc=$?
set -e
echo "usage-error exit code = ${rc} (want 1)"
[ "${rc}" -eq 1 ]
echo "ALL ACCEPTANCE CHECKS PASSED"
