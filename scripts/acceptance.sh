#!/usr/bin/env bash
# End-to-end acceptance for the continuity media-processing core.
#
# It builds the CLI (so program exit codes 0/2/64 are observed directly rather
# than being wrapped by `go run`, which maps every non-zero child exit to 1),
# then runs every scenario from the project acceptance criteria and asserts
# the expected stage counts, resume point, invalidation counters and codes.
set -euo pipefail

cd "$(dirname "$0")/.."

BIN="$(mktemp -d)/continuity"
trap 'rm -rf "$(dirname "$BIN")"' EXIT

echo ">> go build ./..."
go build ./...
go build -o "$BIN" ./cmd/continuity

echo ">> go test ./..."
go test ./... >/dev/null

pass=0
fail=0

check() { # desc expected actual
  if [ "$2" = "$3" ]; then
    echo "   ok: $1 = $3"
    pass=$((pass + 1))
  else
    echo "  FAIL: $1 expected [$2] got [$3]"
    fail=$((fail + 1))
  fi
}

WS_BASE="${TMPDIR:-/tmp}"

echo ">> scenario 1: interrupt after 5 then resume"
WS="$WS_BASE/continuity-accept-demo"
rm -rf "$WS"
OUT="$("$BIN" demo --workspace "$WS" --frames 12 --interrupt-after 5 --resume)"
CODE=$?
check "scenario1 exit code" 0 "$CODE"
check "discovered=12" 12 "$(echo "$OUT" | sed -n 's/.*discovered=\([0-9]*\).*/\1/p')"
check "validated=12" 12 "$(echo "$OUT" | grep -o 'validated=[0-9]*' | head -1 | cut -d= -f2)"
check "resume_from=6" 6 "$(echo "$OUT" | sed -n 's/.*resume_from=\([0-9]*\).*/\1/p')"

echo ">> scenario 2: repeated run is identical"
OUT="$("$BIN" demo --workspace "$WS" --frames 12 --resume)"
check "comparison=identical" "identical" "$(echo "$OUT" | sed -n 's/.*comparison=\([a-z]*\).*/\1/p')"
check "changed_hashes=0" 0 "$(echo "$OUT" | sed -n 's/.*changed_hashes=\([0-9]*\).*/\1/p')"

echo ">> scenario 3: truncate injection fails at frame 3"
WS="$WS_BASE/continuity-accept-bad"
rm -rf "$WS"
set +e
ERR="$("$BIN" demo --workspace "$WS" --inject truncate --frames 12 2>&1)"
CODE=$?
set -e
check "truncate exit code" 2 "$CODE"
check "error_stage=manifest" "manifest" "$(echo "$ERR" | sed -n 's/.*error_stage=\([a-z]*\).*/\1/p')"
check "error_frame=3" 3 "$(echo "$ERR" | sed -n 's/.*error_frame=\([0-9]*\).*/\1/p')"
OFFSET="$(echo "$ERR" | sed -n 's/.*error_offset=\([0-9]*\).*/\1/p')"
if [ "$OFFSET" -ge 0 ] 2>/dev/null; then echo "   ok: error_offset non-negative = $OFFSET"; pass=$((pass+1)); else echo "  FAIL: offset $OFFSET"; fail=$((fail+1)); fi

echo ">> scenario 4: metadata mutation invalidates index only"
WS="$WS_BASE/continuity-accept-meta"
rm -rf "$WS"
OUT="$("$BIN" demo --workspace "$WS" --mutate metadata --frames 12)"
check "metadata content invalidations" 0 "$(echo "$OUT" | sed -n 's/.*cache_invalidations=\([0-9]*\).*/\1/p')"
check "metadata index invalidations" 1 "$(echo "$OUT" | sed -n 's/.*index_invalidations=\([0-9]*\).*/\1/p')"

echo ">> scenario 5: replace and container mutations"
WS="$WS_BASE/continuity-accept-replace"
rm -rf "$WS"
OUT="$("$BIN" demo --workspace "$WS" --mutate replace --frames 12)"
check "replace content invalidations" 1 "$(echo "$OUT" | sed -n 's/.*cache_invalidations=\([0-9]*\).*/\1/p')"
check "replace reason" "content-replaced" "$(echo "$OUT" | sed -n 's/.*cache_reason=\([a-z-]*\).*/\1/p')"

WS="$WS_BASE/continuity-accept-container"
rm -rf "$WS"
OUT="$("$BIN" demo --workspace "$WS" --mutate container --frames 12)"
check "container index invalidations" 1 "$(echo "$OUT" | sed -n 's/.*index_invalidations=\([0-9]*\).*/\1/p')"
check "container reason" "container-changed" "$(echo "$OUT" | sed -n 's/.*cache_reason=\([a-z-]*\).*/\1/p')"

echo ">> scenario 6: argument error exit code"
set +e
"$BIN" demo >/dev/null 2>&1
CODE=$?
set -e
check "usage exit code" 64 "$CODE"

echo
echo "================ ACCEPTANCE: $pass passed, $fail failed ================"
[ "$fail" -eq 0 ]
