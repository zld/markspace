#!/bin/sh
# Acceptance script for the medialab kernel.
#
# Runs every command the brief lists and asserts the documented exit codes:
#   0 success or recoverable warnings, 1 input hard error, 2 verify failure.
# It is intentionally dependency-free so it works in a plain macOS Terminal.
set -u

cd "$(dirname "$0")/.." || exit 1

fail=0
check() {
	label=$1
	want=$2
	shift 2
	"$@" >/tmp/medialab-accept.out 2>&1
	got=$?
	if [ "$got" -eq "$want" ]; then
		printf 'ok   %-22s exit=%d\n' "$label" "$got"
	else
		printf 'FAIL %-22s exit=%d want=%d\n' "$label" "$got" "$want"
		sed 's/^/     /' /tmp/medialab-accept.out
		fail=1
	fi
}

echo "== go build / vet / test"
go build ./... || fail=1
go vet ./... || fail=1
go test ./... || fail=1

BIN=$(mktemp -d)/medialab
go build -o "$BIN" ./cmd/medialab || exit 1

echo "== cases"
check normal            0 "$BIN" demo --case normal --seed 7 --budget-ms 500 --max-bytes 1048576 --workers 2
check corrupt-hash      1 "$BIN" demo --case corrupt-hash --seed 7
check reorder-loss      0 "$BIN" demo --case reorder-loss --seed 7
check timestamp-regress 0 "$BIN" demo --case timestamp-regression --seed 7
check budget-conflict   1 "$BIN" demo --case budget-conflict --seed 7
check cache-boundary    0 "$BIN" demo --case cache-boundary --seed 7

echo "== determinism"
h1=$("$BIN" demo --case normal --seed 7 --budget-ms 500 --max-bytes 1048576 --workers 2 | sed -n 's/^run hash: *//p')
h2=$("$BIN" demo --case normal --seed 7 --budget-ms 500 --max-bytes 1048576 --workers 2 | sed -n 's/^run hash: *//p')
if [ -n "$h1" ] && [ "$h1" = "$h2" ]; then
	printf 'ok   %-22s %s\n' "fixed-seed rerun" "$h1"
else
	printf 'FAIL %-22s %s vs %s\n' "fixed-seed rerun" "$h1" "$h2"
	fail=1
fi

rm -f /tmp/medialab-accept.out
exit "$fail"
