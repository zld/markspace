#!/bin/bash
# Acceptance record for the continuity demo (see README.md).
set -euo pipefail
cd "$(dirname "$0")/.."

echo "== build & test =="
go mod tidy
go build ./...
go test ./...

BIN=$(mktemp -d)/continuity
go build -o "$BIN" ./cmd/continuity

echo "== case normal =="
out=$(go run ./cmd/continuity demo --case normal --seed 17)
sum=$(echo "$out" | tail -1)
for kv in mapped=6 emitted=5 duplicate=1 lost=0 p50_delay_ms=40 drift_ppm=80 max_abs_byte_delta=0; do
  [[ "$sum" == *"$kv"* ]] || { echo "FAIL normal: missing $kv"; exit 1; }
done
v=$(echo "$sum" | sed -n 's/.*max_abs_time_delta_us=\([0-9]*\).*/\1/p')
[ "$v" -le 1000 ] || { echo "FAIL normal: max_abs_time_delta_us=$v > 1000"; exit 1; }
[ "$(echo "$out" | grep -c '^map .*sha256=')" -eq 6 ] || { echo "FAIL normal: want 6 mapping rows with sha256"; exit 1; }

echo "== case damaged =="
set +e
err=$("$BIN" demo --case damaged --seed 17 2>&1 >/dev/null)
code=$?
set -e
[ "$code" -eq 2 ] || { echo "FAIL damaged: exit=$code, want 2"; exit 1; }
for tok in 'segment\[2\]' 'byte=24' 'truncated_box'; do
  echo "$err" | grep -q "$tok" || { echo "FAIL damaged: missing $tok in: $err"; exit 1; }
done
# go run masks exit codes; it must still surface "exit status 2"
go run ./cmd/continuity demo --case damaged --seed 17 2>&1 >/dev/null | grep -q 'exit status 2' \
  || { echo "FAIL damaged: go run did not report exit status 2"; exit 1; }

echo "== case recover (twice, deterministic) =="
r1=$(go run ./cmd/continuity demo --case recover --seed 17 | tail -1)
r2=$(go run ./cmd/continuity demo --case recover --seed 17 | tail -1)
[ "$r1" == "$r2" ] || { echo "FAIL recover: summaries differ"; exit 1; }
for kv in lost=1 gap_ms=2000 resumed=1; do
  [[ "$r1" == *"$kv"* ]] || { echo "FAIL recover: missing $kv"; exit 1; }
done
d=$(echo "$r1" | sed -n 's/.*digest=\([0-9a-f]*\).*/\1/p')
[[ "$d" =~ ^[0-9a-f]{64}$ ]] || { echo "FAIL recover: digest not 64 hex: $d"; exit 1; }

echo "ACCEPTANCE OK"
