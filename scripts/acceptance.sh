#!/bin/sh
# Acceptance run for tsinspect. Builds, tests, generates local fixtures, and
# asserts report fields and quantities rather than trusting the exit status.
#
# usage: scripts/acceptance.sh [work-directory]
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
WORK=${1:-$(mktemp -d "${TMPDIR:-/tmp}/tsinspect-accept.XXXXXX")}
mkdir -p "$WORK"

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok: $*"; }

require_json() {
	python3 - "$@" <<'PY'
import json, sys
path = sys.argv[1]
expr = sys.argv[2]
with open(path) as fh:
    doc = json.load(fh)
ns = {"doc": doc, "len": len, "abs": abs, "sum": sum, "all": all, "any": any, "min": min, "max": max, "set": set, "sorted": sorted}
if not eval(expr, {"__builtins__": ns}, ns):
    print("assertion failed: " + expr, file=sys.stderr)
    sys.exit(1)
PY
}

echo "== workspace: $WORK"
cd "$ROOT"

echo "== go mod download"
go mod download
pass "dependencies present"

echo "== go build"
go build -o "$WORK/tsinspect" ./cmd/tsinspect
pass "binary built"

echo "== go test"
go test ./...
pass "unit and integration tests"

echo "== generate fixtures"
go run ./testdata/generate.go \
	-out "$WORK/sample.ts" \
	-corrupt-out "$WORK/corrupt.ts" \
	-midstream-out "$WORK/midstream.ts" \
	-truncate-out "$WORK/truncated.ts"
for f in sample corrupt midstream truncated; do
	[ -s "$WORK/$f.ts" ] || fail "missing fixture $f.ts"
done
pass "fixtures generated locally"

echo "== primary acceptance command"
set +e
"$WORK/tsinspect" -json -program 101 -out "$WORK/program-101.ts" "$WORK/sample.ts" >"$WORK/sample.json" 2>"$WORK/sample.err"
rc=$?
set -e
[ "$rc" -eq 0 ] || fail "primary command exited $rc: $(cat "$WORK/sample.err")"
pass "exit code 0"

require_json "$WORK/sample.json" 'doc["pat_version_changes"] == 1' || fail "pat_version_changes"
require_json "$WORK/sample.json" 'doc["pmt_version_changes"] == 1' || fail "pmt_version_changes"
require_json "$WORK/sample.json" '3 <= len(doc["pid_mappings"]) <= 6' || fail "pid_mappings count"
require_json "$WORK/sample.json" 'len(doc["elementary_streams"]) == 5' || fail "elementary stream count"
require_json "$WORK/sample.json" 'doc["packets_processed"] >= 300' || fail "packet total"
require_json "$WORK/sample.json" 'all(set(("program_number","pid","stream_type","packet_count","discontinuity_seen")) <= set(row) for row in doc["elementary_streams"])' || fail "elementary stream fields"
require_json "$WORK/sample.json" 'all(10 <= row["packet_count"] <= 400 for row in doc["elementary_streams"])' || fail "per-stream packet counts"
require_json "$WORK/sample.json" 'doc["anomalies"]["duplicates"] >= 1' || fail "duplicate detection"
require_json "$WORK/sample.json" 'doc["anomalies"]["counter_jumps"] >= 1' || fail "jump detection"
require_json "$WORK/sample.json" 'doc["anomalies"]["transport_errors"] >= 1' || fail "transport error detection"
require_json "$WORK/sample.json" 'doc["anomalies"]["af_only_packets"] >= 1' || fail "af-only detection"
require_json "$WORK/sample.json" 'any(row["discontinuity_seen"] for row in doc["elementary_streams"])' || fail "discontinuity flag"
require_json "$WORK/sample.json" 'any(event["kind"]=="pmt" and event["status"]=="removed" for event in doc["table_events"])' || fail "program removal event"
require_json "$WORK/sample.json" 'doc["slice"]["version_boundary_count"] == 2' || fail "slice boundaries"
require_json "$WORK/sample.json" '[b["version"] for b in doc["slice"]["version_boundaries"]] == [0,1]' || fail "boundary versions"
require_json "$WORK/sample.json" 'doc["slice"]["packets_written"] > 0' || fail "slice size"
[ "$(( $(wc -c <"$WORK/program-101.ts") % 188 ))" -eq 0 ] || fail "slice not packet aligned"
pass "JSON fields and quantities"

echo "== standard output summary"
"$WORK/tsinspect" "$WORK/sample.ts" >"$WORK/sample.txt"
grep -q "packets processed:" "$WORK/sample.txt" || fail "stdout packet total"
grep -q "anomalies:" "$WORK/sample.txt" || fail "stdout anomalies"
pass "human-readable summary"

echo "== slice contains only program 101"
"$WORK/tsinspect" -program 101 -out "$WORK/p101.ts" "$WORK/sample.ts" >/dev/null
python3 - "$WORK/p101.ts" <<'PY'
import sys
data = open(sys.argv[1], "rb").read()
assert len(data) % 188 == 0 and data, "slice not packet aligned"
pids = set()
for i in range(0, len(data), 188):
    pids.add(((data[i+1] & 0x1f) << 8) | data[i+2])
assert pids <= {0x100, 0x101, 0x102, 0x1000}, "foreign PID in slice: %s" % sorted(pids)
PY
pass "slice PID filter"

echo "== determinism"
"$WORK/tsinspect" -json -program 101 -out "$WORK/p101-b.ts" "$WORK/sample.ts" >"$WORK/sample-b.json"
cmp -s "$WORK/sample.json" "$WORK/sample-b.json" || fail "JSON differs between identical runs"
cmp -s "$WORK/program-101.ts" "$WORK/p101-b.ts" || fail "slice output differs between identical runs"
pass "byte-identical repeat run"

echo "== stdin input"
"$WORK/tsinspect" -json - <"$WORK/sample.ts" >"$WORK/stdin.json"
require_json "$WORK/stdin.json" 'doc["input"] == "<stdin>" and doc["packets_processed"] == doc["bytes_processed"] // 188' || fail "stdin summary"
pass "stdin input"

echo "== corrupt input exits 2 but still reports and slices"
set +e
"$WORK/tsinspect" -json -program 101 -out "$WORK/corrupt-slice.ts" "$WORK/corrupt.ts" >"$WORK/corrupt.json" 2>"$WORK/corrupt.err"
rc=$?
set -e
[ "$rc" -eq 2 ] || fail "corrupt fixture exited $rc, want 2"
grep -q "CRC" "$WORK/corrupt.err" || fail "CRC reason not reported on stderr"
[ -s "$WORK/corrupt-slice.ts" ] || fail "valid tables did not produce a slice"
require_json "$WORK/corrupt.json" 'doc["anomalies"]["crc_errors"] == 2' || fail "CRC accounting"
require_json "$WORK/corrupt.json" 'doc["anomalies"]["orphan_pmts"] == 1' || fail "orphan PMT accounting"
require_json "$WORK/corrupt.json" 'doc["pat_version_changes"] == 1 and doc["pmt_version_changes"] == 1' || fail "corrupt sections changed versions"
require_json "$WORK/corrupt.json" 'len(doc["programs"]) == 2' || fail "corrupt PAT deleted a program"
require_json "$WORK/corrupt.json" 'all(row["version"] >= 0 for row in doc["programs"])' || fail "program rows"
pass "CRC and orphan reporting"

echo "== previous output survives a failed write"
printf 'previous-output' >"$WORK/preserve.ts"
set +e
"$WORK/tsinspect" -program 101 -out "$WORK/preserve.ts" "$WORK/truncated.ts" >/dev/null 2>&1
rc=$?
set -e
[ "$rc" -eq 2 ] || fail "truncated input exit code $rc, want 2"
[ "$(cat "$WORK/preserve.ts")" = "previous-output" ] || fail "failed run replaced the previous output"
pass "atomic output preserved"

echo "== partial tail is rejected"
set +e
"$WORK/tsinspect" -json "$WORK/truncated.ts" >"$WORK/truncated.json" 2>"$WORK/truncated.err"
rc=$?
set -e
[ "$rc" -eq 2 ] || fail "partial tail exit code $rc, want 2"
grep -q "trailing partial packet: 7 bytes" "$WORK/truncated.err" || fail "tail byte count not reported"
pass "partial tail reporting"

echo "== argument errors"
set +e
"$WORK/tsinspect" -program 101 -out "$WORK/x.ts" -max-buffer-packets 0 "$WORK/sample.ts" >/dev/null 2>&1
rc1=$?
"$WORK/tsinspect" --nonsense "$WORK/sample.ts" >/dev/null 2>&1
rc2=$?
"$WORK/tsinspect" -out "$WORK/x.ts" "$WORK/sample.ts" >/dev/null 2>&1
rc3=$?
set -e
[ "$rc1" -eq 2 ] && [ "$rc2" -eq 2 ] && [ "$rc3" -eq 2 ] || fail "argument exit codes $rc1/$rc2/$rc3"
pass "argument errors exit 2"

echo "== write failure exits 3 and preserves the old file"
mkdir -p "$WORK/readonly"
printf 'keep-me' >"$WORK/readonly/out.ts"
chmod 500 "$WORK/readonly"
set +e
"$WORK/tsinspect" -program 101 -out "$WORK/readonly/out.ts" "$WORK/sample.ts" >/dev/null 2>"$WORK/write.err"
rc=$?
set -e
chmod 700 "$WORK/readonly"
if [ "$rc" -ne 3 ]; then
	echo "note: write failure produced exit $rc (filesystem permissions may not deny writes here)"
	[ "$rc" -eq 0 ] || fail "unexpected exit $rc"
else
	[ "$(cat "$WORK/readonly/out.ts")" = "keep-me" ] || fail "failed write replaced the previous output"
	pass "write failure exits 3"
fi

echo "== mid-stream start buffers until tables arrive"
"$WORK/tsinspect" -json -program 101 -out "$WORK/mid.ts.out" "$WORK/midstream.ts" >"$WORK/midstream.json"
require_json "$WORK/midstream.json" 'doc["slice"]["buffered_flushed"] > 0' || fail "pre-table cache unused"
require_json "$WORK/midstream.json" 'doc["slice"]["packets_written"] > 0' || fail "mid-stream slice empty"
pass "mid-stream cache"

echo
echo "all acceptance checks passed; artefacts in $WORK"
