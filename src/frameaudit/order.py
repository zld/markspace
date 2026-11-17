"""Bounded, stable frame reordering.

Frames arrive in decode order and must leave in presentation (PTS) order.
The reorder is:

* stable  — equal PTS keep their decode order, via the decode-index tie break;
* bounded — the heap may never grow past ``limit``.  When it is full the
  oldest (minimum-PTS) frame is evicted to make room; if an incoming
  timestamped frame belongs before that victim the true reorder depth
  exceeds the limit and :class:`ReorderLimitError` is raised instead of
  growing without bound;
* flushed — ``finish()`` drains every buffered frame in PTS order at EOF.

Frames whose PTS is missing keep the position implied by their decode index:
after the heap drains, missing frames are stably inserted relative to the
timestamped frames, so a dropped timestamp neither sinks to the end nor
overtakes real PTS values.
"""

from __future__ import annotations

import heapq
from collections.abc import Iterable, Iterator

from .decode import DecodedFrame
from .errors import ReorderLimitError, UsageError

# Typical B-frame reorder depth is 1-4 frames; allow generous headroom.
DEFAULT_REORDER_LIMIT = 32


def _key(frame: DecodedFrame) -> tuple[int, int]:
    # Timestamped frames first, ordered by PTS; decode index breaks ties.
    return (frame.pts if frame.pts is not None else 0, frame.decode_index)


class BoundedFrameReorderer:
    """Min-heap reorder buffer with a hard size cap."""

    def __init__(self, limit: int = DEFAULT_REORDER_LIMIT) -> None:
        if limit < 1:
            raise UsageError(f"reorder limit must be >= 1, got {limit}")
        self._limit = limit
        self._heap: list[tuple[tuple[int, int], DecodedFrame]] = []

    @property
    def limit(self) -> int:
        return self._limit

    @property
    def buffered(self) -> int:
        return len(self._heap)

    def push(self, frame: DecodedFrame) -> DecodedFrame | None:
        """Insert *frame*, returning a frame that had to be evicted, if any."""
        if frame.pts is None:
            # Missing-PTS frames cannot participate in PTS ordering; park them
            # outside the heap and merge them back at their decode position.
            raise RuntimeError("use reorder_frames() for missing-PTS frames")

        if len(self._heap) >= self._limit:
            _, victim = self._heap[0]
            # The victim is the oldest frame we still hold.  An incoming frame
            # that precedes it cannot be placed within the reorder depth.
            if frame.pts < victim.pts:  # both are non-None here
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
        """Drain timestamped frames in stable PTS order."""
        ordered: list[DecodedFrame] = []
        while self._heap:
            ordered.append(heapq.heappop(self._heap)[1])
        return ordered


def _merge_missing(
    timestamped: list[DecodedFrame], missing: list[DecodedFrame]
) -> list[DecodedFrame]:
    """Insert missing-PTS frames at their decode-order positions, stably."""
    if not missing:
        return timestamped
    # Walk decode indices; for every missing frame, place it after the most
    # recently emitted frame with a smaller decode index.
    result: list[DecodedFrame] = []
    emitted: set[int] = set()
    missing_sorted = sorted(missing, key=lambda f: f.decode_index)
    ptr = 0
    for frame in timestamped:
        # Emit any missing frames whose decode position precedes this frame.
        while (
            ptr < len(missing_sorted)
            and missing_sorted[ptr].decode_index < frame.decode_index
        ):
            candidate = missing_sorted[ptr]
            if candidate.decode_index not in emitted:
                result.append(candidate)
                emitted.add(candidate.decode_index)
            ptr += 1
        result.append(frame)
    while ptr < len(missing_sorted):
        candidate = missing_sorted[ptr]
        if candidate.decode_index not in emitted:
            result.append(candidate)
        ptr += 1
    return result


def reorder_frames(
    frames: Iterable[DecodedFrame], limit: int = DEFAULT_REORDER_LIMIT
) -> Iterator[DecodedFrame]:
    """Run *frames* through the bounded reorderer and yield presentation order."""
    buf = BoundedFrameReorderer(limit)
    timestamped: list[DecodedFrame] = []
    missing: list[DecodedFrame] = []

    for frame in frames:
        if frame.pts is None:
            missing.append(frame)
            continue
        evicted = buf.push(frame)
        if evicted is not None:
            timestamped.append(evicted)
    timestamped.extend(buf.finish())
    yield from _merge_missing(timestamped, missing)
