#!/usr/bin/env bash
#
# verify.sh — end-to-end acceptance checks for tsaudit.
#
# It builds the tool, regenerates deterministic fixtures, and asserts the
# concrete summary fields, slice round-trip counts, damaged-table behaviour
# and process exit codes required by the project specification. A failing
# assertion prints a diagnostic and exits non-zero; success prints ALL CHECKS
# PASSED and exits 0.
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d -t tsaudit-verify.XXXXXX)"
trap 'rm -rf "$WORK"' EXIT

BIN="$WORK/tsaudit"
SAMPLE="$WORK/sample.ts"
BAD_SECTIONS="$WORK/bad-sections.ts"
BAD_CC="$WORK/bad-cc.ts"
SLICE="$WORK/program1.ts"

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

# assert_eq <description> <actual> <expected>
assert_eq() {
	if [ "$2" != "$3" ]; then
		fail "$1: got '$2', want '$3'"
	fi
	echo "ok: $1 = $2"
}

# json_field <file> <jq-expression>
json_field() {
	jq -r "$2" "$1"
}

command -v jq >/dev/null 2>&1 || fail "jq is required for verify.sh"

cd "$ROOT"

echo "== build and fixtures =="
go mod download || fail "go mod download"
go build -o "$BIN" ./cmd/tsaudit || fail "build"
go run ./testdata/generate.go --scenario normal --out "$SAMPLE" || fail "generate normal"
go run ./testdata/generate.go --scenario bad-sections --out "$BAD_SECTIONS" || fail "generate bad-sections"
go run ./testdata/generate.go --scenario bad-cc --out "$BAD_CC" || fail "generate bad-cc"

FIRST="$WORK/first.json"
"$BIN" --input "$SAMPLE" --format json >"$FIRST"
assert_eq "normal exit code" "$?" "0"

echo "== first summary fields =="
assert_eq "total_packets" "$(json_field "$FIRST" .transport.total_packets)" "120"
assert_eq "pat_version_changes" "$(json_field "$FIRST" .pat_version_changes)" "0"
assert_eq "pmt_version_changes" "$(json_field "$FIRST" .pmt_version_changes)" "1"
assert_eq "mapping records" "$(json_field "$FIRST" '.mappings | length')" "6"
assert_eq "has_complete_pat" "$(json_field "$FIRST" .has_complete_pat)" "true"
assert_eq "corrupt sections" "$(json_field "$FIRST" '.corrupt_sections | length')" "0"

pid_total() { json_field "$1" ".pid_counters[] | select(.pid==$2) | .total_packets"; }
assert_eq "PID 256 packets" "$(pid_total "$FIRST" 256)" "12"
assert_eq "PID 258 packets" "$(pid_total "$FIRST" 258)" "8"
assert_eq "PID 257 packets" "$(pid_total "$FIRST" 257)" "40"
assert_eq "PID 300 packets" "$(pid_total "$FIRST" 300)" "40"

# A remapped PID must appear as two distinct, non-merged version intervals.
INTERVALS_257="$(json_field "$FIRST" '[.mappings[] | select(.elementary_pid==257) | .first_version] | sort | join(",")')"
assert_eq "PID 257 version intervals" "$INTERVALS_257" "0,1"

echo "== slice and round-trip =="
"$BIN" --input "$SAMPLE" --program 1 --slice "$SLICE" >/dev/null
assert_eq "slice exit code" "$?" "0"

RECHECK="$WORK/recheck.json"
"$BIN" --input "$SLICE" --format json >"$RECHECK"
assert_eq "slice re-audit exit code" "$?" "0"
assert_eq "slice PID 256" "$(pid_total "$RECHECK" 256)" "12"
assert_eq "slice PID 258" "$(pid_total "$RECHECK" 258)" "8"
assert_eq "slice PID 257" "$(pid_total "$RECHECK" 257)" "40"
assert_eq "slice PID 300" "$(pid_total "$RECHECK" 300)" "40"
# The slice contains only program 1 PIDs: PAT(0), PMT(4096), and the four ES.
SLICE_PIDS="$(json_field "$RECHECK" '[.pid_counters[].pid] | sort | join(",")')"
assert_eq "slice PID set" "$SLICE_PIDS" "0,256,257,258,300,4096"

echo "== damaged sections never publish =="
BAD="$WORK/bad.json"
"$BIN" --input "$BAD_SECTIONS" --format json >"$BAD"
assert_eq "bad-sections exit code" "$?" "2"
assert_eq "bad-sections PMT changes" "$(json_field "$BAD" .pmt_version_changes)" "0"
assert_eq "bad-sections published PMT versions" \
	"$(json_field "$BAD" '[.published_versions[] | select(.table_id==2) | .version] | join(",")')" "0"
assert_eq "bad-sections corrupt candidates" "$(json_field "$BAD" '.corrupt_sections | length')" "2"
# The still-valid v0 mapping must remain in place.
assert_eq "bad-sections retains PID 256" "$(pid_total "$BAD" 256)" "8"

echo "== continuity anomaly and strict mode =="
CC="$WORK/cc.json"
"$BIN" --input "$BAD_CC" --format json >"$CC"
assert_eq "bad-cc normal exit code" "$?" "0"
assert_eq "PID 257 continuity_gaps" \
	"$(json_field "$CC" '.pid_counters[] | select(.pid==257) | .continuity_gaps')" "2"
# Counter 9 after the jump is tracked as a normal increment, not another gap.
assert_eq "PID 257 gap events" \
	"$(json_field "$CC" '.pid_counters[] | select(.pid==257) | .gap_events')" "1"

"$BIN" --input "$BAD_CC" --strict-continuity >/dev/null 2>&1
assert_eq "strict continuity exit code" "$?" "3"
"$BIN" --input "$SAMPLE" --strict-continuity >/dev/null 2>&1
assert_eq "strict mode clean stream exit code" "$?" "0"

echo "== go test =="
go test ./... >/dev/null || fail "go test ./..."

echo
echo "ALL CHECKS PASSED"
