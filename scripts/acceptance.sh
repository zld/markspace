#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ -x /opt/homebrew/opt/go@1.23/bin/go ]]; then
  GO=/opt/homebrew/opt/go@1.23/bin/go
else
  GO="$(command -v go)"
fi
export GOTOOLCHAIN=local

echo "== tidy/build/test =="
"$GO" mod tidy
"$GO" build ./...
"$GO" test ./...
"$GO" build -o bin/archivecheck ./cmd/archivecheck

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "== worker=3 demo (expected exit 2) =="
code=0
./bin/archivecheck demo --seed 17 --workers 3 --budget-bytes 1048576 > "$TMP/demo3.out" 2> "$TMP/demo3.err" || code=$?
[[ "$code" -eq 2 ]]
grep -q 'phase=clock code=ILLEGAL_DROP_FRAME sample=8 offset=9' "$TMP/demo3.out"
grep -q 'phase=policy code=BUDGET_INFEASIBLE sample=12 offset=320' "$TMP/demo3.out"
grep -q 'budget_bytes=1048576 total_output_bytes=' "$TMP/demo3.out"
grep -q 'within=true' "$TMP/demo3.out"

echo "== worker=1 digest/mapping comparison =="
code=0
./bin/archivecheck demo --seed 17 --workers 1 --budget-bytes 1048576 > "$TMP/demo1.out" || code=$?
[[ "$code" -eq 2 ]]
D3="$(sed -n 's/^stable_digest=\([0-9a-f]\{64\}\)$/\1/p' "$TMP/demo3.out" | tail -1)"
D1="$(sed -n 's/^stable_digest=\([0-9a-f]\{64\}\)$/\1/p' "$TMP/demo1.out" | tail -1)"
[[ -n "$D3" && "$D3" == "$D1" ]]
grep '^item ' "$TMP/demo3.out" > "$TMP/items3"
grep '^item ' "$TMP/demo1.out" > "$TMP/items1"
diff -u "$TMP/items3" "$TMP/items1"

echo "== cancellation/resume =="
code=0
./bin/archivecheck demo --seed 17 --workers 3 --cancel-after 2 > "$TMP/cancel.out" 2> "$TMP/cancel.err" || code=$?
[[ "$code" -eq 2 ]]
grep -Eq 'cancel_run total=13 completed=2 canceled=[1-9][0-9]* not_started=[1-9][0-9]*' "$TMP/cancel.out"
RD="$(sed -n 's/^rerun_jobs=[0-9]* resume_digest=\([0-9a-f]\{64\}\)$/\1/p' "$TMP/cancel.out")"
FD="$(sed -n 's/^full_digest=\([0-9a-f]\{64\}\)$/\1/p' "$TMP/cancel.out")"
[[ "$RD" == "$FD" ]]
grep -q 'resume_matches_full=true' "$TMP/cancel.out"

echo "== usage error =="
code=0
./bin/archivecheck definitely-invalid >/dev/null 2>&1 || code=$?
[[ "$code" -eq 64 ]]

echo "ACCEPTANCE OK: digest=$D3"
