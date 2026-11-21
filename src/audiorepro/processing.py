"""Per-file processing: decode, resample, gain, and atomic replacement.

Every file is handled independently.  The source is only ever opened for
reading; products are written to a temporary sibling and moved into place
with ``os.replace`` once complete, so an interrupted run never leaves a
half-written WAV behind.
"""

from __future__ import annotations

import math
import os
import tempfile
from pathlib import Path
from typing import Any

import numpy as np
import soundfile as sf

from .discover import InputFile, check_output_relative, sha256_file
from .errors import TaskError
from .loudness import StreamAnalyzer, algorithm_parameters, gain_to_target
from .params import OUTPUT_FORMAT, OUTPUT_SUBTYPE, PipelineParams
from .resample import Resampler

STREAM_BLOCK = 1 << 20


def read_block(handle: sf.SoundFile, start: int, count: int) -> np.ndarray:
    handle.seek(int(start))
    return handle.read(int(count), dtype="float64", always_2d=True)


def analyze_file(path: Path, frames: int, channels: int, rate: int, chunk: int) -> Any:
    with sf.SoundFile(str(path), "r") as handle:
        analyzer = StreamAnalyzer(channels, rate)
        remaining = int(frames)
        while remaining > 0:
            block = handle.read(min(chunk, remaining), dtype="float64", always_2d=True)
            if block.shape[0] == 0:
                break
            analyzer.feed(block)
            remaining -= block.shape[0]
    return analyzer.finish(frames=frames)


def stage_resample(
    source: Path,
    staging: Path,
    item: InputFile,
    params: PipelineParams,
    resampler: Resampler,
) -> tuple[Any, int]:
    """Resample the source into a staging float WAV and measure the result."""
    with sf.SoundFile(str(source), "r") as handle:
        def reader(start: int, count: int) -> np.ndarray:
            return read_block(handle, start, count)

        blocks = resampler.resample_reader(
            reader,
            item.frames,
            item.channels,
            item.sample_rate,
            params.sample_rate,
            chunk_frames=params.chunk_frames,
        )
        analyzer = StreamAnalyzer(item.channels, params.sample_rate)
        frames_out = 0
        with sf.SoundFile(
            str(staging),
            "w",
            samplerate=params.sample_rate,
            channels=item.channels,
            subtype=OUTPUT_SUBTYPE,
            format=OUTPUT_FORMAT,
        ) as out:
            for block in blocks:
                if not np.isfinite(block).all():
                    raise TaskError(
                        item.relative_path, "resample", "non-finite samples produced"
                    )
                analyzer.feed(block)
                out.write(block.astype(np.float32))
                frames_out += block.shape[0]
    return analyzer.finish(frames=frames_out), frames_out


def _read_wav_chunks(path: Path) -> list[tuple[bytes, int, int]] | None:
    """Return [(id, payload_offset, payload_size)] or None if not RIFF/WAVE."""
    try:
        with open(path, "rb") as handle:
            header = handle.read(12)
            if len(header) < 12 or header[:4] != b"RIFF" or header[8:12] != b"WAVE":
                return None
            file_size = path.stat().st_size
            chunks: list[tuple[bytes, int, int]] = []
            offset = 12
            while offset + 8 <= file_size:
                handle.seek(offset)
                chunk_id = handle.read(4)
                size = int.from_bytes(handle.read(4), "little")
                chunks.append((chunk_id, offset + 8, size))
                offset += 8 + size + (size & 1)
    except OSError:
        return None
    return chunks


def strip_timestamp_chunks(path: Path) -> None:
    """Remove libsndfile's non-deterministic PEAK chunk from a WAV.

    FLOAT WAVs gain a PEAK chunk whose position field is a wall-clock
    timestamp, so two identical runs would otherwise differ byte for
    byte.  The audio payload is untouched and the rewrite is streamed in
    fixed-size blocks so memory stays bounded.
    """
    chunks = _read_wav_chunks(path)
    if chunks is None:
        return
    dropped = [c for c in chunks if c[0] in (b"PEAK", b"fact")]
    if not dropped:
        return
    fd, temp_name = tempfile.mkstemp(prefix=f".{path.name}.", suffix=".strip", dir=path.parent)
    temp_path = Path(temp_name)
    try:
        payload_size = 0
        with open(path, "rb") as src, os.fdopen(fd, "wb") as dst:
            dst.write(b"RIFF\x00\x00\x00\x00WAVE")
            for chunk_id, payload_offset, size in chunks:
                if chunk_id in (b"PEAK", b"fact"):
                    continue
                dst.write(chunk_id)
                dst.write(int(size).to_bytes(4, "little"))
                src.seek(payload_offset)
                remaining = size
                while remaining > 0:
                    block = src.read(min(STREAM_BLOCK, remaining))
                    if not block:
                        raise OSError("unexpected end of WAV data")
                    dst.write(block)
                    remaining -= len(block)
                if size & 1:
                    dst.write(b"\x00")
                    payload_size += 1
                payload_size += 8 + size
            dst.seek(4)
            dst.write(int(payload_size + 4).to_bytes(4, "little"))
            dst.flush()
            os.fsync(dst.fileno())
        os.replace(temp_path, path)
        temp_path = None
    finally:
        if temp_path is not None:
            remove_file(temp_path)


def stage_finalize(
    staging: Path,
    target: Path,
    item: InputFile,
    params: PipelineParams,
    gain_db: float,
) -> Any:
    """Apply gain, reject non-finite samples, then atomically install the WAV."""
    gain = 10.0 ** (gain_db / 20.0)
    target.parent.mkdir(parents=True, exist_ok=True)
    fd, temp_name = tempfile.mkstemp(
        prefix=f".{target.name}.", suffix=".tmp", dir=str(target.parent)
    )
    os.close(fd)
    temp_path = Path(temp_name)
    analyzer = StreamAnalyzer(item.channels, params.sample_rate)
    try:
        with sf.SoundFile(str(staging), "r") as src, sf.SoundFile(
            str(temp_path),
            "w",
            samplerate=params.sample_rate,
            channels=item.channels,
            subtype=OUTPUT_SUBTYPE,
            format=OUTPUT_FORMAT,
        ) as out:
            while True:
                block = src.read(params.chunk_frames, dtype="float64", always_2d=True)
                if block.shape[0] == 0:
                    break
                # Quantize exactly what will be written so the reported
                # statistics describe the product byte for byte.
                scaled = (block * gain).astype(np.float32).astype(np.float64)
                if not np.isfinite(scaled).all():
                    raise TaskError(
                        item.relative_path, "normalize", "non-finite samples after gain"
                    )
                analyzer.feed(scaled)
                out.write(scaled.astype(np.float32))
        strip_timestamp_chunks(temp_path)
        os.replace(temp_path, target)
        temp_path = None
    finally:
        if temp_path is not None:
            remove_file(temp_path)
    return analyzer.finish()


def remove_file(path: Path) -> None:
    try:
        path.unlink()
    except OSError:
        pass


def _round(value: float | None) -> float | None:
    if value is None or not math.isfinite(value):
        return None
    return round(value, 6)


def process_file(
    item: InputFile,
    params: PipelineParams,
    resampler: Resampler,
) -> dict[str, Any]:
    """Process one input file end to end and return its report entry."""
    target = params.output_dir / check_output_relative(item.relative_path)
    if target.resolve() == item.source.resolve():
        raise TaskError(item.relative_path, "write", "refusing to overwrite the source file")
    staging: Path | None = None
    try:
        try:
            source_hash = sha256_file(item.source)
        except OSError as exc:
            raise TaskError(item.relative_path, "hash", f"cannot hash source: {exc}") from exc
        try:
            before = analyze_file(
                item.source, item.frames, item.channels, item.sample_rate, params.chunk_frames
            )
        except Exception as exc:
            raise TaskError(item.relative_path, "decode", f"cannot decode source: {exc}") from exc

        fd, staging_name = tempfile.mkstemp(
            prefix=".audiorepro-staging-", suffix=".wav", dir=str(params.output_dir)
        )
        os.close(fd)
        staging = Path(staging_name)
        try:
            staged, frames_out = stage_resample(item.source, staging, item, params, resampler)
        except TaskError:
            raise
        except Exception as exc:
            raise TaskError(item.relative_path, "resample", f"resampling failed: {exc}") from exc

        try:
            gain_db, normalizable = gain_to_target(
                staged.lufs, staged.peak_linear, params.target_lufs, params.peak_dbfs
            )
        except (ValueError, ZeroDivisionError) as exc:
            raise TaskError(item.relative_path, "loudness", str(exc)) from exc

        try:
            after = stage_finalize(staging, target, item, params, gain_db)
        except TaskError:
            raise
        except Exception as exc:
            raise TaskError(item.relative_path, "write", f"cannot write output: {exc}") from exc
        remove_file(staging)
        staging = None

        if after.frames != frames_out:
            remove_file(target)
            raise TaskError(
                item.relative_path,
                "write",
                f"frame count changed while finalizing ({frames_out} -> {after.frames})",
            )
        try:
            output_hash = sha256_file(target)
        except OSError as exc:
            raise TaskError(item.relative_path, "write", f"cannot hash output: {exc}") from exc
        return {
            "relative_path": item.relative_path,
            "output_path": check_output_relative(item.relative_path),
            "format": item.ext_format,
            "source_subtype": item.subtype,
            "frames_in": item.frames,
            "frames_out": after.frames,
            "channels": item.channels,
            "channel_layout": item.channel_layout,
            "input_sample_rate": item.sample_rate,
            "sample_rate": params.sample_rate,
            "source_sha256": source_hash,
            "output_sha256": output_hash,
            "gain_db": round(gain_db, 6),
            "normalizable": bool(normalizable),
            "peak_limited": bool(
                normalizable and gain_db < (params.target_lufs - staged.lufs) - 1e-9
            ),
            "rms_before_dbfs": _round(before.rms_dbfs),
            "lufs_before": _round(before.lufs),
            "peak_before_dbfs": _round(before.peak_dbfs),
            "rms_after_dbfs": _round(after.rms_dbfs),
            "lufs_after": _round(after.lufs),
            "peak_after_dbfs": _round(after.peak_dbfs),
            "clip_samples_before": before.clip_samples,
            "clip_samples_after": after.clip_samples,
            "algorithm": {
                "input_k_weighting": algorithm_parameters(item.sample_rate),
                "output_k_weighting": algorithm_parameters(params.sample_rate),
                "resampler": resampler.cache.get(
                    item.sample_rate, params.sample_rate
                ).as_dict(),
            },
        }
    finally:
        if staging is not None:
            remove_file(staging)
