"""Measurement: real bitrate from file bytes, and PSNR from decoded output.

The encoded file is probed with ffprobe for duration and stream geometry, then
decoded back to raw yuv420p with ffmpeg.  PSNR is computed over luma planes
against the matching reference clip.  Both numbers are validated before they
are allowed anywhere near the database: bitrate is an integer and PSNR must be
finite.
"""

from __future__ import annotations

import json
import math
import subprocess
from dataclasses import dataclass
from pathlib import Path

import numpy as np

from .encode import EncoderInfo
from .errors import TaskError
from .plan import Resolution

# Full-scale error for 8-bit luma.
PEAK = 255.0


@dataclass(frozen=True)
class ProbeResult:
    duration_s: float
    width: int
    height: int
    nb_frames: int | None


@dataclass(frozen=True)
class Measurement:
    actual_bps: int
    quality_psnr_db: float
    encoded_bytes: int
    duration_s: float


def _invoke(
    args: list[str], timeout: float, what: str, *, text: bool
) -> subprocess.CompletedProcess:
    try:
        return subprocess.run(
            args,
            capture_output=True,
            text=text,
            timeout=timeout,
            shell=False,
            check=False,
        )
    except subprocess.TimeoutExpired as exc:
        raise TaskError(f"{what} timed out after {timeout:.0f}s") from exc
    except OSError as exc:
        raise TaskError(f"could not start {what}: {exc}") from exc


def _run(args: list[str], timeout: float, what: str) -> subprocess.CompletedProcess:
    proc = _invoke(args, timeout, what, text=True)
    if proc.returncode != 0:
        detail = (proc.stderr or "").strip()[-600:] or f"exit {proc.returncode}"
        raise TaskError(f"{what} failed: {detail}")
    return proc


def probe(encoder: EncoderInfo, path: Path, resolution: Resolution) -> ProbeResult:
    if not path.exists():
        raise TaskError(f"encoded output missing: {path}")
    if path.stat().st_size == 0:
        raise TaskError(f"encoded output is empty: {path}")

    proc = _run(
        [
            encoder.ffprobe_path,
            "-v",
            "error",
            "-select_streams",
            "v:0",
            "-show_entries",
            "stream=width,height,nb_frames:format=duration,size",
            "-of",
            "json",
            str(path),
        ],
        timeout=60,
        what="ffprobe",
    )
    try:
        payload = json.loads(proc.stdout)
    except json.JSONDecodeError as exc:
        raise TaskError("ffprobe returned unparseable JSON") from exc

    streams = payload.get("streams") or []
    if not streams:
        raise TaskError("encoded output has no video stream")
    stream = streams[0]
    fmt = payload.get("format") or {}

    try:
        width = int(stream["width"])
        height = int(stream["height"])
        duration = float(fmt["duration"])
    except (KeyError, TypeError, ValueError) as exc:
        raise TaskError("ffprobe output is missing width/height/duration") from exc

    if width != resolution.width or height != resolution.height:
        raise TaskError(
            f"decoded geometry {width}x{height} does not match "
            f"{resolution.width}x{resolution.height}"
        )
    if not math.isfinite(duration) or duration <= 0:
        raise TaskError(f"invalid duration {duration!r}")

    nb_frames_raw = stream.get("nb_frames")
    nb_frames: int | None = None
    if nb_frames_raw not in (None, "N/A"):
        try:
            nb_frames = int(nb_frames_raw)
        except ValueError:
            nb_frames = None
    return ProbeResult(duration, width, height, nb_frames)


def decode_luma(
    encoder: EncoderInfo,
    path: Path,
    resolution: Resolution,
    *,
    frames: int,
    timeout: float = 120.0,
) -> np.ndarray:
    """Decode the video to yuv420p and return luma frames ``(frames, H, W)``."""
    proc = _invoke(
        [
            encoder.ffmpeg_path,
            "-hide_banner",
            "-nostdin",
            "-loglevel",
            "error",
            "-i",
            str(path),
            "-frames:v",
            str(frames),
            "-f",
            "rawvideo",
            "-pix_fmt",
            "yuv420p",
            "-",
        ],
        timeout,
        "ffmpeg decode",
        text=False,
    )
    if proc.returncode != 0:
        detail = (proc.stderr or b"").decode("utf-8", "replace").strip()[-600:]
        raise TaskError(f"ffmpeg decode failed: {detail or proc.returncode}")
    width, height = resolution.width, resolution.height
    frame_bytes = width * height * 3 // 2
    data = proc.stdout or b""
    if len(data) < frame_bytes * frames:
        raise TaskError(
            f"decoded {len(data)} bytes, expected {frame_bytes * frames}"
        )
    usable = np.frombuffer(data[: frame_bytes * frames], dtype=np.uint8)
    y_size = width * height
    frames_out = usable.reshape(frames, frame_bytes)[:, :y_size]
    return frames_out.reshape(frames, height, width)


def compute_psnr(reference: np.ndarray, decoded: np.ndarray) -> float:
    if reference.shape != decoded.shape:
        raise TaskError(
            f"PSNR shape mismatch: reference {reference.shape} vs decoded "
            f"{decoded.shape}"
        )
    diff = reference.astype(np.float64) - decoded.astype(np.float64)
    mse = float(np.mean(diff * diff))
    if mse <= 0.0:
        # Bit-exact match: report a high but finite score rather than inf.
        return 100.0
    psnr = 10.0 * math.log10((PEAK * PEAK) / mse)
    if not math.isfinite(psnr):
        raise TaskError(f"PSNR is not finite: {psnr!r}")
    return psnr


def measure(
    encoder: EncoderInfo,
    path: Path,
    resolution: Resolution,
    *,
    frames: int,
    reference_luma: np.ndarray,
    timeout: float = 120.0,
) -> Measurement:
    probed = probe(encoder, path, resolution)
    if probed.nb_frames is not None and probed.nb_frames < frames:
        raise TaskError(
            f"encoded output has {probed.nb_frames} frames, expected {frames}"
        )
    decoded = decode_luma(encoder, path, resolution, frames=frames, timeout=timeout)
    psnr = compute_psnr(reference_luma[:frames], decoded[:frames])
    encoded_bytes = path.stat().st_size
    actual_bps = int(round(encoded_bytes * 8 / probed.duration_s))
    if actual_bps <= 0:
        raise TaskError(f"computed non-positive bitrate {actual_bps}")
    return Measurement(
        actual_bps=actual_bps,
        quality_psnr_db=psnr,
        encoded_bytes=encoded_bytes,
        duration_s=probed.duration_s,
    )
