#!/usr/bin/env bash
# Acceptance script for assetcheck.
#
# It runs the exact `go run` invocations from the task for human-readable
# verification, and additionally builds a real binary to assert exact process
# exit codes. `go run` itself collapses any non-zero child exit to 1 and
# prints "exit status N", so exit-code contracts (0/2/3/4) are checked on the
# compiled binary, which behaves identically.
set -u -o pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

FIX="$ROOT/tmp-fixtures"
IDX="$ROOT/tmp-index"
BIN_DIR="$(mktemp -d)/assetcheck-bin"
BIN="$BIN_DIR/assetcheck"
PASS=0
FAIL=0

green() { printf '\033[32m%s\033[0m\n' "$*"; }
red()   { printf '\033[31m%s\033[0m\n' "$*"; }

check() { # check <name> <condition-arg...>; condition is a shell eval string
  local name="$1"; shift
  if eval "$*"; then
    green "PASS: $name"; PASS=$((PASS+1))
  else
    red "FAIL: $name"; FAIL=$((FAIL+1))
  fi
}

code_of() { # code_of <expected> <description> -- command...
  local expected="$1" desc="$2"; shift 2
  "$@" >/tmp/ac-run.out 2>/tmp/ac-run.err
  local got=$?
  if [[ "$got" == "$expected" ]]; then
    green "PASS: $desc (exit $got)"; PASS=$((PASS+1))
  else
    red "FAIL: $desc (want exit $expected, got $got)"; FAIL=$((FAIL+1))
    sed -n '1,5p' /tmp/ac-run.err
  fi
}

echo "== 1. toolchain / tidy / build / test =="
command -v go >/dev/null || { echo "go missing; run: brew install go"; exit 1; }
go mod tidy
go build ./...
go test ./...
go build -o "$BIN" ./cmd/assetcheck

rm -rf "$FIX" "$IDX"

echo "== 2. generate fixtures (exact documented command) =="
go run ./cmd/assetcheck -generate "$FIX" -report text
check "all fixture files generated" "[[ -s $FIX/good.wav && -s $FIX/truncated.wav && -s $FIX/replaced.wav && -s $FIX/sample.mp4 ]]"

echo "== 3. good sample passes thresholds (exact documented command) =="
go run ./cmd/assetcheck -input "$FIX/good.wav" -index "$IDX" \
  -threshold sample-rate=48000,channels=6,max-gap-frames=1024,max-corrupt-ratio=0.001 \
  | tee /tmp/ac-good.out
DIGEST_GOOD="$(awk '/^summary-digest:/{print $2}' /tmp/ac-good.out)"
check "good corrupt ratio 0" "grep -q 'corrupt-ratio=0.000000' /tmp/ac-good.out"
check "good every threshold PASS" "[[ \$(grep -c '\[PASS\]' /tmp/ac-good.out) -eq 5 ]]"
code_of 0 "good exit code 0" "$BIN" -input "$FIX/good.wav" -index "$IDX" \
  -threshold sample-rate=48000,channels=6,max-gap-frames=1024,max-corrupt-ratio=0.001

echo "== 4. truncated sample locates range and exceeds limit =="
go run ./cmd/assetcheck -input "$FIX/truncated.wav" -index "$IDX" \
  -threshold max-corrupt-ratio=0.001 | tee /tmp/ac-trunc.out
check "truncated non-empty range" "grep -q 'start-frame=95232 end-frame=96000' /tmp/ac-trunc.out"
check "truncated reports E1012/E1014" "grep -q E1012 /tmp/ac-trunc.out && grep -q E1014 /tmp/ac-trunc.out"
check "truncated ratio FAIL" "grep -q '\[FAIL\] max-corrupt-ratio' /tmp/ac-trunc.out"
code_of 2 "business over-limit exit code 2" "$BIN" -input "$FIX/truncated.wav" \
  -index "$IDX" -threshold max-corrupt-ratio=0.001

echo "== 5. same-path content replacement invalidates parse cache =="
go run ./cmd/assetcheck -input "$FIX/replaced.wav" -index "$IDX" -mutate content | tee /tmp/ac-repl.out
check "content invalidates parse-cache" "grep -q 'parse-cache: INVALIDATED' /tmp/ac-repl.out"
check "content reason printed" "grep -q content-fingerprint-mismatch /tmp/ac-repl.out"

echo "== 5b. container / metadata axes =="
"$BIN" -input "$FIX/container.wav" -index "$IDX" -mutate container > /tmp/ac-cont.out
check "container invalidates structure-cache only" \
  "grep -q 'structure-cache: INVALIDATED' /tmp/ac-cont.out && ! grep -q 'parse-cache: INVALIDATED' /tmp/ac-cont.out"
"$BIN" -input "$FIX/metadata.wav" -index "$IDX" -mutate metadata > /tmp/ac-meta.out
check "metadata invalidates time-index" "grep -q 'time-index: INVALIDATED' /tmp/ac-meta.out"

echo "== 6. injected failure then resume =="
go run ./cmd/assetcheck -input "$FIX/good.wav" -index "$IDX" -inject-failure after-index-prepare 2>/tmp/ac-inj.err || true
"$BIN" -input "$FIX/good.wav" -index "$IDX" -inject-failure after-index-prepare > /tmp/ac-inj.out 2>&1
check "injection marker and E5003" "grep -q injected-failure=after-index-prepare /tmp/ac-inj.out && grep -q E5003 /tmp/ac-inj.out"
code_of 4 "transaction failure exit code 4" "$BIN" -input "$FIX/good.wav" -index "$IDX" \
  -inject-failure after-index-prepare
check "staging file left for recovery" "[[ -n \"\$(ls -A $IDX/staging 2>/dev/null)\" ]]"
go run ./cmd/assetcheck -input "$FIX/good.wav" -index "$IDX" -resume | tee /tmp/ac-resume.out
DIGEST_RESUME="$(awk '/^summary-digest:/{print $2}' /tmp/ac-resume.out)"
check "resume rolled back injected txn" "grep -q 'rolled-back=\[tx-' /tmp/ac-resume.out"
check "staging emptied after resume" "[[ -z \"\$(ls -A $IDX/staging 2>/dev/null)\" ]]"
check "resume digest equals uninterrupted digest" "[[ '$DIGEST_RESUME' == '$DIGEST_GOOD' ]]"
code_of 0 "resume exit code 0" "$BIN" -input "$FIX/good.wav" -index "$IDX" -resume

echo "== 7. format error exit code 3 =="
code_of 3 "truncated header exit code 3" "$BIN" -input "$FIX/badheader.wav" -index "$IDX"

echo
echo "================ acceptance summary ================"
green "checks passed: $PASS"
if [[ "$FAIL" -gt 0 ]]; then red "checks failed: $FAIL"; exit 1; fi
green "ALL ACCEPTANCE CHECKS PASSED"
