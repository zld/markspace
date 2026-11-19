"""Deterministic JSON reporting for per-file records and batch summaries.

All float statistics are rounded to 6 decimal places; silence is
represented as JSON null. File lists are sorted by source path so a
report can be recomputed and diffed offline.
"""
from __future__ import annotations

import json
from pathlib import Path
from typing import Any

STATISTICS_DEFINITION = {
    "rms_dbfs": "20*log10(root-mean-square), per channel and over all samples; null when silent",
    "lufs": "ungated K-weighted approximation (BS.1770 shelf + RLB biquads at 48 kHz, "
            "signal resampled with the built-in kernel); not a full gated broadcast-standard "
            "measurement; null when silent",
    "clipped": "count of samples with |x| >= 1.0 in the float domain",
    "silence": "statistics with zero energy are reported as null and silent=true",
    "float_precision": "all float statistics rounded to 6 decimal places",
    "true_peak_ceiling_dbfs": -1.0,
}


def num(value: float | None) -> float | None:
    """Reproducible float representation; None stays None."""
    if value is None:
        return None
    return round(float(value), 6)


def file_record(*, source: str, output: str | None, status: str,
                error: str | None = None, message: str | None = None,
                stats: dict[str, Any] | None = None) -> dict[str, Any]:
    rec: dict[str, Any] = {
        "source_path": source,
        "output_path": output,
        "status": status,
        "error": error,
    }
    if message:
        rec["message"] = message
    if stats:
        rec.update(stats)
    return rec


def batch_report(records: list[dict[str, Any]], *, input_dir: str,
                 output_dir: str, target_rate: int,
                 target_lufs: float) -> dict[str, Any]:
    ordered = sorted(records, key=lambda r: r["source_path"])
    summary = {
        "total": len(ordered),
        "succeeded": sum(1 for r in ordered if r["status"] == "ok"),
        "failed": sum(1 for r in ordered if r["status"] == "failed"),
        "skipped": sum(1 for r in ordered if r["status"] == "skipped"),
    }
    return {
        "tool": "audionorm",
        "input_dir": input_dir,
        "output_dir": output_dir,
        "target_rate": target_rate,
        "target_lufs": target_lufs,
        "summary": summary,
        "statistics_definition": STATISTICS_DEFINITION,
        "files": ordered,
    }


def write_report(path: Path, report: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(report, indent=2, allow_nan=False) + "\n",
                    encoding="utf-8")
