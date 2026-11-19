#!/usr/bin/env bash
# Reproducible local acceptance for the headless A/V sync kernel.
# Builds, runs the full fault-injection scenario, validates the JSON report,
# and runs the ctest suite. No network, no GUI, no external media files.
set -euo pipefail

cd "$(dirname "$0")/.."

echo "==> configure"
cmake -S . -B build -DCMAKE_BUILD_TYPE=Release >/dev/null

echo "==> build"
cmake --build build --parallel

echo "==> unit tests"
ctest --test-dir build --output-on-failure

REPORT="$(./build/avsync \
  --duration 12 --fps 30000/1001 --sample-rate 48000 \
  --audio-underrun-ms 180 --video-delay-ms 90 \
  --duplicate-every 47 --missing-pts-every 113 --seed 42)"
echo "==> report"
echo "$REPORT"

echo "==> validate report"
python3 -I - "$REPORT" <<'PY'
import json, sys

report = json.loads(sys.argv[1])

def must(cond, msg):
    if not cond:
        print(f"ASSERT FAILED: {msg}", file=sys.stderr)
        sys.exit(4)

# Required top-level fields from the acceptance contract.
for key in ("master_drift_ms", "av_offset_ms", "dropped_frames",
            "repeated_frames", "audio_underruns", "missing_pts"):
    must(key in report, f"missing field {key}")

for stat in ("master_drift_ms", "av_offset_ms"):
    s = report[stat]
    for q in ("min", "max", "mean", "p50", "p95", "p99"):
        must(q in s, f"{stat}.{q} absent")
    # Non-empty sample set must carry real (non-null) millisecond values.
    must(s["count"] > 0 and s["min"] is not None, f"{stat} has no samples")

# 12s sample: ~360 video frames, ~576000 audio samples (tolerate jitter).
must(340 <= report["video_frames_decoded"] <= 400,
     "decoded video frame count outside ~360")
must(570000 <= report["audio_samples_decoded"] <= 582000,
     "decoded audio sample count outside ~576000")

# Counters are non-negative integers and injections match the parameters.
for key in ("dropped_frames", "repeated_frames", "audio_underruns",
            "missing_pts", "video_frames_decoded",
            "audio_samples_decoded"):
    must(isinstance(report[key], int) and report[key] >= 0,
         f"{key} must be a non-negative integer")

must(report["audio_underruns"] >= 1, "injected audio underrun not counted")
must(report["injected_audio_underruns"] == 1, "underrun injection mismatch")
must(report["injected_missing_pts"] == report["missing_pts"],
     "missing-pts injection vs observed mismatch")
must(report["injected_duplicates"] > 0, "duplicate injection not observed")
must(report["av_offset_ms"]["max"] < 100.0,
     "a/v offset exceeded the sync budget")

print("report validation: OK")
PY

echo "==> argument error path (expect exit 2)"
if ./build/avsync --bogus >/dev/null 2>&1; then
  echo "ASSERT FAILED: invalid argument did not exit 2" >&2
  exit 4
fi

echo "ALL CHECKS PASSED"
