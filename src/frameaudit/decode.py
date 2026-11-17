"""PyAV decoding layer.

Only lightweight per-frame luma samples leave this module: full image planes
are released as soon as the sample is taken, so the pipeline never caches an
entire clip in memory.
"""

from __future__ import annotations

from dataclasses import dataclass
from fractions import Fraction
from pathlib import Path
from typing import Iterator

import av
import numpy as np

from .errors import (
    CodecNotSupportedError,
    MediaError,
    NoVideoStreamError,
)

# Side length of the square luma sample kept per decoded frame.
SAMPLE_GRID = 8


@dataclass(slots=True)
class DecodedFrame:
    """One decoded frame's metadata plus a small luma sample."""

    decode_index: int
    pts: int | None
    dts: int | None
    time_base: Fraction
    keyframe: bool
    pict_type: str  # 'I', 'P', 'B' or '?'
    width: int
    height: int
    luma: np.ndarray  # (SAMPLE_GRID, SAMPLE_GRID) float32, 0..255


@dataclass(slots=True)
class StreamInfo:
    """Container-level facts about the selected video stream."""

    stream_index: int
    codec_name: str
    width: int
    height: int
    time_base: Fraction | None
    average_rate: Fraction | None


def _fraction(value: object) -> Fraction | None:
    if value is None:
        return None
    return Fraction(value.numerator, value.denominator)


def open_video(
    path: str | Path, stream_index: int
) -> tuple[av.container.InputContainer, av.video.stream.VideoStream, StreamInfo]:
    """Open *path* and select the video stream at *stream_index*.

    Raises:
        NoVideoStreamError: the container holds no video streams.
        UsageError: *stream_index* is outside the available range.
        CodecNotSupportedError: no decoder is available for the codec.
        MediaError: any other open failure.
    """
    # Imported here to avoid an import cycle (errors <- cli <- __main__).
    from .errors import UsageError

    path = Path(path)
    try:
        container = av.open(str(path))
    except FileNotFoundError as exc:
        raise MediaError(f"input file not found: {path}") from exc
    except av.FFmpegError as exc:
        raise MediaError(f"cannot open {path}: {exc.strerror or exc}") from exc

    video_streams = container.streams.video
    if not video_streams:
        container.close()
        raise NoVideoStreamError(f"no video stream found in {path}")
    if stream_index < 0 or stream_index >= len(video_streams):
        container.close()
        raise UsageError(
            f"video stream index {stream_index} is out of range "
            f"(0..{len(video_streams) - 1}) for {path}"
        )

    stream = video_streams[stream_index]
    codec_name = stream.codec_context.name
    # An unknown codec name means FFmpeg has no decoder registered for it.
    if not codec_name:
        container.close()
        raise CodecNotSupportedError(
            f"unsupported or unknown codec in stream {stream_index} of {path}"
        )

    info = StreamInfo(
        stream_index=stream_index,
        codec_name=codec_name,
        width=stream.codec_context.width or 0,
        height=stream.codec_context.height or 0,
        time_base=_fraction(stream.time_base),
        average_rate=_fraction(stream.average_rate),
    )
    return container, stream, info


def _luma_sample(frame: av.video.frame.VideoFrame) -> np.ndarray:
    """Extract an evenly spaced SAMPLE_GRID x SAMPLE_GRID luma sample."""
    gray = frame.to_ndarray(format="gray")  # (H, W) uint8, format conversion
    rows = np.linspace(0, gray.shape[0] - 1, SAMPLE_GRID).astype(int)
    cols = np.linspace(0, gray.shape[1] - 1, SAMPLE_GRID).astype(int)
    return gray[rows][:, cols].astype(np.float32, copy=True)


def _pict_type(frame: av.video.frame.VideoFrame) -> str:
    # PyAV may expose either a PictureType enum or a bare integer; normalise
    # through the enum so frame-type counts stay reliable across builds.
    try:
        name = av.video.frame.PictureType(frame.pict_type).name
    except ValueError:
        name = ""
    if name in ("I", "P", "B"):
        return name
    if name in ("S", "SI", "SP", "BI"):
        # Non-IDR sync and mixed macroblock types map to their base type.
        return name[0] if name[0] in ("I", "P", "B") else "?"
    if frame.key_frame:
        return "I"
    return "?"


def iter_decoded_frames(
    container: av.container.InputContainer,
    stream: av.video.stream.VideoStream,
) -> Iterator[DecodedFrame]:
    """Yield decoded frames in decode (DTS) order."""
    decode_index = 0
    try:
        for frame in container.decode(stream):
            frame_tb = _fraction(frame.time_base) or _fraction(stream.time_base)
            if frame_tb is None:
                frame_tb = Fraction(1, 1)
            yield DecodedFrame(
                decode_index=decode_index,
                pts=frame.pts,
                dts=frame.dts,
                time_base=frame_tb,
                keyframe=bool(frame.key_frame),
                pict_type=_pict_type(frame),
                width=frame.width,
                height=frame.height,
                luma=_luma_sample(frame),
            )
            decode_index += 1
    except av.FFmpegError as exc:
        # Decoder-not-found / unsupported codec errors carry distinct errnos;
        # surface them as CodecNotSupportedError, everything else as media.
        message = exc.strerror or str(exc)
        if "decoder" in message.lower() or "codec" in message.lower():
            raise CodecNotSupportedError(
                f"unsupported codec in stream {stream.index}: {message}"
            ) from exc
        raise MediaError(
            f"decode failure in stream {stream.index}: {message}"
        ) from exc
    finally:
        container.close()
