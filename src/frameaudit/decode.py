"""Decode video frames with PyAV and emit raw :class:`FrameRecord` objects.

Only the first video stream is consumed; audio and other streams are ignored.

FFmpeg's modern decode API (``avcodec_send_packet`` /
``avcodec_receive_frame``) always surfaces frames in presentation order, even
for B-frame content where packets arrive in decode (DTS) order.  To preserve
the real decoder sequence this module walks packets in demux order, assigns
each data packet an incrementing ``decode_index``, and then pairs each
decoded frame back to its originating packet by PTS.  Frames are therefore
emitted in true decode order, with missing/duplicate PTS handled via FIFO
queues so no frame is silently dropped.
"""

from __future__ import annotations

import os
from collections import defaultdict, deque
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
        """Yield decoded frames in true decoder (DTS/demux) order.

        The decoder emits frames in presentation order with a delay, while
        packets arrive in coded order.  We record each packet's coded index,
        pair every decoded frame to its originating packet, and only then
        re-emit the records sorted by coded index so downstream reordering
        has genuine decode-order input to work with.
        """
        if max_frames <= 0:
            raise LimitExceededError(
                f"max_frames must be positive, got {max_frames}"
            )
        codec = self.stream.codec_context
        # PTS -> FIFO of coded indices; FIFO handles duplicate PTS and the
        # separate ``None`` queue preserves missing-PTS frames.
        pts_queues: dict[Optional[int], deque[int]] = defaultdict(deque)
        decoded: list[FrameRecord] = []
        next_index = 0

        def assign_index(packet: av.Packet) -> None:
            nonlocal next_index
            if next_index >= max_frames:
                raise LimitExceededError(
                    "frame record limit of "
                    f"{max_frames} exceeded while decoding {self.path}; "
                    "raise --max-frames if this input is expected"
                )
            pts_queues[packet.pts].append(next_index)
            next_index += 1

        def pair(frame: av.VideoFrame) -> FrameRecord:
            queue = pts_queues.get(frame.pts)
            if queue:
                decode_index = queue.popleft()
            elif pts_queues.get(None):
                # Fallback for containers that synthesize a frame PTS even
                # though the originating packet had none.
                decode_index = pts_queues[None].popleft()
            else:
                raise DecodeError(
                    f"could not pair decoded frame pts={frame.pts} with a "
                    "source packet; stream metadata is inconsistent"
                )
            time_base: Optional[Fraction] = frame.time_base or self.stream.time_base
            return FrameRecord(
                decode_index=decode_index,
                frame_type=classify_frame(frame.pict_type),
                pts=frame.pts,
                time_base=time_base,
                key_frame=bool(frame.key_frame),
            )

        try:
            for packet in self._container.demux(self.stream):
                if packet.size == 0:
                    # End-of-stream flush marker: drain buffered frames.
                    decoded.extend(codec.decode(None))
                    continue
                assign_index(packet)
                decoded.extend(codec.decode(packet))
        except LimitExceededError:
            raise
        except av.FFmpegError as exc:
            raise DecodeError(
                f"decoding failed for {self.path} after {next_index} "
                f"packets: {exc}"
            ) from exc

        if not decoded:
            raise InputError(
                f"no decodable video frames found in {self.path}"
            )

        records = [pair(frame) for frame in decoded]
        records.sort(key=lambda record: record.decode_index)
        for record in records:
            yield record

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
