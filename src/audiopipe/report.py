"""Per-file JSON and batch summary writing. Paths are always relative."""

from __future__ import annotations

import json
import os
from pathlib import Path

from .batch import BatchOutcome
from .errors import ProcessingError

REPORT_VERSION = 1


def _atomic_json(path: Path, payload: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.parent / f".{path.name}.tmp-{os.getpid()}"
    try:
        with open(tmp, "w", encoding="utf-8") as fh:
            json.dump(payload, fh, indent=2, sort_keys=True, allow_nan=False)
            fh.write("\n")
            fh.flush()
            os.fsync(fh.fileno())
        os.replace(tmp, path)
    except (OSError, ValueError) as exc:
        try:
            tmp.unlink(missing_ok=True)
        except OSError:
            pass
        raise ProcessingError(f"report write failed: {exc}", reason="report_error") from None


def build_summary(outcome: BatchOutcome, config) -> dict:
    """Batch-level document: stable ordering, relative paths, no absolute paths."""
    results = sorted(outcome.results, key=lambda r: r.input_path)
    return {
        "report_version": REPORT_VERSION,
        "parameters": {
            "sample_rate": config.target_rate,
            "channels": config.channels,
            "target_lufs": config.target_lufs,
            "max_peak_dbfs": config.max_peak_dbfs,
            "block_size": config.block_size,
            "zero_crossings": config.resample.zero_crossings,
            "rolloff": config.resample.rolloff,
        },
        "counts": {
            "total": len(results),
            "succeeded": sum(1 for r in results if r.ok),
            "failed": sum(1 for r in results if not r.ok),
            "peak_limited": outcome.limited_by_peak,
            "silent": outcome.silent,
        },
        "files": [r.as_dict() for r in results],
        "failures": [
            {"input": r.input_path, "reason": r.reason, "message": r.message}
            for r in results
            if not r.ok
        ],
    }


def write_report(path: Path, outcome: BatchOutcome, config) -> Path:
    document = build_summary(outcome, config)
    _atomic_json(path, document)
    return path


def write_file_reports(directory: Path, outcome: BatchOutcome) -> list[Path]:
    """Write one JSON per input, named after its relative path."""
    written: list[Path] = []
    for result in sorted(outcome.results, key=lambda r: r.input_path):
        rel = Path(result.input_path).with_suffix(".json")
        target = directory / rel
        _atomic_json(target, result.as_dict())
        written.append(target)
    return written


def render_terminal(outcome: BatchOutcome) -> str:
    """Compact per-file lines for the terminal."""
    lines: list[str] = []
    for result in outcome.results:
        if not result.ok:
            lines.append(f"FAIL {result.input_path}: {result.message} [{result.reason}]")
            continue
        params = result.params
        stats = result.stats
        lines.append(
            "ok   {src} -> {dst} | sample_rate={rate} channels={ch} "
            "frames_in={fin} frames_out={fout} "
            "rms_before={rb:.6f} rms_after={ra:.6f} "
            "lufs_before={lb} lufs_after={la} "
            "clipped_samples_before={cb} clipped_samples_after={ca} "
            "gain_db={g:.3f} reason={reason}".format(
                src=result.input_path,
                dst=result.output_path,
                rate=params.get("sample_rate"),
                ch=params.get("channels"),
                fin=params.get("frames_in"),
                fout=params.get("frames_out"),
                rb=stats.get("rms_before") or 0.0,
                ra=stats.get("rms_after") or 0.0,
                lb=_fmt(stats.get("lufs_before")),
                la=_fmt(stats.get("lufs_after")),
                cb=stats.get("clipped_samples_before"),
                ca=stats.get("clipped_samples_after"),
                g=params.get("gain_db") or 0.0,
                reason=result.reason or "converted",
            )
        )
    failed = len(outcome.failed)
    lines.append(
        f"batch: {len(outcome.succeeded)} ok, {failed} failed, "
        f"{outcome.limited_by_peak} peak-limited, {outcome.silent} silent"
    )
    return "\n".join(lines)


def _fmt(value: object) -> str:
    if value is None:
        return "undefined"
    return f"{float(value):.3f}"
