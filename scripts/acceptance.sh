#!/usr/bin/env bash
# Reproduce the terminal acceptance record locally.
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

fail() { echo "ACCEPTANCE_FAIL: $*" >&2; exit 1; }

go mod tidy || fail "go mod tidy"
go build ./... || fail "go build"
go test ./... || fail "go test"

rm -rf /tmp/replay-a /tmp/replay-b /tmp/replay-mal /tmp/replay-tamper

echo "== clean first run =="
out="$(go run ./cmd/demo --fixture clean --out /tmp/replay-a)"
echo "$out"
grep -q '^stages=ingest,parse,cut,manifest$' <<<"$out" || fail "stages"
grep -q '^wraps=1$' <<<"$out" || fail "wraps"
grep -q '^cuts=2$' <<<"$out" || fail "cuts"
grep -q '^silence_ms=1000$' <<<"$out" || fail "silence"
grep -q '^created=2 reused=0 invalidated=0$' <<<"$out" || fail "first cache"
grep -q '^manifest_valid=true$' <<<"$out" || fail "manifest valid"

echo "== clean repeat =="
out="$(go run ./cmd/demo --fixture clean --out /tmp/replay-a)"
grep -q '^created=0 reused=2 invalidated=0$' <<<"$out" || fail "repeat cache"

echo "== source mutation =="
out="$(go run ./cmd/demo --fixture clean --out /tmp/replay-a --mutate-source)"
grep -q '^invalidated=2$' <<<"$out" || fail "invalidation"

echo "== recoverable cut checkpoint =="
set +e
go run ./cmd/demo --fixture damaged --out /tmp/replay-b --stop-after cut > /tmp/replay-stop.out 2>&1
gorun_code=$?
set -e
cat /tmp/replay-stop.out
grep -q '^checkpoint=cut$' /tmp/replay-stop.out || fail "checkpoint marker"
grep -q 'gap_events=2' /tmp/replay-stop.out || fail "gap count"
grep -q 'exit_code=2' /tmp/replay-stop.out || fail "internal exit marker"
# "go run" itself normalizes any nonzero child status to 1. Verify the
# contracted process status with the compiled binary.
go build -o /tmp/replay-demo ./cmd/demo
set +e
/tmp/replay-demo --fixture damaged --out /tmp/replay-b --stop-after cut >/dev/null 2>&1
stop_code=$?
set -e
[[ "$stop_code" -eq 2 ]] || fail "binary stop exit code $stop_code"
echo "go_run_wrapper_exit=$gorun_code compiled_binary_exit=$stop_code"

echo "== resume =="
out="$(go run ./cmd/demo --fixture damaged --out /tmp/replay-b --resume)"
echo "$out"
grep -q '^resume_stage=manifest$' <<<"$out" || fail "resume stage"
grep -q '^resume_packet=[1-9][0-9]*$' <<<"$out" || fail "resume packet"
grep -q 'manifest_valid=true' <<<"$out" || fail "resume manifest"

echo "== malformed packet =="
set +e
go run ./cmd/demo --fixture malformed --out /tmp/replay-mal >/tmp/replay-mal.out 2>&1
mal_code=$?
set -e
[[ "$mal_code" -eq 1 ]] || fail "malformed exit $mal_code"
grep -q 'packet_too_large' /tmp/replay-mal.out || fail "malformed marker"

echo "== final manifest verification failure =="
set +e
go run ./cmd/demo --fixture clean --out /tmp/replay-tamper --tamper-output >/tmp/replay-tamper.out 2>&1
tamper_code=$?
set -e
[[ "$tamper_code" -eq 1 ]] || fail "tamper exit $tamper_code"
grep -q 'manifest_valid=false' /tmp/replay-tamper.out || fail "tamper validity"

echo "ACCEPTANCE_OK"
