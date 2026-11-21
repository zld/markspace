#!/usr/bin/env bash
# End-to-end verification for the loopback relay.
#
# Runs the test suite, builds the binary and exercises the acceptance demo,
# asserting the counters that the report contract promises. Exits non-zero if
# any expectation fails.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

BIN="${TMPDIR:-/tmp}/looprelay-verify.$$"
trap 'rm -f "$BIN" "$JSON_FILE" 2>/dev/null || true' EXIT

section() { printf '\n== %s ==\n' "$1"; }

section "toolchain"
go version

section "dependencies"
go mod download

section "tests"
go test ./... -count=1

section "build"
go build -o "$BIN" ./cmd/looprelay

section "acceptance demo"
JSON_FILE="$(mktemp "${TMPDIR:-/tmp}/looprelay.XXXXXX.json")"
set +e
"$BIN" demo \
  --listen 127.0.0.1:0 \
  --packets 240 \
  --interval 20ms \
  --drop-every 37 \
  --delay-every 29 \
  --delay 7ms \
  --json >"$JSON_FILE"
code=$?
set -e

if [ "$code" -ne 0 ]; then
  echo "demo exited with $code" >&2
  cat "$JSON_FILE" >&2
  exit 1
fi

cat "$JSON_FILE"

section "assertions"
python3 - "$JSON_FILE" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as fh:
    r = json.load(fh)

failures = []

def check(name, got, want):
    if got != want:
        failures.append(f"{name}: got {got!r}, want {want!r}")

check("status", r.get("status"), "ok")
check("candidate_pair_count", r.get("candidate_pair_count"), 2)
check("generated_packets", r.get("generated_packets"), 240)
check("skipped_packets", r.get("skipped_packets"), 240 // 37)
check("forwarded_packets", r.get("forwarded_packets"), 240 - 240 // 37)
check("received_packets", r.get("received_packets"), 240 - 240 // 37)
check("lost_packets", r.get("lost_packets"), 0)

for link in r.get("links", []):
    for side in ("local_address", "remote_address"):
        if link.get(side) not in ("127.0.0.1", "::1"):
            failures.append(f"link {link.get('name')} {side} is not loopback: {link.get(side)!r}")

jitter = r.get("jitter_ms", {})
check("jitter_ms.status", jitter.get("status"), "ok")
for key in ("mean", "p95", "max"):
    value = jitter.get(key)
    if not isinstance(value, (int, float)):
        failures.append(f"jitter_ms.{key} missing or non-numeric: {value!r}")
    elif not (0.0 <= float(value) <= 1000.0):
        failures.append(f"jitter_ms.{key} out of plausible range: {value!r}")
p95 = float(jitter.get("p95", 0.0) or 0.0)
if not (0.1 <= p95 <= 10.0):
    failures.append(f"jitter_ms.p95 {p95} is outside the expected 10^0..10^1 ms band")

failed_checks = [c["name"] for c in r.get("checks", []) if not c.get("ok")]
if failed_checks:
    failures.append(f"failed checks: {failed_checks}")

if failures:
    print("FAILED")
    for f in failures:
        print(" -", f)
    sys.exit(1)
print("all assertions passed")
PY

section "exit codes"
expect_code() {
  local want="$1"; shift
  set +e
  "$BIN" "$@" >/dev/null 2>&1
  local got=$?
  set -e
  if [ "$got" -ne "$want" ]; then
    echo "expected exit $want for '$*', got $got" >&2
    exit 1
  fi
  echo "ok: '$*' -> $want"
}

expect_code 2 demo --listen 0.0.0.0:0
expect_code 2 demo --packets 0
expect_code 2 demo --drop-every 1

section "result"
echo "verification passed"
