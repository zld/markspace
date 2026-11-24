#!/usr/bin/env bash
# Acceptance checks for the archivist media repair kernel.
#
# Runs the documented build/test commands, then exercises the demo and the
# per-file exit-code contract. Every assertion prints PASS or FAIL and the
# script exits non-zero if any check fails.
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 1
WORK="${TMPDIR:-/tmp}/archivist-acceptance-$$"
BIN="$WORK/archivist"
FAILED=0

pass() { printf 'PASS  %s\n' "$1"; }
fail() { printf 'FAIL  %s\n' "$1"; FAILED=1; }

cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

mkdir -p "$WORK"

echo "== go build ./..."
if go build ./...; then pass "go build"; else fail "go build"; exit 1; fi

echo "== go vet ./..."
if go vet ./...; then pass "go vet"; else fail "go vet"; fi

echo "== go test ./..."
if go test ./...; then pass "go test"; else fail "go test"; fi

echo "== go build -o archivist ./cmd/archivist"
if go build -o "$BIN" ./cmd/archivist; then pass "cli build"; else fail "cli build"; exit 1; fi

DEMO_DIR="$WORK/demo"
echo "== archivist demo --dir $DEMO_DIR --queue 4"
if "$BIN" demo --dir "$DEMO_DIR" --queue 4 >"$WORK/demo.json" 2>"$WORK/demo.err"; then
  pass "demo exit 0"
else
  fail "demo exit 0 (see $WORK/demo.err)"
  sed -n '1,20p' "$WORK/demo.err"
fi

python3 - "$WORK/demo.json" <<'PY'
import json, sys
path = sys.argv[1]
try:
    d = json.load(open(path))
except Exception as exc:
    print("FAIL  demo report is valid JSON (%s)" % exc)
    sys.exit(1)

fails = []
def check(cond, msg):
    print(("PASS  " if cond else "FAIL  ") + msg)
    if not cond:
        fails.append(msg)

by_name = {}
for r in d["results"]:
    by_name.setdefault(r["name"], []).append(r)

normal = by_name.get("normal", [])
check(bool(normal), "demo includes a normal sample")
if normal:
    n = normal[0]
    check(n["status"] == "ok", "normal sample status=ok")
    check(n["exit_code"] == 0, "normal sample exit_code=0")
    check(n["index_entries"] == 90, "normal sample index_entries=90 (got %d)" % n["index_entries"])
    check(bool(n.get("output_sha256")), "normal sample has output_sha256")
    check(bool(n.get("provenance_sha256")), "normal sample has provenance_sha256")

for kind in ("truncated", "truncated_header", "bad_length"):
    rows = by_name.get(kind, [])
    check(bool(rows), "demo includes %s sample" % kind)
    if rows:
        r = rows[0]
        check(r["status"] != "ok", "%s sample is not ok" % kind)
        check(r["exit_code"] == 2, "%s exit_code=2" % kind)
        check(r["error_offset"] >= 0, "%s reports a byte offset" % kind)
        check(not r.get("output_sha256"), "%s produced no output digest" % kind)

for kind in ("wrap", "regression", "wrap_regression"):
    rows = by_name.get(kind, [])
    if rows:
        r = rows[0]
        check(r["status"] == "ok", "%s sample status=ok" % kind)
        check(bool(r.get("repaired_pts")), "%s recorded repaired timestamps" % kind)

qs = d.get("queries") or []
check(len(qs) > 0, "demo includes random-access queries")
if qs:
    worst = max(q["delta_ticks"] for q in qs)
    tol = min(q["tolerance_ticks"] for q in qs)
    check(all(q["ok"] for q in qs), "every query resolves within one sample tick (worst %d, tolerance %d)" % (worst, tol))

check(d["resume_run_ms"] <= d["resume_budget_ms"],
      "resume %dms <= 80%% of first run %dms (%dms)" % (d["resume_run_ms"], d["first_run_ms"], d["resume_budget_ms"]))
check(d["interrupted_at"] != "", "interrupted run recorded a checkpoint stage")

# Output and provenance digests must agree across the first, resumed and
# repeated runs for every healthy input.
def digest_map(runs):
    out = {}
    for r in runs:
        out.setdefault(r["name"], []).append(r)
    return out

groups = {}
for r in d["results"]:
    if r["status"] != "ok":
        continue
    groups.setdefault(r["name"], []).append(r)
for name, rows in groups.items():
    outs = {r["output_sha256"] for r in rows}
    provs = {r["provenance_sha256"] for r in rows}
    check(len(outs) == 1, "%s output_sha256 stable across runs" % name)
    check(len(provs) == 1, "%s provenance_sha256 stable across runs" % name)

check(d.get("failure") in ("", None), "demo reported no failure")

sys.exit(1 if fails else 0)
PY
if [ $? -ne 0 ]; then fail "demo assertions"; fi

echo "== per-file exit codes"
for kind in normal regression wrap start_offset duplicate wrap_regression; do
  "$BIN" check --out "$WORK/check-$kind" "$DEMO_DIR/inputs/$kind.mp4" >/dev/null 2>&1
  rc=$?
  if [ "$rc" -eq 0 ]; then pass "check $kind exit 0"; else fail "check $kind exit 0 (got $rc)"; fi
done

for kind in truncated truncated_header bad_length; do
  "$BIN" check --out "$WORK/check-$kind" "$DEMO_DIR/inputs/$kind.mp4" >"$WORK/$kind.json" 2>/dev/null
  rc=$?
  if [ "$rc" -eq 2 ]; then pass "check $kind exit 2"; else fail "check $kind exit 2 (got $rc)"; fi
  want=$(python3 -c "
import json
m = json.load(open('$DEMO_DIR/inputs/manifest.json'))
print(next(s['corrupt_at'] for s in m['samples'] if s['kind'] == '$kind'))
" 2>/dev/null || echo "")
  if [ -n "$want" ]; then
    got=$(python3 -c "import json;print(json.load(open('$WORK/$kind.json'))['error_offset'])" 2>/dev/null || echo "-1")
    if [ "$got" = "$want" ]; then
      pass "check $kind error_offset=$want"
    else
      fail "check $kind error_offset=$got want $want"
    fi
  fi
done

echo
if [ "$FAILED" -ne 0 ]; then
  echo "RESULT: FAILED"
  exit 1
fi
echo "RESULT: PASSED"
