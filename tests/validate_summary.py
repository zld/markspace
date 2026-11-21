#!/usr/bin/env python3
"""Validate subtitle-metrics JSONL summaries.

    python3 tests/validate_summary.py build/summary.jsonl

Exits 0 when every record is well formed, 1 otherwise. The checks are the
same invariants the acceptance run relies on, so this doubles as the review
script mentioned in the brief.
"""
import json
import sys

REQUIRED = {"id", "language", "lines", "bbox", "line_breaks", "line_ranges",
            "break_reasons", "overflow_count", "overflow_reasons",
            "missing_glyphs", "errors"}
BREAK_REASONS = {"space", "punctuation", "fallback", "explicit", "end"}
OVERFLOW_REASONS = {"", "cluster_too_wide", "max_lines"}
KNOWN_OVERFLOW = {"cluster_too_wide", "max_lines"}


def fail(problems, index, message):
    problems.append("record {}: {}".format(index, message))


def validate(path):
    problems = []
    seen = set()
    with open(path, encoding="utf-8") as handle:
        for index, line in enumerate(handle):
            if not line.strip():
                continue
            try:
                record = json.loads(line)
            except json.JSONDecodeError as exc:
                fail(problems, index, "invalid JSON: {}".format(exc))
                continue
            missing = REQUIRED - set(record)
            if missing:
                fail(problems, index, "missing fields {}".format(sorted(missing)))
                continue
            if record["id"] in seen:
                fail(problems, index, "duplicate id {}".format(record["id"]))
            seen.add(record["id"])
            lines = record["lines"]
            if not isinstance(lines, int) or lines < 0:
                fail(problems, index, "lines must be a non-negative integer")
                continue
            if len(record["line_ranges"]) != lines:
                fail(problems, index, "line_ranges does not match lines")
            if len(record["break_reasons"]) != lines:
                fail(problems, index, "break_reasons does not match lines")
            if len(record["line_breaks"]) != max(lines - 1, 0):
                fail(problems, index, "line_breaks does not match lines")
            for reason in record["break_reasons"]:
                if reason not in BREAK_REASONS:
                    fail(problems, index, "unknown break reason {}".format(reason))
            for reason in record["overflow_reasons"]:
                if reason not in OVERFLOW_REASONS:
                    fail(problems, index,
                         "unknown overflow reason {}".format(reason))
            flagged = sum(1 for r in record["overflow_reasons"] if r)
            if flagged < record["overflow_count"]:
                fail(problems, index,
                     "overflow_count exceeds per-line overflow reasons")
            bbox = record["bbox"]
            for key in ("x", "y", "width", "height"):
                if not isinstance(bbox.get(key), int):
                    fail(problems, index, "bbox.{} must be an integer".format(key))
            if bbox.get("width", 0) < 0 or bbox.get("height", 0) < 0:
                fail(problems, index, "bbox dimensions must be non-negative")
            previous = -1
            for start in record["line_breaks"]:
                if not isinstance(start, int) or start < 0:
                    fail(problems, index, "line_breaks must be offsets")
                elif start <= previous:
                    fail(problems, index, "line_breaks must increase")
                previous = start
            for index_range, item in enumerate(record["line_ranges"]):
                if item["end"] < item["begin"]:
                    fail(problems, index,
                         "line range {} ends before it begins".format(index_range))
            if record["missing_glyphs"] < 0:
                fail(problems, index, "missing_glyphs must be non-negative")
    return problems


def main(argv):
    if len(argv) != 2:
        print("usage: validate_summary.py SUMMARY.jsonl", file=sys.stderr)
        return 2
    problems = validate(argv[1])
    for problem in problems:
        print(problem, file=sys.stderr)
    print("records_ok={}".format("yes" if not problems else "no"))
    return 0 if not problems else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv))
