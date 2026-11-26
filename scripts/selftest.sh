#!/usr/bin/env bash
# selftest.sh - reproducible local acceptance for replaykit.
#
# Installs/pins the Go wrapper if needed, then runs the exact acceptance
# pipeline: build, full tests and the `selftest --case all` proof run. It also
# builds a binary and checks the faithful 0/1/2 exit statuses (note:
# `go run` collapses a child's non-zero exit to 1, so status 2 is asserted on
# the compiled binary). Purely local; no services. macOS arm64/amd64.
set -euo pipefail

cd "$(dirname "$0")/.."

OUT="${OUT:-/tmp/replay-proof}"

# Resolve the pinned go1.23.12 wrapper, honouring an existing PATH first.
if ! command -v go1.23.12 >/dev/null 2>&1; then
  for cand in "$HOME/go/bin/go1.23.12" "$HOME/.local/bin/go1.23.12" \
              "/opt/homebrew/bin/go1.23.12" "/usr/local/bin/go1.23.12"; do
    if [ -x "$cand" ]; then export PATH="$(dirname "$cand"):$PATH"; break; fi
  done
fi
if ! command -v go1.23.12 >/dev/null 2>&1; then
  echo "go1.23.12 not found. Install it with:" >&2
  echo "  brew install go && go install golang.org/dl/go1.23.12@latest && go1.23.12 download" >&2
  exit 127
fi

echo "== toolchain =="
go1.23.12 version

echo "== mod tidy / download =="
go1.23.12 mod tidy
go1.23.12 mod download

echo "== build =="
go1.23.12 build ./...

echo "== test =="
go1.23.12 test ./...

echo "== selftest --case all =="
rm -rf "$OUT"
go1.23.12 run ./cmd/replay selftest --case all --out "$OUT"

# Faithful exit-status contract via a compiled binary.
BIN="$(mktemp -d)/replay"
go1.23.12 build -o "$BIN" ./cmd/replay
set +e
"$BIN" selftest --case normal       --out "$OUT/status-normal"       >/dev/null 2>&1; c0=$?
"$BIN" selftest --case fatal        --out "$OUT/status-fatal"        >/dev/null 2>&1; c1=$?
"$BIN" selftest --case unrecoverable --out "$OUT/status-unrecoverable" >/dev/null 2>&1; c2=$?
set -e
[ "$c0" -eq 0 ] || { echo "normal exit=$c0 want 0"; exit 1; }
[ "$c1" -eq 1 ] || { echo "fatal exit=$c1 want 1"; exit 1; }
[ "$c2" -eq 2 ] || { echo "unrecoverable exit=$c2 want 2"; exit 1; }
echo "exit status: normal=0 fatal=1 unrecoverable=2"

echo
echo "ALL ACCEPTANCE CHECKS PASSED (proof: $OUT/proof.json)"
