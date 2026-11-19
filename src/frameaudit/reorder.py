"""Presentation-order reordering with a bounded cache window.

B-frame codecs emit frames in decode order, which differs from presentation
order.  Frames are buffered and released once enough later frames have been
seen.  Ordering is fully deterministic:

* frames with a known PTS sort by presentation time in seconds;
* frames sharing the same PTS keep their relative decode order (stable);
* frames without a PTS are anchored by decode index and never dropped.

The cache holds at most ``window_size`` frames.  Once full, the smallest
buffered frame is released to make room.  A *window overflow* is recorded only
when a frame later turns out to sort before a frame already released: the
window was shallower than the stream's real reorder depth, so perfect
presentation order could no longer be guaranteed.  Such a late frame is
appended at the tail in stable decode order instead of mutating the released
prefix, keeping the output lossless and deterministic while reporting the
degradation in :attr:`ReorderBuffer.overflow_count`.
"""

from __future__ import annotations

from typing import Iterable, Optional

from .errors import LimitExceededError
from .model import FrameRecord

# Default maximum number of frames held in the reorder cache.  x264 with
# bframes=2 needs a depth of 3; 16 gives headroom for deeper GOP structures.
DEFAULT_WINDOW_SIZE = 16


def _sort_key(frame: FrameRecord) -> tuple[float, int]:
    """Sort key: presentation time when known, else a late sentinel."""
    pts_seconds = frame.pts_seconds()
    if pts_seconds is None:
        # Missing PTS sorts after all timestamped frames; decode_index keeps
        # multiple missing-PTS frames in deterministic decode order.
        return (float("inf"), frame.decode_index)
    return (pts_seconds, frame.decode_index)


class ReorderBuffer:
    """Bounded streaming buffer producing frames in presentation order."""

    def __init__(
        self,
        window_size: int = DEFAULT_WINDOW_SIZE,
        max_frames: Optional[int] = None,
    ) -> None:
        if window_size <= 0:
            raise LimitExceededError(
                f"reorder window size must be positive, got {window_size}"
            )
        self.window_size = window_size
        self.max_frames = max_frames
        self._buffer: list[FrameRecord] = []
        # Sort key of the most recently released in-order frame.
        self._released_key: Optional[tuple[float, int]] = None
        # Frames that arrived too late to join the released prefix.
        self._late: list[FrameRecord] = []
        # Every frame that triggered an overflow, retained for per-frame flags.
        self.overflowed_frames: list[FrameRecord] = []
        self.overflow_count = 0
        self.total_seen = 0

    def _pop_minimum(self) -> FrameRecord:
        idx = min(
            range(len(self._buffer)),
            key=lambda i: _sort_key(self._buffer[i]),
        )
        return self._buffer.pop(idx)

    def _release(self, frame: FrameRecord) -> Optional[FrameRecord]:
        """Release an in-order frame, or park a late frame on the tail."""
        key = _sort_key(frame)
        if self._released_key is not None and key < self._released_key:
            self._late.append(frame)
            self.overflowed_frames.append(frame)
            self.overflow_count += 1
            return None
        self._released_key = key
        return frame

    def push(self, frame: FrameRecord) -> list[FrameRecord]:
        """Add one decode-order frame; return frames released by this call."""
        if self.max_frames is not None and self.total_seen >= self.max_frames:
            raise LimitExceededError(
                f"reorder cache frame limit of {self.max_frames} exceeded; "
                "input contains more frames than the configured bound"
            )
        self.total_seen += 1
        self._buffer.append(frame)
        if len(self._buffer) <= self.window_size:
            return []
        flushed = self._release(self._pop_minimum())
        return [flushed] if flushed is not None else []

    def finish(self) -> list[FrameRecord]:
        """Drain buffered frames, then append stable late frames."""
        ordered: list[FrameRecord] = []
        while self._buffer:
            frame = self._release(self._pop_minimum())
            if frame is not None:
                ordered.append(frame)
        # Late frames could not join the released prefix; emit them in stable
        # decode order so output remains deterministic and lossless.
        self._late.sort(key=lambda item: item.decode_index)
        ordered.extend(self._late)
        self._late.clear()
        return ordered


def reorder_frames(
    frames: Iterable[FrameRecord],
    window_size: int = DEFAULT_WINDOW_SIZE,
    max_frames: Optional[int] = None,
) -> tuple[list[FrameRecord], int, set[int]]:
    """Reorder a complete frame list.

    Returns the ordered frames, the window overflow count and the set of
    ``decode_index`` values belonging to frames that overflowed (late frames
    parked on the tail), so callers can flag them per frame.
    """
    buffer = ReorderBuffer(window_size=window_size, max_frames=max_frames)
    ordered: list[FrameRecord] = []
    for frame in frames:
        ordered.extend(buffer.push(frame))
    ordered.extend(buffer.finish())
    overflow_indices = {
        frame.decode_index for frame in buffer.overflowed_frames
    }
    return ordered, buffer.overflow_count, overflow_indices


def stable_presentation_order(frames: Iterable[FrameRecord]) -> list[FrameRecord]:
    """Pure stable sort used when no streaming window bound is required."""
    return sorted(frames, key=_sort_key)
