"""Decode video frames with PyAV and emit raw :class:`FrameRecord` objects.

Only the first video stream is consumed; audio and other streams are ignored.
This stage performs no reordering and no timestamp repair: the records reflect
the order in which the decoder produced frames, which for B-frame content
differs from presentation order.
"""

from __future__ import annotations

import os
from fractions import Fraction
from pathlib import Path
from typing import Iterator, Optional

import av

from .errors import DecodeError, InputError, LimitExceededError
from .model import (
    FRAME_TYPE_B,
    FRAME_TYPE_I,
    FRAME_TYPE_OTHER,
    FRAME_TYPE_P,
    FrameRecord,
)

# Default hard cap on per-frame records; protects downstream memory usage.
DEFAULT_MAX_FRAMES = 1_000_000

_PICT_TYPE_MAP = {
    "I": FRAME_TYPE_I,
    "P": FRAME_TYPE_P,
    "B": FRAME_TYPE_B,
}


def classify_frame(pict_type: object) -> str:
    """Map a PyAV ``PictureType`` onto I/P/B/OTHER labels."""
    name = getattr(pict_type, "name", None)
    if name in _PICT_TYPE_MAP:
        return _PICT_TYPE_MAP[name]
    return FRAME_TYPE_OTHER


class VideoInput:
    """Context manager wrapping an open PyAV input container.

    The container is opened eagerly so that missing files and unreadable
    inputs raise :class:`InputError` before any frame is requested.
    """

    def __init__(self, path: str | os.PathLike[str]) -> None:
        self.path = Path(path)
        if not self.path.is_file():
            raise InputError(f"input file does not exist: {self.path}")
        try:
            self._container = av.open(str(self.path), mode="r")
        except av.FFmpegError as exc:
            raise InputError(f"cannot open input {self.path}: {exc}") from exc
        except OSError as exc:
            raise InputError(f"cannot open input {self.path}: {exc}") from exc

        video_streams = self._container.streams.video
        if not video_streams:
            self._container.close()
            raise InputError(
                f"no video stream found in {self.path}; audio-only or "
                "non-video inputs are not supported"
            )
        self.stream = video_streams[0]

    @property
    def container(self) -> av.container.InputContainer:
        return self._container

    @property
    def metadata(self) -> dict[str, Optional[str]]:
        stream = self.stream
        return {
            "container_format": self._container.format.name
            if self._container.format
            else None,
            "video_codec": stream.codec_context.name,
            "time_base": str(stream.time_base) if stream.time_base else None,
            "average_rate": str(stream.average_rate)
            if stream.average_rate
            else None,
        }

    def iter_frames(
        self, max_frames: int = DEFAULT_MAX_FRAMES
    ) -> Iterator[FrameRecord]:
        """Yield decoded frames in decoder order up to ``max_frames``."""
        if max_frames <= 0:
            raise LimitExceededError(
                f"max_frames must be positive, got {max_frames}"
            )
        index = 0
        try:
            for frame in self._container.decode(self.stream):
                if index >= max_frames:
                    raise LimitExceededError(
                        "frame record limit of "
                        f"{max_frames} exceeded while decoding {self.path}; "
                        "raise --max-frames if this input is expected"
                    )
                time_base: Optional[Fraction] = frame.time_base
                if time_base is None:
                    time_base = self.stream.time_base
                yield FrameRecord(
                    decode_index=index,
                    frame_type=classify_frame(frame.pict_type),
                    pts=frame.pts,
                    time_base=time_base,
                    key_frame=bool(frame.key_frame),
                )
                index += 1
        except LimitExceededError:
            raise
        except av.FFmpegError as exc:
            raise DecodeError(
                f"decoding failed for {self.path} after {index} frames: {exc}"
            ) from exc

        if index == 0:
            raise InputError(
                f"no decodable video frames found in {self.path}"
            )

    def close(self) -> None:
        self._container.close()

    def __enter__(self) -> "VideoInput":
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()


def decode_frames(
    path: str | os.PathLike[str],
    max_frames: int = DEFAULT_MAX_FRAMES,
) -> tuple[list[FrameRecord], dict[str, Optional[str]]]:
    """Decode every frame and return records plus stream metadata."""
    with VideoInput(path) as video:
        metadata = video.metadata
        records = list(video.iter_frames(max_frames=max_frames))
    return records, metadata
