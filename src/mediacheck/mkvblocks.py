"""Cluster block scanning used to derive per-track media duration.

Only block headers are read: the track number vint, the signed relative
timecode and the flags byte.  Sample payloads are skipped by declared length.
"""

from __future__ import annotations

from .binary import BinaryError
from .ebml import Element, iter_elements, issue, read_uint, read_vint_value
from .mkvdefs import (
    BLOCK,
    BLOCK_DURATION,
    BLOCK_GROUP,
    CLUSTER_TIMECODE,
    SIMPLE_BLOCK,
)
from .model import MediaFile, SEVERITY_WARNING

MAX_BLOCKS_PER_CLUSTER = 200_000


def block_payload(el: Element) -> tuple[int, int]:
    """Return ``(track_number, relative_timecode)`` from a Block body."""
    reader = el.body
    track = read_vint_value(reader)
    rel_tc = reader.read_int16()
    reader.read_uint8()  # flags: keyframe, lacing, discardable
    return track, rel_tc


def _block_group(el: Element, media: MediaFile) -> tuple[
        tuple[int, int] | None, int]:
    block = None
    duration = 0
    for grand in iter_elements(el.body, media, 3):
        try:
            if grand.id == BLOCK:
                block = block_payload(grand)
            elif grand.id == BLOCK_DURATION:
                duration = read_uint(grand.body)
        except BinaryError:
            continue
    return block, duration


def cluster_blocks(el: Element, media: MediaFile,
                   scale: int) -> list[tuple[int, float]]:
    """Return ``(track_number, block_end_ms)`` for every block in a cluster.

    ``scale`` is nanoseconds per timecode tick.  A BlockGroup without a
    BlockDuration contributes only its start time; that is still the best
    evidence of how far into the file the track reaches.
    """
    cluster_tc = 0
    blocks = 0
    ends: list[tuple[int, float]] = []
    for child in iter_elements(el.body, media, 2):
        if child.id == CLUSTER_TIMECODE:
            try:
                cluster_tc = read_uint(child.body)
            except BinaryError as exc:
                media.add_issue(issue("mkv.cluster_timecode_truncated",
                                      exc.reason, exc.offset))
        elif child.id == SIMPLE_BLOCK:
            try:
                track, rel_tc = block_payload(child)
            except BinaryError:
                continue
            blocks += 1
            ends.append((track, (cluster_tc + rel_tc) * scale / 1e6))
        elif child.id == BLOCK_GROUP:
            block, duration = _block_group(child, media)
            if block is not None:
                blocks += 1
                track, rel_tc = block
                ends.append((track, (cluster_tc + rel_tc + duration)
                             * scale / 1e6))
        if blocks > MAX_BLOCKS_PER_CLUSTER:
            media.add_issue(issue(
                "mkv.block_limit",
                f"cluster has more than {MAX_BLOCKS_PER_CLUSTER} blocks; "
                f"trailing blocks ignored", el.start, SEVERITY_WARNING))
            return ends
    return ends
