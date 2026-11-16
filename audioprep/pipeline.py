"""Batch pipeline orchestration.

Each input file runs as an independent, two-pass streaming job:

* **pass 1 (measure)** -- decode fixed-size frame blocks, map channels into
  the output layout, resample with a per-file :class:`StreamingResampler`,
  and accumulate K-weighted LUFS plus RMS/peak/clipping statistics. Only
  block-sized arrays and filter/resampler state are held in memory;
* **decision** -- derive the gain from the measured loudness and the peak
  ceiling;
* **pass 2 (render)** -- repeat the same decode/map/resample path, apply the
  gain, re-measure post-normalization statistics, and stream into an
  :class:`AtomicWavWriter`. The target is replaced only on full success; on
  any failure the temp file is removed and the error is recorded per file.

Resampler coefficient plans are built once per distinct rate pair and shared
read-only across worker threads; every file keeps its own phase state.
Bad files, zero-frame inputs and non-finite samples never abort the batch.
"""

from __future__ import annotations

import math
import threading
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field
from pathlib import Path

import numpy as np

from audioprep import channels as chan
from audioprep import waveio
from audioprep.loudness import LoudnessAccumulator
from audioprep.normalize import StreamingStats, decide_gain
from audioprep.resampler import ResamplerPlan, StreamingResampler, build_plan


class PipelineError(RuntimeError):
    """Raised for batch-level configuration problems."""


@dataclass
class ProcessParams:
    """All parameters governing one batch, shared by every file."""

    sample_rate: int
    channel_mode: str
    target_lufs: float
    peak_ceiling_dbfs: float
    block_frames: int


@dataclass
class FileResult:
    """Per-file outcome; exactly one of record/error is populated."""

    relative: str
    ok: bool
    record: dict | None = None
    error: str | None = None


def _check_finite(block: np.ndarray, where: str) -> None:
    if not np.all(np.isfinite(block)):
        raise FloatingPointError(f"non-finite sample encountered {where}")


@dataclass
class _PlanCache:
    """Thread-safe lazily built cache of immutable resampler plans."""

    _plans: dict[tuple[int, int], ResamplerPlan] = field(default_factory=dict)
    _lock: threading.Lock = field(default_factory=threading.Lock)

    def get(self, in_rate: int, out_rate: int) -> ResamplerPlan:
        key = (in_rate, out_rate)
        plan = self._plans.get(key)
        if plan is not None:
            return plan
        with self._lock:
            plan = self._plans.get(key)
            if plan is None:
                plan = build_plan(in_rate, out_rate)
                self._plans[key] = plan
            return plan


def _transform_blocks(
    item: waveio.InputFile,
    params: ProcessParams,
    plan_cache: _PlanCache,
    out_channels: int,
):
    """Yield resampled blocks already mapped to the output channel layout.

    Each invocation owns a fresh :class:`StreamingResampler`, so the two
    passes over one file (and different files) never share phase state.
    """
    info = waveio.probe_file(item.path)
    plan = plan_cache.get(info.samplerate, params.sample_rate)
    nominal_frames = plan.expected_frames(info.frames)
    resampler = StreamingResampler(plan)
    for decoded in waveio.read_blocks(item.path, params.block_frames):
        _check_finite(decoded, f"decoding {item.relative}")
        mapped = chan.map_channels(decoded, info.channels, params.channel_mode)
        produced = resampler.process_block(mapped)
        if produced.shape[0]:
            _check_finite(produced, f"resampling {item.relative}")
            yield produced
    tail = resampler.flush()
    if tail.shape[0]:
        _check_finite(tail, f"flushing {item.relative}")
        # Tail zero-padding participates in convolution but is trimmed to the
        # nominal ceil(N*out/in) length and never reaches duration/loudness.
        yield tail[:nominal_frames] if tail.shape[0] > nominal_frames else tail


def process_file(
    item: waveio.InputFile,
    output_dir: Path,
    params: ProcessParams,
    plan_cache: _PlanCache,
) -> FileResult:
    """Run both streaming passes for one file, never raising on bad input."""
    relative = item.relative.as_posix()
    try:
        info = waveio.probe_file(item.path)
        out_channels = chan.output_channels(info.channels, params.channel_mode)
        input_hash = waveio.hash_file(item.path)
        plan = plan_cache.get(info.samplerate, params.sample_rate)
        nominal_frames = plan.expected_frames(info.frames)

        # Pass 1: measure the signal normalization will act on.
        loud = LoudnessAccumulator(params.sample_rate)
        before = StreamingStats()
        output_frames = 0
        for block in _transform_blocks(item, params, plan_cache, out_channels):
            loud.add_block(block)
            before.add_block(block)
            output_frames += block.shape[0]
        if output_frames == 0:
            raise PipelineError("zero-frame input contains no samples")
        if output_frames != nominal_frames:
            raise PipelineError(
                f"resampler produced {output_frames} frames, expected {nominal_frames}"
            )
        measurement = loud.finalize()

        decision = decide_gain(
            measurement.lufs_i,
            params.target_lufs,
            params.peak_ceiling_dbfs,
            before.peak,
        )

        # Pass 2: render with the decided gain, measuring post-normalization.
        after = StreamingStats()
        after_loud = LoudnessAccumulator(params.sample_rate)
        target = waveio.output_path_for(output_dir, item)
        with waveio.AtomicWavWriter(target, params.sample_rate, out_channels) as writer:
            for block in _transform_blocks(item, params, plan_cache, out_channels):
                rendered = block * decision.linear_gain
                _check_finite(rendered, f"rendering {item.relative}")
                after.add_block(rendered)
                after_loud.add_block(rendered)
                writer.write(rendered)
        after_measurement = after_loud.finalize()
        output_hash = waveio.hash_file(target)

        record = {
            "relative_path": relative,
            "input_path": str(item.path),
            "output_path": str(target),
            "input_hash_sha256": input_hash,
            "output_hash_sha256": output_hash,
            "input_sample_rate": info.samplerate,
            "output_sample_rate": params.sample_rate,
            "input_channels": info.channels,
            "output_channels": out_channels,
            "output_frames": output_frames,
            "rms_dbfs_before": before.rms_dbfs(),
            "rms_dbfs_after": after.rms_dbfs(),
            "lufs_i_before": measurement.lufs_i,
            "lufs_i_after": after_measurement.lufs_i,
            "peak_abs_before": before.peak,
            "peak_abs_after": after.peak,
            "clipped_samples_before": before.clipped,
            "clipped_samples_after": after.clipped,
            "gain_db": 20.0 * math.log10(decision.linear_gain),
            "peak_constrained": decision.peak_constrained,
            "short_fallback_before": measurement.short_fallback,
            "short_fallback_after": after_measurement.short_fallback,
            "parameters": {
                "target_lufs": params.target_lufs,
                "peak_ceiling_dbfs": params.peak_ceiling_dbfs,
                "block_frames": params.block_frames,
                "channel_mode": params.channel_mode,
            },
        }
        return FileResult(relative=relative, ok=True, record=record)
    except Exception as exc:  # per-file isolation: batch continues
        target = waveio.output_path_for(output_dir, item)
        target.unlink(missing_ok=True)
        return FileResult(relative=relative, ok=False, error=f"{type(exc).__name__}: {exc}")


def _validate_params(raw: dict) -> ProcessParams:
    params = ProcessParams(
        sample_rate=int(raw["sample_rate"]),
        channel_mode=str(raw["channels"]),
        target_lufs=float(raw["target_lufs"]),
        peak_ceiling_dbfs=float(raw["peak_ceiling_dbfs"]),
        block_frames=int(raw["block_frames"]),
    )
    if params.channel_mode not in chan.MODES:
        raise PipelineError(f"invalid channel mode {params.channel_mode!r}")
    if params.block_frames <= 0:
        raise PipelineError("block-frames must be positive")
    if params.peak_ceiling_dbfs >= 0.0:
        raise PipelineError("peak ceiling must be below 0 dBFS")
    workers = int(raw.get("workers", 1))
    if workers < 1:
        raise PipelineError("workers must be >= 1")
    return params


def run_batch(raw: dict) -> int:
    """CLI entry: scan, process concurrently, write the sorted JSON report."""
    from audioprep.report import build_report, write_report

    # Map argparse dest names onto the pipeline parameter vocabulary.
    raw = {
        **raw,
        "peak_ceiling_dbfs": raw["peak_ceiling"],
        "channels": raw["channels"],
    }
    try:
        params = _validate_params(raw)
    except (PipelineError, ValueError, TypeError) as exc:
        print(f"configuration error: {exc}")
        return 2

    input_dir: Path = raw["input_dir"]
    output_dir: Path = raw["output_dir"]
    report_path: Path = raw["report"]
    workers = int(raw.get("workers", 1))

    try:
        items = waveio.scan_input(input_dir)
    except waveio.AudioInputError as exc:
        print(f"configuration error: {exc}")
        return 2
    if not items:
        print("configuration error: no supported audio files found")
        return 2

    plan_cache = _PlanCache()
    results: list[FileResult] = []
    if workers == 1:
        for item in items:
            results.append(process_file(item, output_dir, params, plan_cache))
    else:
        with ThreadPoolExecutor(max_workers=workers) as pool:
            futures = [
                pool.submit(process_file, item, output_dir, params, plan_cache)
                for item in items
            ]
            results.extend(future.result() for future in futures)

    # Stable path order regardless of thread completion order.
    results.sort(key=lambda r: r.relative)
    report = build_report(params, results)
    write_report(report_path, report)

    ok = [r for r in results if r.ok]
    failed = [r for r in results if not r.ok]
    print(f"files_ok={len(ok)} files_failed={len(failed)}")
    for result in results:
        if result.ok:
            rec = result.record
            print(
                f"OK  {result.relative} "
                f"output_sample_rate={rec['output_sample_rate']} "
                f"output_channels={rec['output_channels']} "
                f"rms_dbfs_before={_fmt(rec['rms_dbfs_before'])} "
                f"rms_dbfs_after={_fmt(rec['rms_dbfs_after'])} "
                f"lufs_i_before={_fmt(rec['lufs_i_before'])} "
                f"lufs_i_after={_fmt(rec['lufs_i_after'])} "
                f"clipped_samples_before={rec['clipped_samples_before']} "
                f"clipped_samples_after={rec['clipped_samples_after']}"
            )
        else:
            print(f"ERR {result.relative} {result.error}")
    return 1 if failed else 0


def _fmt(value: float | None) -> str:
    return "null" if value is None else f"{value:.3f}"
