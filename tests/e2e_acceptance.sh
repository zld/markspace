#!/usr/bin/env bash
# End-to-end acceptance against the built binary. Runs in a scratch directory
# so fixtures never pollute the source tree.
set -euo pipefail

BIN="$1"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/lutcheck_e2e.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
cd "$WORK"

"$BIN" demo --generate-fixtures --size 17 --space rec709 \
  --compare scalar,parallel --histogram-bins 16 --json > report.json

python3 -I - report.json <<'PY'
import json, sys
report = json.load(open(sys.argv[1], encoding="utf-8"))
assert report["grid_size"] == 17, report["grid_size"]
dev = report["max_deviation_rgb"]
assert set(dev) == {"r", "g", "b"}
assert max(abs(v) for v in dev.values()) <= 1e-6, dev
for key in ("histogram_before", "histogram_after"):
    hist = report[key]
    assert hist["bins"] == 16
    for channel in ("r", "g", "b"):
        assert len(hist[channel]) == 16
        assert sum(hist[channel]) > 0
assert 10_000 <= report["pixels"] < 100_000, report["pixels"]
PY

# Determinism: a second run must produce byte-identical fixtures.
"$BIN" demo --generate-fixtures --size 17 --space rec709 --compare scalar \
  --histogram-bins 16 --json > /dev/null
cp fixtures/grade.cube first.cube
"$BIN" demo --generate-fixtures --size 17 --space rec709 --compare scalar \
  --histogram-bins 16 --json > /dev/null
cmp -s first.cube fixtures/grade.cube

# Exit codes: 2 usage, 3 input, 0 success.
set +e
"$BIN" demo --size 1 >/dev/null 2>&1
[[ $? -eq 2 ]]
printf 'P6\n2 2\n255\n\x01\x02' > truncated.ppm
"$BIN" render --input truncated.ppm --lut fixtures/grade.cube \
  --compare scalar >/dev/null 2>&1
[[ $? -eq 3 ]]
"$BIN" parse --lut fixtures/grade.cube >/dev/null
[[ $? -eq 0 ]]
set -e

# Batch mode writes one output per input and keeps order.
"$BIN" batch --inputs fixtures/sample.ppm,fixtures/sample.ppm \
  --out-dir out --lut fixtures/grade.cube --input-space rec709 --lut-space rec709 \
  --compare scalar,parallel --json > batch.jsonl
[[ "$(wc -l < batch.jsonl | tr -d ' ')" -eq 2 ]]
[[ -f out/render_0.ppm && -f out/render_1.ppm ]]

echo "e2e: OK"
