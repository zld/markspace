#!/bin/sh
# Build, test, and verify that the demo reproduces the documented summary.
#
# Usage: scripts/verify.sh
#
# Requires Go and Capstone 5.  Runs entirely from the repository root; generated
# samples live under ./.work and are removed at the end.

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

workdir=".work"
old="$workdir/old.elf"
new="$workdir/new.elf"
json1="$workdir/report-1.json"
json2="$workdir/report-2.json"

cleanup() {
	rm -rf "$workdir"
}
trap cleanup EXIT INT TERM

echo "== gofmt"
unformatted=$(gofmt -l ./cmd ./internal 2>/dev/null || true)
if [ -n "$unformatted" ]; then
	echo "gofmt needed:" >&2
	echo "$unformatted" >&2
	exit 1
fi

echo "== go vet"
go vet ./...

echo "== go test"
go test ./...

mkdir -p "$workdir"

echo "== demo (deterministic reproduction)"
go run ./cmd/bbdiff --generate-fixtures --old "./$old" --new "./$new" --json >"$json1"
go run ./cmd/bbdiff --old "./$old" --new "./$new" --json >"$json2"

if ! cmp -s "$json1" "$json2"; then
	echo "FAIL: two analysis runs produced different JSON" >&2
	exit 1
fi

expected='{"added":1,"removed":1,"changed":2,"uncertain":0}'
actual=$(tr -d ' \n' <"$json1" | sed -n 's/.*"summary":\({[^}]*}\).*/\1/p')
if [ "$actual" != "$expected" ]; then
	echo "FAIL: summary = $actual, want $expected" >&2
	exit 1
fi

echo "== corrupt sample (uncertain path)"
go run ./cmd/bbdiff --generate-fixtures --new-variant corrupt \
	--old "./$old" --new "./$new" --json >"$json1"
actual=$(tr -d ' \n' <"$json1" | sed -n 's/.*"summary":\({[^}]*}\).*/\1/p')
if [ "$actual" != '{"added":1,"removed":1,"changed":2,"uncertain":1}' ]; then
	echo "FAIL: corrupt-variant summary = $actual" >&2
	exit 1
fi

echo "== OK: build, tests, demo and degraded path verified"
