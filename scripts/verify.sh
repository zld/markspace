#!/bin/sh
# verify.sh - build relaydiag and run the acceptance checks on macOS.
#
# Checks:
#   1. dependencies download, vet passes, binary builds
#   2. `run --packets 1200 --interval 5ms --timeout 20s --json` exits 0 and
#      prints a JSON summary with all contract fields
#   3. `run --packets 0` exits 2 (parameter error)
set -u
cd "$(dirname "$0")/.."

fail() { echo "FAIL: $*" >&2; exit 1; }

echo "== go mod download =="
go mod download || fail "go mod download"

echo "== go vet =="
go vet ./... || fail "go vet"

echo "== go build =="
go build -o /tmp/relaydiag ./cmd/relaydiag || fail "go build"

echo "== acceptance run =="
out=$(/tmp/relaydiag run --packets 1200 --interval 5ms --timeout 20s --json)
rc=$?
echo "$out"
[ "$rc" -eq 0 ] || fail "acceptance run exited $rc, want 0"

for f in candidate_pairs selected_pair_state forwarded_packets lost_packets \
	duplicate_packets reordered_packets jitter_ms duration_ms; do
	case "$out" in
	*"\"$f\""*) ;;
	*) fail "summary missing field $f" ;;
	esac
done

echo "== parameter error exit code =="
/tmp/relaydiag run --packets 0 >/dev/null 2>&1
rc=$?
[ "$rc" -eq 2 ] || fail "--packets 0 exited $rc, want 2"

echo "PASS"
