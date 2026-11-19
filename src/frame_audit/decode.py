"""Sequential decoding: frame type, PTS and time base, one frame at a time.

The decoder is driven packet by packet rather than through the high-level
``container.decode`` helper, for three reasons:

* a corrupt packet can be recorded and stepped over instead of aborting the file;
* ``--max-frames`` can stop pulling packets, which stops the memory growth at one
  frame plus the decoder's own reference frames instead of buffering a video;
* packet timestamps stay visible, so a missing PTS is distinguishable from a
  timestamp the decoder invented.

Only integers and enum values are retained; pixel planes are never copied out.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from fractions import Fraction
from pathlib import Path

import av

from .errors import DecodeError, InputError
from .model import FrameRecord, StreamInfo
from .probe import probe

__all__ = ["DecodeOutcome", "decode_frames"]


@dataclass(slots=True)
class DecodeOutcome:
    """What one sequential decode produced."""

    frames: list[FrameRecord] = field(default_factory=list)
    truncated: bool = False
    decode_errors: list[str] = field(default_factory=list)
    empty_packets: int = 0

    @property
    def count(self) -> int:
        return len(self.frames)


def _pict_type(frame: object) -> str:
    value = getattr(frame, "pict_type", None)
    name = getattr(value, "name", None)
    if isinstance(name, str) and name:
        return name
    text = str(value)
    return text if text else "?"


def _err_text(exc: BaseException) -> str:
    message = str(exc).strip()
    return message.splitlines()[0] if message else exc.__class__.__name__


def decode_frames(
    path: str | Path,
    *,
    time_base: Fraction | None = None,
    max_frames: int | None = None,
    info: StreamInfo | None = None,
) -> DecodeOutcome:
    """Decode ``path`` sequentially and return per-frame records.

    Individual packet failures are collected in ``decode_errors`` and decoding
    continues with the next packet.  A failure to open the container or find a
    video stream raises :class:`InputError`; an unusable time base raises
    :class:`DecodeError`.  When ``max_frames`` is reached before the stream ends
    the outcome is marked ``truncated`` so callers never report it as a complete
    scan.

    ``info`` lets a caller pass stream facts it has already probed, avoiding a
    second header parse; it is validated the same way either way.
    """
    if max_frames is not None and max_frames <= 0:
        raise ValueError("max_frames must be positive when provided")

    source = Path(path)
    stream_info = info if info is not None else probe(source)
    base = time_base if time_base is not None else stream_info.time_base
    if base is None or base <= 0:
        raise DecodeError("stream reports no usable time base", reason="invalid_time_base")

    outcome = DecodeOutcome()
    try:
        container = av.open(str(source))
    except av.FFmpegError as exc:  # type: ignore[attr-defined]
        raise InputError(f"cannot open container: {exc}", reason="open_failed") from exc
    except OSError as exc:
        raise InputError(f"cannot read file: {exc}", reason="open_failed") from exc

    try:
        if not container.streams.video:
            raise InputError("no video stream in container", reason="no_video_stream")
        stream = container.streams.video[0]

        def record(frame: object) -> None:
            outcome.frames.append(
                FrameRecord(
                    index=len(outcome.frames),
                    pict_type=_pict_type(frame),
                    pts=getattr(frame, "pts", None),
                    dts=getattr(frame, "dts", None),
                    time_base=base,
                )
            )

        limit_hit = False
        packets = container.demux(stream)
        for packet in packets:
            if not packet.size:
                outcome.empty_packets += 1
                continue
            try:
                decoded = packet.decode()
            except av.FFmpegError as exc:  # type: ignore[attr-defined]
                outcome.decode_errors.append(_err_text(exc))
                continue
            for offset, frame in enumerate(decoded):
                record(frame)
                if max_frames is not None and len(outcome.frames) >= max_frames:
                    limit_hit = True
                    if offset + 1 < len(decoded):
                        # The packet already yielded more frames than we keep.
                        outcome.truncated = True
                        return outcome
                    break
            if limit_hit:
                break

        if limit_hit:
            # Confirm whether the stream really ended here; hitting the cap on
            # the final frame is a complete scan, not a truncation.
            outcome.truncated = _more_frames_available(packets, stream)
            return outcome

        # Flush frames still held by the decoder (B-frame reordering delay).
        try:
            pending = stream.codec_context.decode(None)
        except av.FFmpegError as exc:  # type: ignore[attr-defined]
            outcome.decode_errors.append(_err_text(exc))
            pending = []
        for offset, frame in enumerate(pending):
            record(frame)
            if max_frames is not None and len(outcome.frames) >= max_frames:
                outcome.truncated = offset + 1 < len(pending)
                break
    finally:
        container.close()

    return outcome


def _more_frames_available(packets: object, stream: object) -> bool:
    """Is there at least one more decodable frame after the cap?

    Only metadata is touched: one packet is peeked from the demuxer, and the
    decoder is flushed only if the demuxer is exhausted.  A pending decoder
    buffer counts as truncation because those frames were never reported.
    """
    try:
        for packet in packets:  # type: ignore[union-attr]
            if getattr(packet, "size", 0):
                return True
    except av.FFmpegError:  # type: ignore[attr-defined]
        return False
    try:
        flushed = stream.codec_context.decode(None)  # type: ignore[union-attr]
    except (av.FFmpegError, AttributeError):  # type: ignore[attr-defined]
        return False
    return bool(flushed)
