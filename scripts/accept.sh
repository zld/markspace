#!/usr/bin/env bash
# Reproducible acceptance run for the tracecheck media investigation kernel.
# Runs fully offline; generated samples are produced by the binary itself.
# Exit codes are asserted on the built binary because `go run` maps every
# non-zero child exit to its own exit code 1.
set -euo pipefail

cd "$(dirname "$0")/.."

run() {
	echo "==> $*"
	"$@"
}

run go mod tidy
run go build ./...
run go test ./...

# Build the acceptance binary into a gitignored output directory.
mkdir -p out
run go build -o out/tracecheck ./cmd/tracecheck

assert_exit() {
	local want=$1
	shift
	set +e
	"$@"
	local got=$?
	set -e
	if [[ $got -ne $want ]]; then
		echo "FAIL: expected exit $want but got $got for: $*" >&2
		exit 1
	fi
	echo "OK: exit $want"
}

assert_exit 0   out/tracecheck --generate --profile clean --seed 7
assert_exit 2   out/tracecheck --generate --profile damaged --seed 7 \
	--workers 4 --queue 8 --loss-budget 2 --keyframe-budget 3
assert_exit 130 out/tracecheck --generate --profile cancel --seed 7 \
	--cancel-after 5 --workers 2 --queue 2

# Invalid parameters must map to exit code 3.
assert_exit 3   out/tracecheck --generate --profile bogus --seed 7

echo
echo "acceptance: all profiles and exit codes verified"
