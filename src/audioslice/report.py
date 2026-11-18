"""Machine-readable JSON summaries for batch runs and stored results."""
from __future__ import annotations

import json
from pathlib import Path

from .batch import BatchSummary
from .storage import Storage


def build_batch_report(summary: BatchSummary) -> dict:
    """Aggregate one batch run; silence totals use exact sample arithmetic."""
    files_out = [f.to_dict() for f in summary.files]
    silence_seconds = round(
        sum(f.silence_total_seconds for f in summary.files), 6
    )
    return {
        "input_dir": summary.input_dir,
        "params": summary.params.to_dict(),
        "param_fingerprint": summary.params.fingerprint(),
        "files_seen": len(summary.files),
        "files_processed": summary.processed,
        "files_cached": summary.cached,
        "files_failed": summary.failed,
        "segments": summary.segment_total,
        "silence_total_seconds": silence_seconds,
        "files": files_out,
    }


def render_batch_report(summary: BatchSummary) -> str:
    return json.dumps(build_batch_report(summary), indent=2, sort_keys=True)


def build_list_report(storage: Storage, task: str | None = None) -> dict:
    rows = storage.list_results(task)
    return {
        "task_filter": task,
        "count": len(rows),
        "results": [
            {
                "result_id": r["result_id"],
                "task_id": r["task_id"],
                "path": r["path"],
                "status": r["status"],
                "sample_rate": r["sample_rate"],
                "n_samples": r["n_samples"],
                "segments": r["segment_count"],
                "silence_total_seconds": (
                    round(r["silence_total_samples"] / r["sample_rate"], 6)
                    if r["silence_total_samples"] is not None and r["sample_rate"]
                    else None
                ),
                "param_fingerprint": r["param_fingerprint"],
                "error_message": r["error_message"],
                "created_at": r["created_at"],
            }
            for r in rows
        ],
    }


def build_summary_report(storage: Storage, task: str | None = None) -> dict:
    rows = storage.list_results(task)
    ok = [r for r in rows if r["status"] == "ok"]
    failed = [r for r in rows if r["status"] == "error"]
    silence_samples = sum(int(r["silence_total_samples"] or 0) for r in ok)
    rates = {int(r["sample_rate"]) for r in ok if r["sample_rate"]}
    rate = next(iter(rates)) if len(rates) == 1 else None
    return {
        "task_filter": task,
        "results_total": len(rows),
        "results_ok": len(ok),
        "results_failed": len(failed),
        "segments": sum(int(r["segment_count"]) for r in ok),
        "silence_total_samples": silence_samples,
        "silence_total_seconds": (
            round(silence_samples / rate, 6) if rate else None
        ),
        "mixed_sample_rates": None if rate is None else sorted(rates),
        "failed_paths": [r["path"] for r in failed],
    }


def render_json(payload: dict) -> str:
    return json.dumps(payload, indent=2, sort_keys=True)


def write_report(payload: dict, out_path: str | Path) -> Path:
    p = Path(out_path)
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(render_json(payload) + "\n", encoding="utf-8")
    return p
