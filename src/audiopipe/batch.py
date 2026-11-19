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
