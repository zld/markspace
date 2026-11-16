"""Encode one candidate via FFmpeg and validate the artifact.

FFmpeg/ffprobe are always invoked as argv lists (never a shell string).
The encoded file is probed for a real video stream and positive duration,
then hashed so later runs can verify integrity and reuse identical
artifacts.
"""

from __future__ import annotations

import json
import subprocess
from dataclasses import dataclass
from pathlib import Path

from .store import sha256_file

ENCODE_TIMEOUT_S = 600
PROBE_TIMEOUT_S = 60


class EncodeError(Exception):
    """FFmpeg run failed or produced an invalid artifact."""


class ProbeError(Exception):
    """Input cannot be probed or fails source validation."""


@dataclass(frozen=True)
class ProbeInfo:
    duration_s: float
    width: int
    height: int
    n_frames: int
    codec: str


def _run(argv: list[str], timeout: int) -> subprocess.CompletedProcess:
    try:
        return subprocess.run(
            argv, capture_output=True, text=True, timeout=timeout, check=False
        )
    except FileNotFoundError as exc:
        raise ProbeError(f"tool not found: {argv[0]}") from exc
    except subprocess.TimeoutExpired as exc:
        raise ProbeError(f"timeout running {argv[0]}") from exc


def ffmpeg_version() -> str:
    proc = _run(["ffmpeg", "-hide_banner", "-version"], PROBE_TIMEOUT_S)
    first = (proc.stdout.splitlines() or ["ffmpeg unknown"])[0]
    return first.strip()


def probe(path: str | Path) -> ProbeInfo:
    argv = [
        "ffprobe", "-v", "error", "-select_streams", "v:0",
        "-show_entries", "stream=width,height,nb_frames,codec_name:format=duration",
        "-of", "json", str(path),
    ]
    proc = _run(argv, PROBE_TIMEOUT_S)
    if proc.returncode != 0:
        raise ProbeError(f"ffprobe failed: {proc.stderr.strip()[:200]}")
    try:
        data = json.loads(proc.stdout)
        stream = data["streams"][0]
        duration = float(data["format"]["duration"])
    except (KeyError, IndexError, TypeError, ValueError) as exc:
        raise ProbeError(f"no video stream or duration in {path}") from exc
    try:
        n_frames = int(stream.get("nb_frames") or 0)
    except (TypeError, ValueError):
        n_frames = 0
    return ProbeInfo(
        duration_s=duration,
        width=int(stream.get("width") or 0),
        height=int(stream.get("height") or 0),
        n_frames=n_frames,
        codec=str(stream.get("codec_name") or ""),
    )


def validate_source(path: str | Path) -> ProbeInfo:
    """Reject files without a video stream, zero duration, or undecodable."""
    info = probe(path)
    if not info.codec or info.width <= 0 or info.height <= 0:
        raise ProbeError("no decodable video stream")
    if info.duration_s <= 0:
        raise ProbeError("zero or unknown duration")
    argv = ["ffmpeg", "-v", "error", "-i", str(path), "-t", "0.5", "-f", "null", "-"]
    proc = _run(argv, PROBE_TIMEOUT_S)
    if proc.returncode != 0:
        raise ProbeError(f"cannot decode: {proc.stderr.strip()[:200]}")
    return info


def encode(src: str | Path, task, out_path: str | Path) -> dict:
    """Encode one planned task; return artifact metadata for the store."""
    out_path = Path(out_path)
    out_path.parent.mkdir(parents=True, exist_ok=True)
    argv = [
        "ffmpeg", "-y", "-hide_banner", "-v", "error",
        "-i", str(src),
        "-vf", f"scale={task.width}:{task.height}",
        "-c:v", "libx264", "-preset", task.preset,
        "-b:v", f"{task.target_kbps}k",
        "-pix_fmt", "yuv420p",
        "-movflags", "+faststart",
        str(out_path),
    ]
    try:
        proc = subprocess.run(
            argv, capture_output=True, text=True, timeout=ENCODE_TIMEOUT_S, check=False
        )
    except subprocess.TimeoutExpired:
        out_path.unlink(missing_ok=True)
        raise EncodeError(f"ffmpeg timed out after {ENCODE_TIMEOUT_S}s") from None
    if proc.returncode != 0:
        out_path.unlink(missing_ok=True)
        raise EncodeError(f"ffmpeg exited {proc.returncode}: {proc.stderr.strip()[:300]}")
    if not out_path.is_file() or out_path.stat().st_size == 0:
        raise EncodeError("ffmpeg produced no output file")
    try:
        info = probe(out_path)
    except ProbeError as exc:
        out_path.unlink(missing_ok=True)
        raise EncodeError(f"output failed validation: {exc}") from exc
    size = out_path.stat().st_size
    actual_kbps = size * 8 / info.duration_s / 1000.0
    return {
        "actual_kbps": actual_kbps,
        "duration_s": info.duration_s,
        "file_hash": sha256_file(out_path),
        "file_path": str(out_path),
        "encoder_version": ffmpeg_version(),
        "params": {
            "key": task.params_key,
            "argv": argv,
            "width": task.width,
            "height": task.height,
            "target_kbps": task.target_kbps,
            "preset": task.preset,
        },
    }
