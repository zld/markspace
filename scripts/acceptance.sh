#!/bin/sh
# End-to-end acceptance for archive-lab. Run from the project root on macOS.
# Exits non-zero when any expectation fails.
set -eu

work="${1:-.tmp/acceptance}"
bin="$work/archive-lab"

rm -rf "$work"
mkdir -p "$work"
go build -o "$bin" ./cmd/archive-lab

"$bin" fixture --dir "$work/cases" >/dev/null

common="--target-bps 800000 --max-bps 900000 --min-psnr 35 --budget-ms 5000"

echo "== normal =="
"$bin" run --input "$work/cases/normal.m3u8" $common > "$work/normal.json"
python3 - "$work/normal.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
assert d["status"] == "ok", d
assert abs(d["bitrate_error_pct"]) <= 5, d["bitrate_error_pct"]
assert d["psnr_db"] is not None and d["psnr_db"] >= 35, d["psnr_db"]
assert d["playlist_drift_ms"] <= 1, d["playlist_drift_ms"]
for key in ("target_bps", "actual_bps", "bitrate_error_pct", "psnr_db",
            "playlist_drift_ms", "input_sha256", "config_sha256"):
    assert key in d, key
print("   ok: bps=%d err=%.3f%% psnr=%.2f drift=%.3fms" % (
    d["actual_bps"], d["bitrate_error_pct"], d["psnr_db"], d["playlist_drift_ms"]))
PY

echo "== damaged inputs =="
for name in truncated.m3u8 malformed.mpd; do
  set +e
  "$bin" run --input "$work/cases/$name" $common > "$work/$name.json" 2> "$work/$name.err"
  code=$?
  set -e
  test "$code" -eq 2 || { echo "FAIL $name exit=$code"; exit 1; }
  python3 - "$work/$name.json" "$name" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
assert d["status"] == "error", d
assert d["failed_stage"] == "parse", d
assert d["index_sha256"] == "", "no index may be committed"
assert d["cache"] == "", "no cache may be written"
assert d["error"], "error must be located"
print("   ok:", d["error"])
PY
done
test ! -e "$work/cases/.archive-lab/index-truncated.json"
test ! -e "$work/cases/.archive-lab/index-malformed.json"

echo "== recovery: normal, replace, metadata, repeat =="
# Cold start: the normal check above already populated this directory's state.
rm -rf "$work/cases/.archive-lab"
check() {
  python3 - "$1" "$2" "$3" <<'PY'
import json, sys
path, want_cache, want_rebuilt = sys.argv[1], sys.argv[2], sys.argv[3] == "true"
d = json.load(open(path))
assert d["cache"] == want_cache, (d["cache"], want_cache)
assert d["index_rebuilt"] is want_rebuilt, (d["index_rebuilt"], want_rebuilt)
PY
}
"$bin" run --input "$work/cases/normal.m3u8" $common > "$work/r1.json"
check "$work/r1.json" miss true
"$bin" swap --from "$work/cases/media/replace" --to "$work/cases/media/normal" >/dev/null
"$bin" run --input "$work/cases/normal.m3u8" $common > "$work/r2.json"
check "$work/r2.json" miss true
"$bin" swap --from "$work/cases/media/metadata" --to "$work/cases/media/normal" >/dev/null
"$bin" run --input "$work/cases/normal.m3u8" $common > "$work/r3.json"
check "$work/r3.json" miss true
"$bin" run --input "$work/cases/normal.m3u8" $common > "$work/r4.json"
check "$work/r4.json" hit false
python3 - "$work/r3.json" "$work/r4.json" <<'PY'
import json, sys
a, b = json.load(open(sys.argv[1])), json.load(open(sys.argv[2]))
assert a["report_sha256"] == b["report_sha256"], "repeat must reproduce the report"
print("   ok: repeat report digest", a["report_sha256"][:16])
PY

echo "== usage =="
set +e
"$bin" run >/dev/null 2>&1
code=$?
set -e
test "$code" -eq 1 || { echo "FAIL usage exit=$code"; exit 1; }
echo "   ok: usage exit 1"

echo "acceptance: PASS"
