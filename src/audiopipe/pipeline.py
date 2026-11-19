"""One-file processing pipeline: read, map channels, resample, normalise, write.

Every stage records what it did so the report can be replayed offline.
"""

from __future__ import annotations

import math
import os
from dataclasses import dataclass, field
from pathlib import Path

import numpy as np

from . import layout, loudness, resample, wavio
from .config import Config
from .errors import AudioPipeError, InputError, ProcessingError
from .resample import BOUNDARY_RULE, Ratio, rational_ratio, resampled_frames

#: Output bit depth; PCM_24 gives enough headroom for normalised programme.
OUTPUT_SUBTYPE = "PCM_24"


@dataclass
class FileResult:
    """JSON-safe record of one input file."""

    input_path: str
    output_path: str | None
    ok: bool
    reason: str | None = None
    message: str | None = None
    params: dict = field(default_factory=dict)
    stats: dict = field(default_factory=dict)
    error: dict | None = None

    @property
    def status(self) -> str:
        return "ok" if self.ok else "failed"

    def as_dict(self) -> dict:
        record = {
            "input": self.input_path,
            "output": self.output_path,
            "status": self.status,
            "ok": self.ok,
            "reason": self.reason,
            "message": self.message,
            "params": self.params,
            "stats": self.stats,
        }
        if self.error is not None:
            record["error"] = self.error
        return record


def output_name(input_relative: Path) -> str:
    """Output file name: keep the stem, use a lower-case .wav extension."""
    return f"{input_relative.stem}.wav"


def process_file(
    input_path: Path,
    output_path: Path,
    relative: str,
    output_relative: str,
    config: Config,
    *,
    max_bytes: int,
) -> FileResult:
    """Process one WAV. Raises only for unexpected bugs; errors are recorded."""
    audio = wavio.read_wav(input_path, max_bytes=max_bytes)
    channels_in = audio.channels
    frames_in = audio.frames
    rate_in = audio.rate
    source = loudness.quick_stats(audio.data)
    # Reject a wrong channel layout before spending time on DSP.
    layout.map_channels(audio.data, config.channels)
    ratio: Ratio = rational_ratio(rate_in, config.target_rate)
    frames_out_expected = resampled_frames(frames_in, rate_in, config.target_rate)

    mapped = layout.map_channels(audio.data, config.channels)
    channels_out = int(mapped.shape[1])

    resampled = resample.resample(
        mapped,
        rate_in,
        config.target_rate,
        block_size=config.block_size,
        config=config.resample,
    )
    if resampled.shape[0] != frames_out_expected:
        raise ProcessingError(
            f"frame accounting mismatch: {resampled.shape[0]} != {frames_out_expected}",
            reason="frame_mismatch",
        )
    # Trim any tail padding back to the true frame count.
    resampled = layout.truncate_tail(resampled, frames_out_expected)

    before = loudness.measure(resampled, config.target_rate)
    decision = None
    gain_db = 0.0
    if config.normalise:
        decision = loudness.normalisation_gain(
            before,
            target_lufs=config.target_lufs,  # type: ignore[arg-type]
            max_peak_dbfs=config.max_peak_dbfs,
        )
        gain_db = decision.gain_db
    elif config.max_peak_dbfs is not None:
        limit_gain = loudness.peak_limit_gain(before, config.max_peak_dbfs)
        if limit_gain is not None:
            gain_db = 20.0 * math.log10(limit_gain)
    if not math.isfinite(gain_db):
        raise ProcessingError("non-finite gain", reason="bad_gain")
    gain = loudness.gain_linear(gain_db)
    after_data = layout.apply_gain(resampled, gain)
    after = loudness.measure(after_data, config.target_rate)

    wavio.write_wav_atomic(output_path, after_data, config.target_rate, subtype=OUTPUT_SUBTYPE)
    _fsync_directory(output_path.parent)

    params = {
        "sample_rate_in": rate_in,
        "sample_rate": config.target_rate,
        "channels_in": channels_in,
        "channels": channels_out,
        "channel_mode": config.channels,
        "channel_note": layout.channel_note(channels_in, config.channels),
        "frames_in": frames_in,
        "frames_out": int(after_data.shape[0]),
        "frames_out_expected": frames_out_expected,
        "ratio_up": ratio.up,
        "ratio_down": ratio.down,
        "ratio": _round(ratio.value, 10),
        "block_size": config.block_size,
        "boundary": BOUNDARY_RULE,
        "kernel": {
            "type": "windowed-sinc-blackman",
            "zero_crossings": config.resample.zero_crossings,
            "rolloff": config.resample.rolloff,
            "cutoff": _round(
                resample.cutoff(rate_in, config.target_rate, config.resample.rolloff), 6
            ),
        },
        "target_lufs": config.target_lufs,
        "max_peak_dbfs": config.max_peak_dbfs,
        "gain_db": _round(gain_db, 6),
        "limited_by_peak": bool(decision.limited_by_peak) if decision else False,
        "gain_reason": decision.reason if decision else "no_gain",
        "output_subtype": OUTPUT_SUBTYPE,
        "duration_in_s": _round(frames_in / rate_in, 9) if rate_in else 0.0,
        "duration_out_s": _round(after_data.shape[0] / config.target_rate, 9),
    }
    stats = {
        "source_rms": _round(source.rms, 9),
        "source_rms_dbfs": _round(source.rms_dbfs, 4),
        "source_peak_dbfs": _round(source.peak_dbfs, 4),
        "source_clipped_samples": int(source.clipped_samples),
        "source_silent": bool(source.silent),
        "rms_before": _round(before.rms, 9),
        "rms_after": _round(after.rms, 9),
        "lufs_before": _round(before.lufs, 4),
        "lufs_after": _round(after.lufs, 4),
        "peak_dbfs_before": _round(before.peak_dbfs, 4),
        "peak_dbfs_after": _round(after.peak_dbfs, 4),
        "clipped_samples_before": int(before.clipped_samples),
        "clipped_samples_after": int(after.clipped_samples),
        "silent_before": bool(before.silent),
        "silent_after": bool(after.silent),
        "loudness_defined": after.lufs is not None,
    }
    reason = None
    message = None
    if after.silent:
        reason = "silent"
        message = "input is silent; loudness is undefined and gain is 0 dB"
    elif decision is not None and decision.limited_by_peak:
        reason = "peak_limited"
        message = "target loudness would exceed the peak ceiling; gain reduced"
    elif before.clipped_samples > 0 and after.clipped_samples == 0:
        reason = "declicked"
        message = "resampler overshoot was removed by the normalisation gain"
    return FileResult(
        input_path=relative,
        output_path=output_relative,
        ok=True,
        reason=reason,
        message=message,
        params=params,
        stats=stats,
    )


def _round(value: float | None, digits: int) -> float | None:
    if value is None or not math.isfinite(value):
        return None
    return round(float(value), digits)


def _fsync_directory(directory: Path) -> None:
    """Best-effort directory fsync so the rename is durable."""
    try:
        fd = os.open(str(directory), os.O_RDONLY)
    except OSError:
        return
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def failure_result(relative: str, exc: BaseException) -> FileResult:
    """Turn an exception into a report record without a machine path."""
    if isinstance(exc, AudioPipeError):
        error = exc.as_dict()
        reason = exc.reason
        message = exc.message
    else:
        error = {"reason": "internal", "message": type(exc).__name__}
        reason = "internal"
        message = type(exc).__name__
    return FileResult(
        input_path=relative,
        output_path=None,
        ok=False,
        reason=reason,
        message=message,
        error=error,
    )
