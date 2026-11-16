"""Sorted, offline-recomputable JSON reporting and verification.

The report is a plain JSON document (no NaN/Infinity tokens) with a stable
key order and entries sorted by relative path, so two identical runs produce
byte-identical files. :func:`verify_report` ignores every input-side field
and recomputes the output-side statistics directly from the rendered WAV
files, comparing each numeric field within an explicit tolerance and every
hash/structure field exactly.
"""

from __future__ import annotations

import json
import math
from pathlib import Path

import numpy as np

from audioprep import waveio
from audioprep.loudness import LoudnessAccumulator
from audioprep.normalize import StreamingStats

# Verification tolerances: PCM_24 quantization plus filter accumulation.
TOLERANCE_DB = 0.05
TOL_LINEAR = 1e-6
# Output statistics that verify recomputes from the rendered file itself.
RECOMPUTED_FIELDS = (
    "output_sample_rate",
    "output_channels",
    "output_frames",
    "rms_dbfs_after",
    "lufs_i_after",
    "peak_abs_after",
    "clipped_samples_after",
)


class ReportError(RuntimeError):
    """Raised when a report is missing, unreadable or structurally invalid."""


def _json_default(obj):
    if isinstance(obj, (np.floating, np.integer)):
        return obj.item()
    if isinstance(obj, Path):
        return str(obj)
    raise TypeError(f"not JSON serializable: {type(obj)!r}")


def build_report(params, results) -> dict:
    """Assemble the report payload with stable ordering and no infinite dB."""
    records = [r.record for r in results if r.ok]
    errors = [
        {"relative_path": r.relative, "error": r.error}
        for r in results
        if not r.ok
    ]
    records.sort(key=lambda rec: rec["relative_path"])
    errors.sort(key=lambda rec: rec["relative_path"])
    return {
        "schema_version": 1,
        "parameters": {
            "sample_rate": params.sample_rate,
            "channel_mode": params.channel_mode,
            "target_lufs": params.target_lufs,
            "peak_ceiling_dbfs": params.peak_ceiling_dbfs,
            "block_frames": params.block_frames,
        },
        "files_ok": len(records),
        "files_failed": len(errors),
        "records": records,
        "errors": errors,
    }


def write_report(path: Path, report: dict) -> None:
    """Atomically write the report, rejecting non-finite numeric values."""
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(f".{path.name}.tmp")
    text = json.dumps(
        report, indent=2, sort_keys=True, allow_nan=False, default=_json_default
    )
    tmp.write_text(text + "\n", encoding="utf-8")
    tmp.replace(path)


def read_report(path: Path) -> dict:
    try:
        report = json.loads(Path(path).read_text(encoding="utf-8"),
                            parse_constant=_reject_constant)
    except FileNotFoundError as exc:
        raise ReportError(f"report not found: {path}") from exc
    except (json.JSONDecodeError, ValueError) as exc:
        raise ReportError(f"invalid report JSON: {exc}") from exc
    if not isinstance(report, dict) or "records" not in report:
        raise ReportError("report missing records section")
    return report


def _reject_constant(value: str):
    raise ValueError(f"non-finite JSON constant not allowed: {value}")


def recompute_output(record: dict, output_dir: Path, block_frames: int) -> dict:
    """Recompute output-side statistics independently from the rendered WAV."""
    target = Path(output_dir) / record["relative_path"]
    target = target.with_suffix(waveio.OUTPUT_EXT)
    if not target.is_file():
        raise ReportError(f"missing output file: {target}")

    actual_hash = waveio.hash_file(target)
    info = waveio.probe_file(target)
    loud = LoudnessAccumulator(info.samplerate)
    stats = StreamingStats()
    frames = 0
    for block in waveio.read_blocks(target, block_frames):
        loud.add_block(block)
        stats.add_block(block)
        frames += block.shape[0]
    measurement = loud.finalize()
    return {
        "output_hash_sha256": actual_hash,
        "output_sample_rate": info.samplerate,
        "output_channels": info.channels,
        "output_frames": frames,
        "rms_dbfs_after": stats.rms_dbfs(),
        "lufs_i_after": measurement.lufs_i,
        "peak_abs_after": stats.peak,
        "clipped_samples_after": stats.clipped,
    }


def _compare(field: str, reported, actual, mismatches: list[str]) -> None:
    if field in ("output_sample_rate", "output_channels", "output_frames",
                 "clipped_samples_after", "output_hash_sha256"):
        if reported != actual:
            mismatches.append(f"{field}: report={reported!r} recomputed={actual!r}")
        return
    # Numeric dB/linear fields; null must match null exactly.
    if reported is None or actual is None:
        if reported != actual:
            mismatches.append(f"{field}: report={reported!r} recomputed={actual!r}")
        return
    tol = TOL_LINEAR if field == "peak_abs_after" else TOLERANCE_DB
    if not math.isfinite(reported) or not math.isfinite(actual) or abs(
        float(reported) - float(actual)
    ) > tol:
        mismatches.append(f"{field}: report={reported!r} recomputed={actual!r}")


def verify_report(report_path: Path, output_dir: Path) -> int:
    """Recompute every output and compare; exit 3 on any mismatch."""
    try:
        report = read_report(report_path)
    except ReportError as exc:
        print(f"verify error: {exc}")
        return 2
    block_frames = int(report.get("parameters", {}).get("block_frames", 4096))

    mismatches: list[str] = []
    checked = 0
    for record in report["records"]:
        checked += 1
        try:
            actual = recompute_output(record, output_dir, block_frames)
        except ReportError as exc:
            mismatches.append(f"{record.get('relative_path')}: {exc}")
            continue
        # Stored output hash is also recomputed independently.
        stored = {name: record.get(name) for name in RECOMPUTED_FIELDS}
        stored["output_hash_sha256"] = record.get("output_hash_sha256")
        for field in stored:
            _compare(field, stored[field], actual[field], mismatches)

    if mismatches:
        print(f"verify FAILED: {len(mismatches)} mismatch(es) across {checked} file(s)")
        for line in mismatches:
            print(f"  - {line}")
        return 3
    print(f"verify OK: {checked} file(s) recomputed and consistent")
    return 0
