#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
DIR="${CONTINUITY_VERIFY_DIR:-/tmp/pwf-verify}"

go mod tidy
go build ./...
go test ./...

rm -rf "$DIR"
go run ./cmd/continuity demo \
  --dir "$DIR" \
  --seed 17 \
  --min-coverage-ppm 999000 \
  --max-av-skew-ms 20 \
  --max-bitrate-bps 2400000 \
  --budget-ms 2000 | tee "$DIR-demo.log"

grep -q 'offset_delta=0' "$DIR-demo.log"
grep -Eq '"output_sha256": "[0-9a-f]{64}"' "$DIR-demo.log"
grep -q '"replay_sha256_match": true' "$DIR-demo.log"
grep -q 'replay_sha256_match=true' "$DIR-demo.log"

expect_code() {
  local want="$1"
  shift
  set +e
  "$@" >"$DIR-command.out" 2>&1
  local got=$?
  set -e
  if [[ "$got" -ne "$want" ]]; then
    cat "$DIR-command.out"
    echo "expected exit $want, got $got: $*" >&2
    exit 1
  fi
}

expect_code 2 go run ./cmd/continuity inspect --file "$DIR/truncated-vint.mkv" --expect-offset 4
expect_code 3 go run ./cmd/continuity plan --dir "$DIR" --budget-ms 1 --max-bitrate-bps 100000
expect_code 4 go run ./cmd/continuity resume --dir "$DIR/checkpoint-conflict" --fixture-dir "$DIR" --lfe-mix

echo "verify_ok: demo=0 input_corrupt=2 infeasible=3 checkpoint_conflict=4"
