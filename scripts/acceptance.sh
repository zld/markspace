#!/bin/sh
# Acceptance script for the continuity kernel. Run from anywhere:
#   sh scripts/acceptance.sh
# Exits 0 only if every check passes.
set -eu
cd "$(dirname "$0")/.."

echo "== build & unit tests =="
go mod tidy
go build ./...
go test ./...

rm -rf ./tmp
go build -o ./tmp/continuity ./cmd/continuity

run() { # run <expected-exit> <args...>
	want=$1; shift
	set +e
	out=$("$@" 2>&1); code=$?
	set -e
	printf '%s\n' "$out"
	if [ "$code" -ne "$want" ]; then
		echo "FAIL: expected exit $want, got $code: $*" >&2
		exit 1
	fi
}

echo "== demo run (damaged fixtures -> exit 2) =="
run 2 ./tmp/continuity demo --seed 17 --out ./tmp/demo | tee ./tmp/summary.txt

echo "== bitrate deviation within +/-5% =="
devs=$(grep -o '([+-][0-9.]*%)' ./tmp/summary.txt | tr -d '()%+')
for d in $devs; do
	awk -v d="$d" 'BEGIN { if (d < 0) d = -d; if (d > 5) exit 1 }' || {
		echo "FAIL: bitrate deviation ${d}% exceeds 5%" >&2; exit 1; }
done

echo "== alignment error within one audio sample tick =="
align=$(grep '^alignment error:' ./tmp/summary.txt | awk '{print $3}')
[ "$align" -le 1 ] || { echo "FAIL: alignment error $align > 1" >&2; exit 1; }

echo "== interrupt then resume: manifest must be byte-identical =="
run 1 ./tmp/continuity demo --seed 17 --out ./tmp/resume --interrupt-after scan
run 2 ./tmp/continuity demo --seed 17 --out ./tmp/resume
cmp ./tmp/demo/manifest.json ./tmp/resume/manifest.json || {
	echo "FAIL: resumed manifest differs" >&2; exit 1; }
grep -q "fixtures=cached" ./tmp/resume/summary.txt 2>/dev/null || true

echo "== config change must refuse old checkpoint (exit 1) =="
run 1 ./tmp/continuity demo --seed 17 --out ./tmp/resume --max-keyframe-ticks 90000

echo "== input change must refuse old checkpoint (exit 1) =="
run 1 ./tmp/continuity demo --seed 18 --out ./tmp/resume

echo "ACCEPTANCE OK"
