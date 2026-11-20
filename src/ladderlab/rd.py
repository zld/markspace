"""Rate-distortion candidate preparation.

Takes raw per-parameter results, drops duplicate bitstreams (identical
content hash) keeping the highest-quality representative, and emits a
candidate list sorted by bitrate for the ladder DP.
"""
from __future__ import annotations


def dedupe_candidates(results: list[dict]) -> tuple[list[dict], int]:
    """Collapse identical bitstreams; returns (candidates, dropped)."""
    best_by_hash: dict[str, dict] = {}
    for r in sorted(results, key=lambda r: (r["stream_hash"], r["param_key"])):
        cur = best_by_hash.get(r["stream_hash"])
        if cur is None or _better(r, cur):
            best_by_hash[r["stream_hash"]] = r
    dropped = len(results) - len(best_by_hash)
    candidates = sorted(
        best_by_hash.values(), key=lambda r: (r["actual_bps"], r["param_key"])
    )
    return candidates, dropped


def _better(a: dict, b: dict) -> bool:
    """Higher quality wins; ties break on lower bitrate then param key."""
    if a["quality_score"] != b["quality_score"]:
        return a["quality_score"] > b["quality_score"]
    if a["actual_bps"] != b["actual_bps"]:
        return a["actual_bps"] < b["actual_bps"]
    return a["param_key"] < b["param_key"]
