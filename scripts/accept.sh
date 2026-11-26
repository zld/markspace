#!/bin/sh
# Acceptance script: exercises the full contract. Exit codes 0/1/2 are asserted
# against a built binary, because `go run` collapses every non-zero program exit
# to 1. The same scenarios are also shown through `go run`, where only
# "non-zero" is observable.
# Usage: ./scripts/accept.sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

bin=$(mktemp -t continuity-bin.XXXXXX)
trap 'rm -f "$bin"' EXIT
pass=0
fail=0
ok() { pass=$((pass+1)); printf 'ok   %s\n' "$1"; }
bad() { fail=$((fail+1)); printf 'FAIL %s\n' "$1"; }

echo "== build =="
go mod download
go mod tidy
go build ./...
go test ./...
go build -o "$bin" ./cmd/continuity
ok "build and unit tests"

kv() { printf '%s\n' "$1" | sed -n "s/^$2=//p"; }
check_key() {
	name=$1; want=$2
	got=$(kv "$REPORT" "$name")
	if [ "$got" = "$want" ]; then ok "demo $name=$want"; else bad "demo $name=$got (want $want)"; fi
}

echo "== demo (normal) =="
REPORT=$("$bin" demo --format hls,dash --late-ms 500 --queue-bytes 1048576)
sha1=$(kv "$REPORT" sha256)
check_key samples 8
check_key time_delta_ticks 0
check_key byte_delta 0
check_key mapping_missing 0
check_key failure_location none
REPORT2=$("$bin" demo --format hls,dash --late-ms 500 --queue-bytes 1048576)
sha2=$(kv "$REPORT2" sha256)
if [ -n "$sha1" ] && [ "$sha1" = "$sha2" ]; then ok "demo digest stable"; else bad "demo digest differs ($sha1 vs $sha2)"; fi

echo "== fault: truncated-box =="
set +e
tfc=$("$bin" demo --fault truncated-box 2>&1)
tcode=$?
set -e
if [ "$tcode" -eq 2 ]; then ok "truncated exit 2"; else bad "truncated exit $tcode"; fi
if printf '%s\n' "$tfc" | grep -Eq 'failure_location=[^ ]*\.m4s@[0-9]+'; then
	ok "truncated reports fragment and byte offset"
else
	bad "truncated missing fragment/offset"
fi

echo "== fault: late-with-checkpoint =="
lc=$("$bin" demo --fault late-with-checkpoint)
lrec=$(kv "$lc" late_recomputed)
lrej=$(kv "$lc" late_rejected)
lsha1=$(kv "$lc" sha256)
if [ "$lrec" = "1" ]; then ok "late_recomputed=1"; else bad "late_recomputed=$lrec"; fi
if [ "$lrej" = "1" ]; then ok "late_rejected=1"; else bad "late_rejected=$lrej"; fi
lc2=$("$bin" demo --fault late-with-checkpoint)
lsha2=$(kv "$lc2" sha256)
if [ -n "$lsha1" ] && [ "$lsha1" = "$lsha2" ]; then ok "recovery digest stable"; else bad "recovery digest differs"; fi

echo "== exit-code contract =="
set +e
"$bin" demo --format bogus >/dev/null 2>&1; ucode=$?
"$bin" demo --fault truncated-box >/dev/null 2>&1; dcode=$?
"$bin" demo >/dev/null 2>&1; scode=$?
set -e
if [ "$ucode" -eq 1 ]; then ok "usage exit 1"; else bad "usage exit $ucode"; fi
if [ "$dcode" -eq 2 ]; then ok "rejection exit 2"; else bad "rejection exit $dcode"; fi
if [ "$scode" -eq 0 ]; then ok "success exit 0"; else bad "success exit $scode"; fi

echo "== go run masking note =="
set +e
go run ./cmd/continuity demo --fault truncated-box >/dev/null 2>&1; grc=$?
set -e
if [ "$grc" -ne 0 ]; then ok "go run surfaces non-zero (masked to $grc)"; else bad "go run returned 0 for a failing run"; fi

echo
printf 'passed=%d failed=%d\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
