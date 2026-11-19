#!/usr/bin/env python3
"""Validate the mediakey demo JSON report.

Reads the report on stdin (or from a path argument) and asserts:
  - report_version is present
  - rotation_count is in the 1..5 magnitude band for the standard demo
  - exactly the requested number of chunk results, sorted by index
  - each result carries chunk_index, key_id, status, failure_reason
  - the injected fault categories all appear at least once
  - partial failures still leave a complete per-chunk report
Exits non-zero with a diagnostic on the first violated assertion.
"""
import json
import sys

EXPECTED_CHUNKS = 12
EXPECTED_ROTATION_MIN, EXPECTED_ROTATION_MAX = 1, 5
REQUIRED_REASONS = {"expired", "replay", "auth_failed"}


def main() -> int:
    stream = open(sys.argv[1], encoding="utf-8") if len(sys.argv) > 1 else sys.stdin
    with stream:
        report = json.load(stream)

    assert "report_version" in report, "missing report_version"
    rotations = report["rotation_count"]
    assert EXPECTED_ROTATION_MIN <= rotations <= EXPECTED_ROTATION_MAX, (
        f"rotation_count {rotations} outside {EXPECTED_ROTATION_MIN}..{EXPECTED_ROTATION_MAX}"
    )
    results = report["chunk_results"]
    assert len(results) == EXPECTED_CHUNKS, (
        f"expected {EXPECTED_CHUNKS} chunk_results, got {len(results)}"
    )
    indices = [c["chunk_index"] for c in results]
    assert indices == sorted(indices), "chunk_results not sorted by chunk_index"
    assert indices == list(range(EXPECTED_CHUNKS)), "chunk indices must be dense 0..n-1"

    for c in results:
        for field in ("chunk_index", "key_id", "status", "failure_reason"):
            assert field in c, f"chunk result missing field {field}: {c}"
        assert c["key_id"], "key_id must be populated even on failure"
        assert c["status"] in ("ok", "failed"), f"bad status {c['status']}"

    seen = {c["failure_reason"] for c in results if c["status"] == "failed"}
    missing = REQUIRED_REASONS - seen
    assert not missing, f"missing injected failure categories: {sorted(missing)}"

    summary = report["summary"]
    assert summary["total"] == EXPECTED_CHUNKS
    assert summary["ok"] + summary["failed"] == EXPECTED_CHUNKS
    assert summary["failed"] >= len(REQUIRED_REASONS)
    for reason, count in summary["reasons"].items():
        assert count >= 1, f"summary reason {reason} has zero count"

    print(
        "demo check OK:",
        f"rotations={rotations}",
        f"ok={summary['ok']}",
        f"failed={summary['failed']}",
        f"reasons={sorted(summary['reasons'])}",
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
