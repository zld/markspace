#!/usr/bin/env python3
"""Validate a pw_sync JSON report for the fields required by build regression.

Usage: validate_report.py REPORT_JSON [--deterministic-with SECOND_JSON]
Exits 0 on success, 1 on any contract violation. Pure stdlib, no network.
"""
import argparse
import json
import sys

REQUIRED_TOP = [
    "input_file", "clock_mode", "late_threshold_ms",
    "discontinuity_tolerance_ms", "media_duration_seconds",
    "master_drift_ms", "path_a_av_offset_ms", "path_b_av_offset_ms",
    "path_a_dropped_frames", "path_a_repeated_frames",
    "path_b_dropped_frames", "path_b_repeated_frames",
    "timestamp_repairs", "discontinuities", "corrupted_packets",
    "decode_failures", "flushed_frames", "errors",
]
REQUIRED_DRIFT = [
    "mode", "samples", "resets", "cumulative_drift_ms",
    "mean_drift_ms", "max_abs_drift_ms",
]
REQUIRED_OFFSET = [
    "samples", "p50_ms", "p95_ms", "max_abs_ms", "mean_ms",
    "histogram_edges_ms", "histogram_counts",
]
NONNEGATIVE_INT = [
    "path_a_dropped_frames", "path_a_repeated_frames",
    "path_b_dropped_frames", "path_b_repeated_frames",
    "timestamp_repairs", "discontinuities", "corrupted_packets",
    "decode_failures", "flushed_frames",
]
EXPECTED_EDGES = [-80.0, -40.0, -20.0, -10.0, 0.0, 10.0, 20.0, 40.0, 80.0]


def validate(report):
    for key in REQUIRED_TOP:
        if key not in report:
            raise AssertionError("missing top-level field: " + key)
    for key in REQUIRED_DRIFT:
        if key not in report["master_drift_ms"]:
            raise AssertionError("missing master_drift_ms." + key)
    for path in ("path_a_av_offset_ms", "path_b_av_offset_ms"):
        node = report[path]
        for key in REQUIRED_OFFSET:
            if key not in node:
                raise AssertionError("missing %s.%s" % (path, key))
        if node["histogram_edges_ms"] != EXPECTED_EDGES:
            raise AssertionError(path + " histogram edges are not the fixed contract")
        if len(node["histogram_counts"]) != len(EXPECTED_EDGES) + 1:
            raise AssertionError(path + " histogram must have 10 bins")
        if node["p50_ms"] > node["p95_ms"]:
            raise AssertionError(path + " P50 must be <= P95")
    for key in NONNEGATIVE_INT:
        value = report[key]
        if not isinstance(value, int) or value < 0:
            raise AssertionError(key + " must be a non-negative integer")
    if not isinstance(report["errors"], list):
        raise AssertionError("errors must be a list")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("report")
    parser.add_argument("--deterministic-with", default=None)
    args = parser.parse_args()
    with open(args.report, "r", encoding="utf-8") as handle:
        first = json.load(handle)
    validate(first)
    if args.deterministic_with:
        with open(args.deterministic_with, "r", encoding="utf-8") as handle:
            second = json.load(handle)
        validate(second)
        # Deterministic contract: frame counts and statistical distributions
        # must be identical across two runs of the same fixture.
        for path in ("path_a_av_offset_ms", "path_b_av_offset_ms"):
            for key in ("histogram_counts", "samples", "p50_ms", "p95_ms",
                        "max_abs_ms"):
                if first[path][key] != second[path][key]:
                    raise AssertionError(
                        "non-deterministic %s.%s between runs" % (path, key))
        for key in ("timestamp_repairs", "discontinuities",
                    "video_frames_decoded", "audio_frames_decoded",
                    "flushed_frames"):
            if first.get(key) != second.get(key):
                raise AssertionError("non-deterministic " + key)
        for path in ("path_a", "path_b"):
            for key in ("dropped_frames", "repeated_frames"):
                full = path + "_" + key
                if first[full] != second[full]:
                    raise AssertionError("non-deterministic " + full)
    print("report validation OK:", args.report)


if __name__ == "__main__":
    try:
        main()
    except (AssertionError, ValueError, KeyError, OSError) as exc:
        print("VALIDATION FAILED:", exc, file=sys.stderr)
        sys.exit(1)
