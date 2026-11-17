"""FFmpeg subprocess management and temp-output handling.

FFmpeg is always invoked with an argument array (never a shell).  Each attempt
writes to a uniquely named temporary file next to its final destination; the
temp file is only promoted after the caller validates it, so a crashed or
interrupted run can never leave a half-written result that looks complete.
"""

from __future__ import annotations

import os
import re
import shutil
import subprocess
import time
import uuid
from dataclasses import dataclass
from pathlib import Path

from .errors import CancelledError, EncoderUnavailableError, TaskError
from .plan import JobSpec, Resolution
from .synth import SourceClip

_FFMPEG_VERSION_PATTERN = re.compile(r"ffmpeg version (\S+)")


@dataclass(frozen=True)
class EncoderInfo:
    ffmpeg_path: str
    ffprobe_path: str
    version: str

    @property
    def version_key(self) -> str:
        """Version string used in task keys (stable across patch builds)."""
        return self.version


@dataclass(frozen=True)
class EncodeOutcome:
    output_path: Path
    temp_path: Path
    stderr_tail: str


def _probe_binary(name: str) -> str:
    found = shutil.which(name)
    if not found:
        raise EncoderUnavailableError(
            f"{name} not found on PATH; install it with: brew install ffmpeg"
        )
    return found


def detect_encoder() -> EncoderInfo:
    ffmpeg_path = _probe_binary("ffmpeg")
    ffprobe_path = _probe_binary("ffprobe")
    try:
        proc = subprocess.run(
            [ffmpeg_path, "-version"],
            capture_output=True,
            text=True,
            timeout=30,
            shell=False,
            check=False,
        )
    except (OSError, subprocess.SubprocessError) as exc:
        raise EncoderUnavailableError(f"could not run ffmpeg: {exc}") from exc
    if proc.returncode != 0:
        raise EncoderUnavailableError(
            f"ffmpeg -version failed with exit {proc.returncode}"
        )
    first_line = proc.stdout.splitlines()[0] if proc.stdout else ""
    match = _FFMPEG_VERSION_PATTERN.search(first_line)
    if not match:
        raise EncoderUnavailableError("could not parse ffmpeg version")
    version = match.group(1)

    try:
        encoders = subprocess.run(
            [ffmpeg_path, "-hide_banner", "-encoders"],
            capture_output=True,
            text=True,
            timeout=30,
            shell=False,
            check=False,
        )
    except (OSError, subprocess.SubprocessError) as exc:
        raise EncoderUnavailableError(f"could not list ffmpeg encoders: {exc}") from exc
    if "libx264" not in (encoders.stdout or ""):
        raise EncoderUnavailableError(
            "ffmpeg build lacks libx264; reinstall with brew install ffmpeg"
        )
    return EncoderInfo(ffmpeg_path=ffmpeg_path, ffprobe_path=ffprobe_path, version=version)


def build_encode_args(
    encoder: EncoderInfo,
    clip: SourceClip,
    job: JobSpec,
    *,
    destination: Path,
    keyint: int = 24,
) -> list[str]:
    resolution: Resolution = clip.resolution
    bitrate = f"{job.bitrate_kbps}k"
    return [
        encoder.ffmpeg_path,
        "-hide_banner",
        "-nostdin",
        "-loglevel",
        "error",
        "-y",
        "-f",
        "rawvideo",
        "-pix_fmt",
        "yuv420p",
        "-s",
        f"{resolution.width}x{resolution.height}",
        "-r",
        str(clip.fps),
        "-i",
        str(clip.path),
        "-frames:v",
        str(clip.frames),
        "-c:v",
        "libx264",
        "-preset",
        job.preset,
        "-b:v",
        bitrate,
        "-maxrate",
        bitrate,
        "-bufsize",
        f"{job.bitrate_kbps * 2}k",
        "-g",
        str(keyint),
        "-x264-params",
        "scenecut=0:open-gop=0",
        "-pix_fmt",
        "yuv420p",
        "-movflags",
        "+faststart",
        "-f",
        "mp4",
        str(destination),
    ]


def temp_path_for(output_path: Path, worker_id: str | None = None) -> Path:
    output_path.parent.mkdir(parents=True, exist_ok=True)
    token = uuid.uuid4().hex[:12]
    owner = re.sub(r"[^A-Za-z0-9_-]", "_", worker_id or "job")
    return output_path.with_name(f".{output_path.stem}.{owner}.{token}.partial.mp4")


def _drain(stream) -> str:
    if stream is None:
        return ""
    try:
        raw = stream.read()
    except (OSError, ValueError):
        return ""
    if isinstance(raw, bytes):
        return raw.decode("utf-8", "replace")
    return raw or ""


def encode(
    encoder: EncoderInfo,
    clip: SourceClip,
    job: JobSpec,
    *,
    output_path: Path,
    worker_id: str | None = None,
    timeout: float = 300.0,
    cancel_check=None,
) -> EncodeOutcome:
    """Encode one job to a temp file; promote it to ``output_path`` on success.

    FFmpeg runs under :class:`subprocess.Popen` and is polled so that a pause or
    SIGINT can stop it promptly; the partial file is then removed.
    """
    output_path.parent.mkdir(parents=True, exist_ok=True)
    temp_path = temp_path_for(output_path, worker_id)
    args = build_encode_args(encoder, clip, job, destination=temp_path)
    try:
        proc = subprocess.Popen(
            args,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
            shell=False,
        )
    except OSError as exc:
        _unlink_quiet(temp_path)
        raise TaskError(f"could not start ffmpeg: {exc}") from exc

    deadline = time.monotonic() + timeout
    cancelled = False
    timed_out = False
    while proc.poll() is None:
        if cancel_check is not None and cancel_check():
            cancelled = True
            _terminate(proc)
            break
        if time.monotonic() >= deadline:
            timed_out = True
            _terminate(proc)
            break
        time.sleep(0.05)

    stderr_tail = _drain(proc.stderr).strip()[-800:]
    if proc.stderr is not None:
        proc.stderr.close()
    returncode = proc.wait()

    if cancelled:
        _unlink_quiet(temp_path)
        raise CancelledError("cancelled")
    if timed_out:
        _unlink_quiet(temp_path)
        raise TaskError(f"ffmpeg timed out after {timeout:.0f}s")
    if returncode != 0:
        _unlink_quiet(temp_path)
        detail = stderr_tail or f"exit status {returncode}"
        raise TaskError(f"ffmpeg failed: {detail}")
    if not temp_path.exists() or temp_path.stat().st_size == 0:
        _unlink_quiet(temp_path)
        raise TaskError("ffmpeg produced no output")

    os.replace(temp_path, output_path)
    return EncodeOutcome(output_path=output_path, temp_path=temp_path, stderr_tail=stderr_tail)


def _terminate(proc: subprocess.Popen) -> None:
    try:
        proc.terminate()
        proc.wait(timeout=5)
    except subprocess.TimeoutExpired:
        proc.kill()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            pass
    except OSError:
        pass


def _unlink_quiet(path: Path) -> None:
    try:
        path.unlink()
    except OSError:
        pass


def cleanup_stale_partials(directory: Path) -> int:
    """Remove abandoned temp outputs left behind by an interrupted run."""
    removed = 0
    if not directory.exists():
        return 0
    for candidate in directory.glob(".*.partial.mp4"):
        _unlink_quiet(candidate)
        removed += 1
    return removed
