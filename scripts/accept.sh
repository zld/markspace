#!/bin/sh
# Reproduce the acceptance transcript: build a binary, run a damaged demo with a
# checkpoint, then resume it and confirm the checksums match.
#
# The acceptance instructions use `go run`, which prints the program's exit code
# ("exit status 3") but itself exits 1 for any non-zero code. The script builds
# ./archivecheck so the real exit codes can be asserted.
set -eu
cd "$(dirname "$0")/.."
state=${STATE:-/tmp/archivecheck-state.json}
bin=$(mktemp -d)/archivecheck

go mod tidy
go build ./...
go test ./...
go build -o "$bin" ./cmd/archivecheck

rm -f "$state"
echo "== first run (burst-loss)"
rc=0
"$bin" demo --seed 42 --fault burst-loss --checkpoint "$state" > "$state.first.txt" || rc=$?
first_rc=$rc
echo "== resume"
rc=0
"$bin" demo --seed 42 --resume --checkpoint "$state" > "$state.resume.txt" || rc=$?
second_rc=$rc

first_sum=$(sed -n 's/^output_sha256=//p' "$state.first.txt")
resume_sum=$(sed -n 's/^output_sha256=//p' "$state.resume.txt")
resumed=$(sed -n 's/^resumed_nodes=//p' "$state.resume.txt")

echo "== first exit=$first_rc resume exit=$second_rc"
echo "== first checksum  $first_sum"
echo "== resume checksum $resume_sum (resumed_nodes=$resumed)"

if [ "$first_rc" != 3 ] || [ "$second_rc" != 3 ]; then
    echo "FAIL: expected exit 3 for a damaged archive" >&2
    exit 1
fi
if [ "$first_sum" != "$resume_sum" ]; then
    echo "FAIL: checksum changed across resume" >&2
    exit 1
fi
if [ "$resumed" != 0 ]; then
    echo "FAIL: resume re-executed $resumed nodes" >&2
    exit 1
fi
echo "OK"
