"""Independent re-measurement of a completed report.

The verifier reads the original sources and the produced WAVs with
SoundFile, recomputes every reported statistic from scratch (it never
imports the pipeline's cached measurements), and reports differences
beyond a tolerance.
"""

from __future__ import annotations

from pathlib import Path
from typing import Any

import soundfile as sf

from .discover import check_output_relative, sha256_file
from .errors import VerifyError
from .loudness import analyze_reader
from .report import compare_entry, load_report, summarize_verify, utc_now
from .resample import output_length


def _measure(path: Path, chunk_frames: int = 65536) -> tuple[Any, Any]:
    try:
        info = sf.info(str(path))
    except Exception as exc:
        raise VerifyError(f"cannot open {path}: {exc}") from exc
    if info.channels not in (1, 2, 6):
        raise VerifyError(f"{path} has unsupported layout {info.channels}ch")
    try:
        handle = sf.SoundFile(str(path), "r")
    except Exception as exc:
        raise VerifyError(f"cannot open {path}: {exc}") from exc
    with handle:

        def reader(start: int, count: int):
            try:
                handle.seek(int(start))
                return handle.read(int(count), dtype="float64", always_2d=True)
            except Exception as exc:  # pragma: no cover - corrupt product
                raise VerifyError(f"cannot decode {path}: {exc}") from exc

        analysis = analyze_reader(
            reader, int(info.frames), int(info.channels), int(info.samplerate), chunk_frames
        )
    return analysis, info


def _required(recorded: dict[str, Any], key: str) -> Any:
    if key not in recorded:
        raise VerifyError(f"report entry for {recorded.get('relative_path')} lacks {key}")
    return recorded[key]


def _recompute(
    recorded: dict[str, Any], input_dir: Path, output_dir: Path
) -> tuple[dict[str, Any], list[str]]:
    notes: list[str] = []
    relative = _required(recorded, "relative_path")
    source = input_dir / relative
    product = output_dir / check_output_relative(relative)
    if not source.is_file():
        raise VerifyError(f"source for {relative} is missing: {source}")
    if not product.is_file():
        raise VerifyError(f"output for {relative} is missing: {product}")
    recorded_rate = int(_required(recorded, "sample_rate"))

    source_hash = sha256_file(source)
    if recorded.get("source_sha256") not in (None, source_hash):
        notes.append("source file changed since the report was written")
    product_hash = sha256_file(product)
    if recorded.get("output_sha256") not in (None, product_hash):
        notes.append("output file changed since the report was written")

    before, source_info = _measure(source)
    after, output_info = _measure(product)
    expected_frames = output_length(
        int(source_info.frames), int(source_info.samplerate), recorded_rate
    )
    if after.frames != expected_frames:
        notes.append(
            f"output has {after.frames} frames, expected {expected_frames} for "
            f"{source_info.samplerate} -> {recorded_rate} Hz"
        )
    if int(output_info.samplerate) != recorded_rate:
        notes.append(f"output sample rate is {output_info.samplerate}, recorded {recorded_rate}")
    if output_info.channels != source_info.channels:
        notes.append(
            f"output has {output_info.channels} channels, source has {source_info.channels}"
        )
    recomputed = {
        "frames_in": int(source_info.frames),
        "frames_out": int(after.frames),
        "channels": int(source_info.channels),
        "sample_rate": recorded_rate,
        "rms_before_dbfs": before.rms_dbfs,
        "lufs_before": before.lufs,
        "peak_before_dbfs": before.peak_dbfs,
        "rms_after_dbfs": after.rms_dbfs,
        "lufs_after": after.lufs,
        "peak_after_dbfs": after.peak_dbfs,
        "clip_samples_before": before.clip_samples,
        "clip_samples_after": after.clip_samples,
    }
    return recomputed, notes


def verify(
    input_dir: Path,
    output_dir: Path,
    report_path: Path,
    tolerance: float,
    chunk_frames: int = 65536,
) -> dict[str, Any]:
    report = load_report(report_path)
    if tolerance < 0:
        raise VerifyError("tolerance must be non-negative")
    entries: list[dict[str, Any]] = []
    for recorded in report["files"]:
        if not isinstance(recorded, dict) or "relative_path" not in recorded:
            raise VerifyError("report contains a malformed file entry")
        try:
            recomputed, notes = _recompute(recorded, input_dir, output_dir)
        except VerifyError as exc:
            # A missing or unreadable product is a verification difference
            # for that file, not a reason to abandon the rest of the report.
            entries.append(
                {
                    "relative_path": recorded["relative_path"],
                    "recorded": recorded,
                    "recomputed": None,
                    "diffs": [
                        {
                            "field": "integrity",
                            "recorded": None,
                            "recomputed": str(exc),
                            "delta": None,
                        }
                    ],
                }
            )
            continue
        diffs = compare_entry(recorded, recomputed, tolerance)
        for note in notes:
            diffs.append(
                {"field": "integrity", "recorded": None, "recomputed": note, "delta": None}
            )
        entries.append(
            {
                "relative_path": recorded["relative_path"],
                "recorded": {key: recorded.get(key) for key in recomputed},
                "recomputed": recomputed,
                "diffs": diffs,
            }
        )
    recorded_errors = report.get("errors") or []
    summary = summarize_verify(entries, tolerance, report_path)
    summary["recorded_errors"] = recorded_errors
    summary["checked_at"] = utc_now()
    return summary
