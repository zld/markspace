#!/usr/bin/env bash
# Local acceptance sequence from the project brief.
set -euo pipefail

SEED="${SEED:-7}"
OUT_A="${OUT_A:-/tmp/media-demo-a}"
OUT_B="${OUT_B:-/tmp/media-demo-b}"

echo "== go mod tidy =="
go mod tidy

echo "== go build ./... =="
go build ./...

echo "== go vet ./... =="
go vet ./...

echo "== go test ./... =="
go test ./...

rm -rf "$OUT_A" "$OUT_B"
mkdir -p "$OUT_A" "$OUT_B"
go build -o "$OUT_A/mediacheck" ./cmd/mediacheck
BIN="$OUT_A/mediacheck"

echo "== demo run A =="
"$BIN" demo --seed "$SEED" --out "$OUT_A" > "$OUT_A.stdout"

echo "== demo run B =="
"$BIN" demo --seed "$SEED" --out "$OUT_B" > "$OUT_B.stdout"

echo "== compare deterministic output =="
diff -u "$OUT_A.stdout" "$OUT_B.stdout"
diff -u "$OUT_A/summary.txt" "$OUT_B/summary.txt"
diff -u "$OUT_A/report.txt" "$OUT_B/report.txt"

echo "== scenario exit codes =="
"$BIN" demo --seed "$SEED" --out "$OUT_A/normal" --scenario normal >/dev/null

code=0
"$BIN" demo --seed "$SEED" --out "$OUT_A/damage" \
  --scenario truncated_nal >/dev/null || code=$?
test "$code" -eq 2

code=0
"$BIN" demo --seed "$SEED" --out "$OUT_A/abort" \
  --scenario commit_interrupt >/dev/null || code=$?
test "$code" -eq 3

code=0
"$BIN" demo --seed "$SEED" --out "$OUT_A/bad" \
  --scenario no-such-scenario >/dev/null || code=$?
test "$code" -eq 1

echo "acceptance passed"
