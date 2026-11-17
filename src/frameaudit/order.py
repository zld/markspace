"""Bounded, stable frame reordering.

Frames arrive in decode order and must leave in presentation (PTS) order.
The reorder is:

* stable  — equal PTS keep their decode order, via the decode-index tie break;
* bounded — the heap may never grow past ``limit``.  When it is full the
  oldest (minimum-PTS) frame is emitted to make room; if the incoming frame
  belongs before that victim the true reorder depth exceeds the limit and
  :class:`ReorderLimitError` is raised instead of growing without bound;
* flushed — ``finish()`` drains every buffered frame in PTS order at EOF.

Frames whose PTS is missing sort after all timestamped frames, again stably.
"""

from __future__ import annotations

import heapq
from collections.abc import Iterable, Iterator

from .decode import DecodedFrame
from .errors import ReorderLimitError, UsageError

# Typical B-frame reorder depth is 1-4 frames; allow generous headroom.
DEFAULT_REORDER_LIMIT = 32


def _key(frame: DecodedFrame) -> tuple[int, int, int]:
    if frame.pts is None:
        # Missing PTS: park after every real timestamp, stable by index.
        return (1, 0, frame.decode_index)
    return (0, frame.pts, frame.decode_index)


class BoundedFrameReorderer:
    """Min-heap reorder buffer with a hard size cap."""

    def __init__(self, limit: int = DEFAULT_REORDER_LIMIT) -> None:
        if limit < 1:
            raise UsageError(f"reorder limit must be >= 1, got {limit}")
        self._limit = limit
        self._heap: list[tuple[tuple[int, int, int], DecodedFrame]] = []

    @property
    def limit(self) -> int:
        return self._limit

    @property
    def buffered(self) -> int:
        return len(self._heap)

    def push(self, frame: DecodedFrame) -> DecodedFrame | None:
        """Insert *frame*, returning a frame that had to be evicted, if any."""
        if len(self._heap) >= self._limit:
            victim_key, victim = self._heap[0]
            # The victim is the oldest frame we still hold.  An incoming
            # timestamped frame that precedes it cannot be placed within the
            # configured reorder depth.
            if (
                frame.pts is not None
                and victim.pts is not None
                and frame.pts < victim.pts
            ):
                raise ReorderLimitError(
                    f"reorder buffer limit {self._limit} exceeded: frame "
                    f"{frame.decode_index} (pts={frame.pts}) precedes buffered "
                    f"frame {victim.decode_index} (pts={victim.pts})"
                )
            heapq.heappop(self._heap)
            evicted = victim
        else:
            evicted = None
        heapq.heappush(self._heap, (_key(frame), frame))
        return evicted

    def finish(self) -> list[DecodedFrame]:
        """Drain the buffer in stable PTS order."""
        ordered: list[DecodedFrame] = []
        while self._heap:
            ordered.append(heapq.heappop(self._heap)[1])
        return ordered


def reorder_frames(
    frames: Iterable[DecodedFrame], limit: int = DEFAULT_REORDER_LIMIT
) -> Iterator[DecodedFrame]:
    """Run *frames* through the bounded reorderer and yield presentation order."""
    buf = BoundedFrameReorderer(limit)
    for frame in frames:
        evicted = buf.push(frame)
        if evicted is not None:
            yield evicted
    yield from buf.finish()
