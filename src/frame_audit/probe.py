"""Read container and video-stream facts before any pixels are decoded.

Everything here is metadata: format name, codec, geometry, time base, declared
frame rate and the container's own duration/frame count.  Those declared values
are reported for comparison only - the audit never substitutes them for the
frames it actually decodes.
"""

from __future__ import annotations

from fractions import Fraction
from pathlib import Path

import av

from .errors import InputError
from .model import StreamInfo

__all__ = ["probe", "VIDEO_EXTENSIONS", "is_probable_video"]


#: Extensions accepted when a directory is scanned in batch mode.  The
#: container is still probed with PyAV; a misnamed file is simply decoded by
#: content, since libavformat sniffs the actual format.
VIDEO_EXTENSIONS: frozenset[str] = frozenset(
    {
        ".mp4",
        ".m4v",
        ".mov",
        ".mkv",
        ".webm",
        ".avi",
        ".ts",
        ".m2ts",
        ".mts",
        ".mpg",
        ".mpeg",
        ".nut",
        ".y4m",
        ".h264",
        ".264",
        ".hevc",
        ".h265",
    }
)


def is_probable_video(path: Path) -> bool:
    """Cheap extension test used to skip non-video files in batch mode."""
    return path.suffix.lower() in VIDEO_EXTENSIONS


def _fraction(value: object) -> Fraction | None:
    if value is None:
        return None
    try:
        result = Fraction(value)
    except (TypeError, ValueError, ZeroDivisionError):
        return None
    return result if result > 0 else None


def _rate(value: object) -> Fraction | None:
    """Return a plausible frame rate, rejecting the sample-rate fallbacks.

    libavformat fills ``average_rate`` with ``1 / time_base`` when a stream has
    no timing information at all (for example a container whose sample table was
    zeroed).  Those values are in the tens of thousands and are not frame rates.
    """
    if value is None:
        return None
    try:
        result = Fraction(value)
    except (TypeError, ValueError, ZeroDivisionError):
        return None
    if result <= 0 or result > 1_000:
        return None
    return result


def _ticks_to_us(ticks: int | None, time_base: Fraction | None) -> int | None:
    if ticks is None or time_base is None:
        return None
    return round(Fraction(ticks) * time_base * 1_000_000)


def probe(path: str | Path) -> StreamInfo:
    """Open ``path`` and return the first video stream's facts.

    Raises :class:`InputError` when the file cannot be opened, has no video
    stream, or reports no usable time base: none of those can produce an
    auditable timeline.
    """
    source = Path(path)
    if not source.exists():
        raise InputError(f"file not found: {source}", reason="not_found")
    if not source.is_file():
        raise InputError(f"not a regular file: {source}", reason="not_a_file")

    try:
        container = av.open(str(source))
    except av.FFmpegError as exc:  # type: ignore[attr-defined]
        raise InputError(
            f"cannot open container: {exc}", reason="open_failed"
        ) from exc
    except OSError as exc:
        raise InputError(f"cannot read file: {exc}", reason="open_failed") from exc

    try:
        if not container.streams.video:
            raise InputError(
                "no video stream in container", reason="no_video_stream"
            )
        stream = container.streams.video[0]
        codec = stream.codec_context
        time_base = _fraction(stream.time_base)
        if time_base is None:
            raise InputError(
                "stream reports no usable time base", reason="invalid_time_base"
            )

        container_duration = _ticks_to_us(container.duration, Fraction(1, 1_000_000))
        stream_duration = _ticks_to_us(stream.duration, time_base)
        start_time = _ticks_to_us(stream.start_time, time_base)
        if start_time is None:
            start_time = _ticks_to_us(container.start_time, Fraction(1, 1_000_000))

        return StreamInfo(
            format_name=container.format.name if container.format.name else "unknown",
            codec_name=codec.name or "unknown",
            width=codec.width or 0,
            height=codec.height or 0,
            time_base=time_base,
            average_rate=_rate(stream.average_rate) or _rate(stream.guessed_rate),
            container_frames=stream.frames if stream.frames else None,
            container_duration_us=container_duration or stream_duration,
            start_time_us=start_time,
        )
    finally:
        container.close()
