"""Deterministic overlap resolution.

Per track, the timeline is cut into atomic segments at every clip
boundary. Each segment is awarded to the covering clip with the highest
priority; ties are broken by input order, then by clip id, so the outcome
is stable and reproducible. Losing clips are trimmed and may be split
into several fragments; each fragment's source offset is shifted by the
same amount its timeline start moved. Adjacent fragments of the same
clip are merged, empty fragments are dropped, and fragment ids are
derived deterministically (``<id>#<n>`` when a clip is split).
"""
from __future__ import annotations

import heapq
from dataclasses import replace

from .model import Clip, Timeline, Track


def _winner_key(clip: Clip) -> tuple:
    # Highest priority wins; then earliest input order; then smallest id.
    return (-clip.priority, clip.input_index, clip.id)


def _resolve_track_clips(clips: list[Clip]) -> list[Clip]:
    live = [c for c in clips if c.duration > 0]
    if not live:
        return []
    boundaries = sorted({c.start for c in live} | {c.end for c in live})
    by_start = sorted(live, key=lambda c: (c.start, c.input_index, c.id))
    heap: list = []  # entries: (_winner_key, end, clip); key is unique
    segments: list = []
    ptr = 0
    for i, seg_start in enumerate(boundaries[:-1]):
        seg_end = boundaries[i + 1]
        while ptr < len(by_start) and by_start[ptr].start <= seg_start:
            clip = by_start[ptr]
            heapq.heappush(heap, (_winner_key(clip), clip.end, clip))
            ptr += 1
        while heap and heap[0][1] <= seg_start:  # lazy eviction of ended clips
            heapq.heappop(heap)
        if heap:
            segments.append((seg_start, seg_end, heap[0][2]))
    # Merge adjacent segments won by the same clip.
    merged: list = []
    for seg_start, seg_end, clip in segments:
        if merged and merged[-1][2] is clip and merged[-1][1] == seg_start:
            merged[-1] = (merged[-1][0], seg_end, clip)
        else:
            merged.append((seg_start, seg_end, clip))
    # Group fragments per source clip, preserving first-appearance order.
    frags_by_clip: dict[int, list] = {}
    owners: list[Clip] = []
    for seg_start, seg_end, clip in merged:
        key = id(clip)
        if key not in frags_by_clip:
            frags_by_clip[key] = []
            owners.append(clip)
        frags_by_clip[key].append((seg_start, seg_end))
    out: list[Clip] = []
    for clip in owners:
        fragments = frags_by_clip[id(clip)]
        single = len(fragments) == 1
        for n, (frag_start, frag_end) in enumerate(fragments, start=1):
            frag_id = clip.id if single else f"{clip.id}#{n}"
            out.append(
                replace(
                    clip,
                    id=frag_id,
                    start=frag_start,
                    duration=frag_end - frag_start,
                    source_start=clip.source_start + (frag_start - clip.start),
                )
            )
    out.sort(key=lambda c: (c.start, c.input_index, c.id))
    return out


def resolve_timeline(timeline: Timeline) -> Timeline:
    """Resolve every track; empty tracks are kept, empty clips dropped."""
    tracks: list[Track] = []
    next_index = 0
    for track in timeline.tracks:
        fragments = _resolve_track_clips(list(track.clips))
        reassigned = []
        for fragment in fragments:
            reassigned.append(replace(fragment, input_index=next_index))
            next_index += 1
        tracks.append(Track(name=track.name, clips=tuple(reassigned)))
    return Timeline(fps=timeline.fps, tracks=tuple(tracks), name=timeline.name)
