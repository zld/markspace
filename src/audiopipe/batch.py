"""Batch scanning, per-file processing, scheduling and aggregation.

This module owns the batch contract:

* :func:`scan_inputs` finds WAV files recursively and returns sorted
  POSIX-style relative paths, so runs and reports are reproducible.
* :func:`validate_paths` refuses overlapping input/output trees, which
  would otherwise let the pipeline overwrite its own source material.
* :func:`process_file` runs the full read -> resample -> measure -> gain
  -> write chain for one file and always returns a record; per-file
  failures are captured in that record and never abort the batch.
* :func:`run_batch` schedules files (sequentially or in a process pool)
  and assembles the report document.
* :func:`verify_report` re-reads the produced WAVs and re-derives the
  reported numbers, for offline auditing of a report.

The per-file measurement strategy is described in :func:`process_file`.
"""

from __future__ import annotations

import concurrent.futures
import json
import math
import os
import time
from dataclasses import dataclass
from typing import Optional

import numpy as np

from .io import (
    PipelineError,
    WavReader,
    WavWriter,
    count_rail_samples,
    probe_wav,
    quantise_pcm,
)
from .loudness import LevelMeter, PEAK_CEILING, decide_gain
from .resample import PolyphaseResampler
from . import report as report_mod
from .report import ReportWriteError

__all__ = [
    "BatchConfig",
    "ConfigError",
    "FileConfig",
    "format_summary",
    "process_file",
    "run_batch",
    "scan_inputs",
    "validate_paths",
    "verify_report",
]

DEFAULT_BLOCK_FRAMES = 65536


class ConfigError(Exception):
    """Batch-level misconfiguration; maps to exit code 2."""


@dataclass(frozen=True)
class FileConfig:
    """Everything needed to process one file."""

    input_dir: str
    output_dir: str
    relative_path: str
    output_rate: int
    output_subtype: str
    target_lufs: float
    max_gain_db: Optional[float]
    max_peak_db: Optional[float]
    block_frames: int = DEFAULT_BLOCK_FRAMES

    @property
    def input_path(self) -> str:
        return os.path.join(self.input_dir, *self.relative_path.split("/"))

    @property
    def output_path(self) -> str:
        return os.path.join(self.output_dir, *self.relative_path.split("/"))


@dataclass(frozen=True)
class BatchConfig:
    """Batch-level options shared by every file."""

    input_dir: str
    output_dir: str
    report_path: str
    output_rate: int
    output_subtype: str = "PCM_24"
    target_lufs: float = -18.0
    max_gain_db: Optional[float] = 12.0
    max_peak_db: Optional[float] = -0.1
    jobs: int = 1
    block_frames: int = DEFAULT_BLOCK_FRAMES


# ---------------------------------------------------------------------------
# Scanning and path safety
# ---------------------------------------------------------------------------

def scan_inputs(input_dir: str) -> list[str]:
    """Return sorted POSIX relative paths of every ``.wav`` under ``input_dir``."""
    if not os.path.isdir(input_dir):
        raise ConfigError(f"input directory does not exist: {input_dir}")
    found: list[str] = []
    for root, dirnames, filenames in os.walk(input_dir, followlinks=False):
        dirnames.sort()
        for name in sorted(filenames):
            if name.lower().endswith(".wav"):
                full = os.path.join(root, name)
                if os.path.isfile(full):
                    rel = os.path.relpath(full, input_dir)
                    found.append(rel.replace(os.sep, "/"))
    found.sort()
    return found


def _resolved(path: str) -> str:
    return os.path.realpath(os.path.abspath(path))


def validate_paths(input_dir: str, output_dir: str) -> None:
    """Reject identical or nested input/output trees."""
    src = _resolved(input_dir)
    dst = _resolved(output_dir)
    if src == dst:
        raise ConfigError("input and output directories must differ")
    common = os.path.commonpath([src, dst])
    if common == src:
        raise ConfigError("output directory must not be inside the input directory")
    if common == dst:
        raise ConfigError("input directory must not be inside the output directory")


# ---------------------------------------------------------------------------
# Per-file processing
# ---------------------------------------------------------------------------

def _peak_ceiling(max_peak_db: Optional[float]) -> float:
    """Linear peak ceiling from a dBFS limit.

    Clamped to just below 1.0 so rounding at full scale can never push a
    predicted peak above the representable grid.
    """
    if max_peak_db is None:
        return PEAK_CEILING
    if not math.isfinite(max_peak_db) or max_peak_db > 0.0:
        raise ConfigError(f"max_peak_db must be <= 0, got {max_peak_db!r}")
    return min(PEAK_CEILING, 10.0 ** (max_peak_db / 20.0))


def _measurement_dict(meas) -> dict:
    return {
        "rms": meas.rms,
        "rms_per_channel": list(meas.rms_per_channel),
        "lufs": meas.lufs,
        "peak": meas.peak,
        "clipped_samples": meas.clipped_samples,
        "frames": meas.frames,
    }


def _empty_record(config: FileConfig) -> dict:
    return {
        "relative_path": config.relative_path,
        "status": "error",
        "input": {
            "sample_rate": None,
            "channels": None,
            "frames": None,
            "subtype": None,
        },
        "output": {
            "path": config.relative_path,
            "sample_rate": config.output_rate,
            "channels": None,
            "frames": None,
            "subtype": config.output_subtype,
        },
        "padding_frames": None,
        "before": {},
        "after": {},
        "gain": {},
        "resample": {},
        "error": None,
        "duration_seconds": None,
    }


def process_file(config: FileConfig) -> dict:
    """Process one file and return its report record (never raises).

    Two streaming passes:

    1. resample the input through the polyphase kernel and measure the
       resampled, pre-gain programme (``before``), then plan the gain;
    2. repeat resampling, apply the gain, quantise, measure the decoded
       output samples (``after``) and write the WAV.

    Both passes are block based, so memory is one block plus the
    resampler history regardless of file length.
    """
    started = time.perf_counter()
    record = _empty_record(config)
    ceiling = _peak_ceiling(config.max_peak_db)
    try:
        info = probe_wav(config.input_path)
        record["input"].update(
            {
                "sample_rate": info.sample_rate,
                "channels": info.channels,
                "frames": info.frames,
                "subtype": info.subtype,
            }
        )
        record["output"]["channels"] = info.channels

        # ---- pass 1: resample and measure the pre-gain programme ------
        resampler = PolyphaseResampler(info.sample_rate, config.output_rate, info.channels)
        before_meter = LevelMeter(config.output_rate, info.channels)
        source_rail_samples = 0

        def first_pass(block: np.ndarray, reader) -> None:
            nonlocal source_rail_samples
            source_rail_samples += count_rail_samples(block, reader.info.subtype)
            before_meter.feed(resampler.process_chunk(block))

        in_frames = _stream(
            config,
            first_pass,
            final=lambda: before_meter.feed(resampler.finish()),
        )
        before = before_meter.snapshot()
        expected = resampler.output_frames_for(in_frames)
        if info.frames and in_frames != info.frames:
            raise PipelineError(
                "internal_error", f"read {in_frames} frames, header declares {info.frames}"
            )
        if before.frames != expected:
            raise PipelineError(
                "internal_error",
                f"measured {before.frames} frames, expected {expected}",
            )
        decision = decide_gain(
            before.lufs,
            before.peak,
            config.target_lufs,
            max_gain_db=config.max_gain_db,
            peak_ceiling=ceiling,
        )

        # ---- pass 2: gain, quantise, measure decoded output, write ----
        resampler = PolyphaseResampler(info.sample_rate, config.output_rate, info.channels)
        after_meter = LevelMeter(config.output_rate, info.channels)
        writer = WavWriter(
            config.output_path, config.output_rate, info.channels, config.output_subtype
        )
        try:

            def handle(block: np.ndarray, _reader) -> None:
                out = resampler.process_chunk(block)
                if out.shape[0]:
                    _write_gained(writer, out, decision.gain_linear, after_meter)

            _stream(config, handle, final=lambda: _write_gained(
                writer, resampler.finish(), decision.gain_linear, after_meter
            ))
            if writer.frames_written != expected:
                raise PipelineError(
                    "internal_error",
                    f"wrote {writer.frames_written} frames, expected {expected}",
                )
            writer.commit()
        except BaseException:
            writer.abort()
            raise
        after = after_meter.snapshot()

        left_pad, right_pad = resampler.padding_breakdown
        record.update(
            {
                "status": "ok",
                "padding_frames": left_pad + right_pad,
                "resample": {
                    "input_rate": info.sample_rate,
                    "output_rate": config.output_rate,
                    "numerator": resampler.ratio.numerator,
                    "denominator": resampler.ratio.denominator,
                    "input_frames": in_frames,
                    "output_frames": writer.frames_written,
                    "padding_left_frames": left_pad,
                    "padding_right_frames": right_pad,
                    "filter": resampler.spec.to_dict(),
                },
                "before": _measurement_dict(before),
                "after": _measurement_dict(after),
                "gain": _gain_dict(decision, config.max_gain_db),
            }
        )
        record["output"]["frames"] = writer.frames_written
        record["output"]["out_of_range_samples"] = writer.out_of_range_samples
        # Clipping is reported as four distinct quantities, never merged:
        #  * input.rail_clipped_samples -- source samples pinned to a PCM
        #    rail (clipping already present in the input material);
        #  * before.clipped_samples / resample.overshoot_samples -- the
        #    resampled programme above full scale before any gain
        #    (interpolation overshoot);
        #  * output.out_of_range_samples -- post-gain samples outside
        #    [-1, 1] immediately before quantisation;
        #  * after.clipped_samples -- samples pinned to an output PCM rail,
        #    i.e. magnitude actually lost to the fixed-point grid.
        record["input"]["rail_clipped_samples"] = source_rail_samples
        record["resample"]["overshoot_samples"] = before.clipped_samples
        record["after"]["clipped_samples"] = writer.rail_samples
        return record
    except PipelineError as exc:
        record["error"] = exc.to_dict()
        return record
    except Exception as exc:  # isolate unexpected per-file failures too
        record["error"] = {
            "code": "internal_error",
            "message": f"{type(exc).__name__}: {exc}",
        }
        return record
    finally:
        record["duration_seconds"] = time.perf_counter() - started


def _gain_dict(decision, max_gain_db: Optional[float]) -> dict:
    return {
        "applied_gain_db": decision.gain_db,
        "linear_gain": decision.gain_linear,
        "requested_gain_db": decision.requested_gain_db,
        "max_gain_db": max_gain_db,
        "max_gain_limited": decision.max_gain_limited,
        "peak_limited": decision.peak_limited,
        "predicted_peak": decision.predicted_peak,
        "reason": decision.reason,
    }


def _stream(config: FileConfig, handle, *, final=None) -> int:
    """Read in blocks, call ``handle(block, reader)``, then ``final()``."""
    in_frames = 0
    with WavReader(config.input_path, block_frames=config.block_frames) as reader:
        for block in reader.blocks():
            in_frames += block.shape[0]
            handle(block, reader)
    if final is not None:
        final()
    return in_frames


def _write_gained(writer: WavWriter, block: np.ndarray, gain: float, meter: LevelMeter) -> None:
    """Apply gain; measure the decoded output samples; write.

    The meter sees the exact decoded samples (``ints / scale``), so the
    reported "after" figures describe the audio in the output file, while
    the writer counts pre-quantisation out-of-range samples separately.
    """
    if block.shape[0] == 0:
        return
    scaled = block * gain
    _ints, decoded, _out_of_range, _rail_samples = quantise_pcm(scaled, writer.subtype)
    meter.feed(decoded)
    writer.write(scaled)


# ---------------------------------------------------------------------------
# Batch scheduling
# ---------------------------------------------------------------------------

def _to_posix(path: str) -> str:
    return path.replace(os.sep, "/")


def _report_config(config: BatchConfig, file_count: int) -> dict:
    return {
        "input_dir": _to_posix(config.input_dir),
        "output_dir": _to_posix(config.output_dir),
        "output_rate": config.output_rate,
        "output_subtype": config.output_subtype,
        "target_lufs": config.target_lufs,
        "max_gain_db": config.max_gain_db,
        "max_peak_db": config.max_peak_db,
        "jobs": config.jobs,
        "block_frames": config.block_frames,
        "files_discovered": file_count,
    }


def _file_config(config: BatchConfig, relative_path: str) -> FileConfig:
    return FileConfig(
        input_dir=config.input_dir,
        output_dir=config.output_dir,
        relative_path=relative_path,
        output_rate=config.output_rate,
        output_subtype=config.output_subtype,
        target_lufs=config.target_lufs,
        max_gain_db=config.max_gain_db,
        max_peak_db=config.max_peak_db,
        block_frames=config.block_frames,
    )


def _validate_batch_config(config: BatchConfig) -> None:
    """Reject batch-level misconfiguration before any audio is touched."""
    if not isinstance(config.output_rate, int) or isinstance(config.output_rate, bool):
        raise ConfigError("output rate must be an integer")
    if not 8000 <= config.output_rate <= 384000:
        raise ConfigError(f"output rate out of range: {config.output_rate}")
    if config.output_subtype not in ("PCM_16", "PCM_24"):
        raise ConfigError(
            f"output subtype must be PCM_16 or PCM_24, got {config.output_subtype!r}"
        )
    if not math.isfinite(config.target_lufs):
        raise ConfigError("target LUFS must be finite")
    if config.max_gain_db is not None:
        if not math.isfinite(config.max_gain_db) or config.max_gain_db < 0.0:
            raise ConfigError(
                f"max gain must be finite and non-negative, got {config.max_gain_db!r}"
            )
    _peak_ceiling(config.max_peak_db)
    if config.jobs < 1:
        raise ConfigError(f"jobs must be >= 1, got {config.jobs}")
    if config.block_frames < 1:
        raise ConfigError(f"block frames must be >= 1, got {config.block_frames}")
    if not config.report_path or not str(config.report_path).strip():
        raise ConfigError("report path must not be empty")
    validate_paths(config.input_dir, config.output_dir)
    # The report must not be written inside the source tree, and it must
    # be a file path, not an existing directory.
    report_resolved = _resolved(config.report_path)
    source = _resolved(config.input_dir)
    if os.path.commonpath([source, report_resolved]) == source:
        raise ConfigError("report must not be written inside the input directory")
    if os.path.isdir(report_resolved):
        raise ReportWriteError(f"report path is a directory: {config.report_path}")


def run_batch(config: BatchConfig) -> dict:
    """Process every WAV under ``config.input_dir``; return the report dict.

    Per-file errors are captured in the records; only batch-level
    problems raise :class:`ConfigError`.
    """
    _validate_batch_config(config)
    try:
        os.makedirs(config.output_dir, exist_ok=True)
    except OSError as exc:
        raise ConfigError(f"cannot create output directory: {exc}") from exc
    if not os.path.isdir(config.output_dir):
        raise ConfigError(f"output path is not a directory: {config.output_dir}")

    relative_paths = scan_inputs(config.input_dir)
    configs = [_file_config(config, rel) for rel in relative_paths]
    started_at = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    started = time.perf_counter()

    if config.jobs == 1 or len(configs) <= 1:
        records = [process_file(cfg) for cfg in configs]
    else:
        records = [None] * len(configs)
        executor_class = concurrent.futures.ProcessPoolExecutor
        with executor_class(max_workers=min(config.jobs, len(configs))) as pool:
            futures = {pool.submit(process_file, cfg): idx for idx, cfg in enumerate(configs)}
            for future in concurrent.futures.as_completed(futures):
                idx = futures[future]
                try:
                    records[idx] = future.result()
                except Exception as exc:  # pragma: no cover - worker died
                    record = _empty_record(configs[idx])
                    record["error"] = {
                        "code": "worker_failed",
                        "message": f"{type(exc).__name__}: {exc}",
                    }
                    records[idx] = record
    duration = time.perf_counter() - started
    return report_mod.build_batch_report(
        command="normalize",
        config=_report_config(config, len(relative_paths)),
        files=records,
        started_at=started_at,
        duration_seconds=duration,
    )


def format_summary(report: dict) -> str:
    """One-line terminal summary, e.g. ``files=3 success=3 failed=0``."""
    summary = report["summary"]
    return (
        f"files={summary['files']} success={summary['success']} "
        f"failed={summary['failed']}"
    )


# ---------------------------------------------------------------------------
# Report verification
# ---------------------------------------------------------------------------

RMS_TOLERANCE_ABS = 1e-9
RMS_TOLERANCE_REL = 1e-6
PEAK_TOLERANCE = 1e-9
LUFS_TOLERANCE = 1e-4


class VerificationError(Exception):
    """The report itself cannot be used for verification."""


def verify_report(report_path: str) -> dict:
    """Re-read every successful output and recompute its measurements.

    Checks the sample rate, channel count, frame count, RMS, peak,
    K-weighted LUFS and rail-clipping count against the JSON record
    within documented tolerances.  Returns a verification document.
    """
    try:
        with open(report_path, "r", encoding="utf-8") as fh:
            report = json.load(fh)
    except (OSError, ValueError) as exc:
        raise VerificationError(f"cannot read report: {exc}") from exc
    if not isinstance(report, dict) or not isinstance(report.get("files"), list):
        raise VerificationError("report is missing a 'files' list")

    output_dir = report.get("config", {}).get("output_dir")
    if not output_dir:
        raise VerificationError("report is missing config.output_dir")

    results = []
    for record in report["files"]:
        if record.get("status") != "ok":
            results.append(
                {
                    "relative_path": record.get("relative_path"),
                    "status": "skipped",
                    "reason": "processing failed",
                }
            )
            continue
        results.append(_verify_record(record, output_dir))

    checked = sum(1 for r in results if r["status"] in ("ok", "mismatch"))
    mismatched = sum(1 for r in results if r["status"] == "mismatch")
    return {
        "report": report_path,
        "summary": {
            "files": len(results),
            "checked": checked,
            "mismatched": mismatched,
            "skipped": len(results) - checked,
        },
        "files": results,
    }


def _verify_record(record: dict, output_dir: str) -> dict:
    relative_path = record.get("relative_path")
    expected = record.get("output", {})
    subtype = expected.get("subtype", "PCM_24")
    path = os.path.join(output_dir, *str(relative_path).split("/"))
    checks: list[dict] = []
    try:
        info = probe_wav(path)
        meter = LevelMeter(info.sample_rate, info.channels)
        rails = 0
        with WavReader(path, block_frames=DEFAULT_BLOCK_FRAMES) as reader:
            for block in reader.blocks():
                meter.feed(block)
                rails += count_rail_samples(block, subtype)
        measured = meter.snapshot()
    except PipelineError as exc:
        return {
            "relative_path": relative_path,
            "status": "mismatch",
            "error": exc.to_dict(),
            "checks": checks,
        }

    def check(name, reported, recomputed, ok, tolerance=None):
        checks.append(
            {
                "name": name,
                "reported": reported,
                "recomputed": recomputed,
                "tolerance": tolerance,
                "ok": bool(ok),
            }
        )

    check(
        "sample_rate",
        expected.get("sample_rate"),
        info.sample_rate,
        expected.get("sample_rate") == info.sample_rate,
    )
    check(
        "channels",
        expected.get("channels"),
        info.channels,
        expected.get("channels") == info.channels and 1 <= info.channels <= 8,
    )
    check(
        "frames",
        expected.get("frames"),
        info.frames,
        expected.get("frames") == info.frames,
    )
    reported_after = record.get("after", {})
    reported_rms = reported_after.get("rms")
    ok = (
        isinstance(reported_rms, (int, float))
        and abs(reported_rms - measured.rms)
        <= max(RMS_TOLERANCE_ABS, RMS_TOLERANCE_REL * abs(measured.rms))
    )
    check(
        "after.rms",
        reported_rms,
        measured.rms,
        ok,
        {"abs": RMS_TOLERANCE_ABS, "rel": RMS_TOLERANCE_REL},
    )
    reported_peak = reported_after.get("peak")
    check(
        "after.peak",
        reported_peak,
        measured.peak,
        isinstance(reported_peak, (int, float))
        and abs(reported_peak - measured.peak) <= PEAK_TOLERANCE,
        {"abs": PEAK_TOLERANCE},
    )
    reported_lufs = reported_after.get("lufs")
    if reported_lufs is None or measured.lufs is None:
        lufs_ok = reported_lufs == measured.lufs
        lufs_delta = None
    else:
        lufs_delta = abs(reported_lufs - measured.lufs)
        lufs_ok = lufs_delta <= LUFS_TOLERANCE
    check("after.lufs", reported_lufs, measured.lufs, lufs_ok, {"abs": LUFS_TOLERANCE})
    reported_clipped = reported_after.get("clipped_samples")
    check(
        "after.clipped_samples",
        reported_clipped,
        rails,
        reported_clipped == rails,
        {"abs": 0},
    )
    status = "ok" if all(c["ok"] for c in checks) else "mismatch"
    return {
        "relative_path": relative_path,
        "status": status,
        "checks": checks,
    }
