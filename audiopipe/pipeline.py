"""Decode -> resample -> gain -> encode pipeline coordination."""

from __future__ import annotations

import os
import tempfile
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass
from pathlib import Path

import numpy as np
import soundfile as sf

from .discovery import SourceFile
from .loudness import LoudnessMeter
from .report import PEAK_CEILING_LINEAR, build_record, sha256_file
from .resample import KernelCache, Resampler, expected_output_frames


@dataclass
class BatchParams:
    target_rate: int
    target_lufs: float | None
    chunk_frames: int
    taps: int
    workers: int


def _stream_resample(source: Path, channels: int, source_rate: int,
                     target_rate: int, taps: int, chunk: int,
                     cache: KernelCache, meter: LoudnessMeter
                     ) -> tuple[np.ndarray, int]:
    """First pass: resample to a temp array while collecting stats."""
    rs = Resampler(source_rate, target_rate, channels, taps=taps,
                   cache=cache)
    pieces: list[np.ndarray] = []
    pad_frames = 0
    with sf.SoundFile(str(source)) as handle:
        while True:
            block = handle.read(chunk, dtype="float64", always_2d=True)
            if block.shape[0] == 0:
                break
            if block.shape[1] != channels:
                raise ValueError("channel count changed mid-stream")
            if not np.all(np.isfinite(block)):
                raise ValueError("non-finite sample in input")
            out = rs.process(block)
            if out.shape[0]:
                meter.add(out)
                pieces.append(out)
    tail, pad_frames = rs.flush()
    if tail.shape[0]:
        meter.add(tail)
        pieces.append(tail)
    signal = np.concatenate(pieces, axis=0) if pieces else np.empty(
        (0, channels), dtype=np.float64)
    return signal, pad_frames


def _compute_gain(signal: np.ndarray, meter: LoudnessMeter,
                  target_lufs: float | None) -> dict:
    """Determine linear gain with the -1 dBFS peak ceiling."""
    lufs = meter.integrated_lufs()
    if target_lufs is None:
        return {"linear": 1.0, "db": 0.0, "peak_limited": False,
                "silence_skipped": lufs is None}
    if lufs is None:
        return {"linear": 1.0, "db": 0.0, "peak_limited": False,
                "silence_skipped": True}
    desired = 10.0 ** ((target_lufs - lufs) / 20.0)
    peak = float(np.max(np.abs(signal))) if signal.size else 0.0
    peak_gain = (PEAK_CEILING_LINEAR / peak) if peak > 0.0 else desired
    limited = desired > peak_gain
    gain = min(desired, peak_gain)
    return {"linear": float(gain),
            "db": float(20.0 * np.log10(gain)) if gain > 0 else float("-inf"),
            "peak_limited": bool(limited),
            "silence_skipped": False}


def _atomic_write(path: Path, signal: np.ndarray, rate: int) -> None:
    """Write PCM_24 WAV to a temp file then atomically replace."""
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(
        suffix=".wav", prefix=".tmp-", dir=str(path.parent))
    os.close(fd)
    tmp = Path(tmp_name)
    try:
        sf.write(str(tmp), signal, rate, subtype="PCM_24",
                 format="WAV")
        os.replace(tmp, path)
    except BaseException:
        tmp.unlink(missing_ok=True)
        raise


def process_file(source: SourceFile, params: BatchParams,
                 cache: KernelCache) -> dict:
    """Process one file end to end; never raises (errors are recorded)."""
    relative = source.relative_path
    try:
        source_sha = sha256_file(source.source_path)
        input_frames = _frame_count(source.source_path)
        meter = LoudnessMeter(source.channels, params.target_rate)
        signal, pad_frames = _stream_resample(
            source.source_path, source.channels, source.sample_rate,
            params.target_rate, params.taps, params.chunk_frames,
            cache, meter)
        expected = expected_output_frames(
            input_frames, source.sample_rate, params.target_rate)
        if signal.shape[0] != expected:
            raise ValueError(
                f"output frame count {signal.shape[0]} != contract {expected}")
        before = {"rms": meter.rms(),
                  "lufs": meter.integrated_lufs(),
                  "clipped": meter.clipped()}
        gain = _compute_gain(signal, meter, params.target_lufs)
        scaled = signal * gain["linear"]
        if not np.all(np.isfinite(scaled)):
            raise ValueError("non-finite sample after gain")
        after_meter = LoudnessMeter(source.channels, params.target_rate)
        after_meter.add(scaled)
        after = {"rms": after_meter.rms(),
                 "lufs": after_meter.integrated_lufs(),
                 "clipped": after_meter.clipped()}
        _atomic_write(source.output_path, scaled, params.target_rate)
        output_sha = sha256_file(source.output_path)
        record = build_record(
            source.source_path, relative, source.output_path,
            source_sha,
            {"target_rate": params.target_rate,
             "source_rate": source.sample_rate,
             "target_lufs": params.target_lufs, "taps": params.taps},
            input_frames=input_frames,
            output_frames=signal.shape[0],
            zero_pad_frames=pad_frames, before=before, after=after,
            gain=gain, channels=source.channels)
        record["output_sha256"] = output_sha
        return record
    except Exception as exc:
        return {"source": str(relative), "status": "error",
                "error": f"{type(exc).__name__}: {exc}"}


def _frame_count(path: Path) -> int:
    return int(sf.info(str(path)).frames)


def run_batch(sources: list[SourceFile], pre_errors: list[dict],
              params: BatchParams) -> list[dict]:
    """Process all sources concurrently, preserving input order."""
    cache = KernelCache()
    results: list[dict | None] = [None] * len(sources)
    with ThreadPoolExecutor(max_workers=max(1, params.workers)) as pool:
        future_map = {
            pool.submit(process_file, src, params, cache): index
            for index, src in enumerate(sources)}
        for future in as_completed(future_map):
            results[future_map[future]] = future.result()
    records = [record for record in results if record is not None]
    return pre_errors + records
