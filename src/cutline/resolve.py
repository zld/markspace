"""Deterministic overlap resolution with post-resolution verification.

Two strategies are provided:

``priority``
    Higher ``priority`` values win the contested frames.  Losing clips are
    trimmed; a clip covered on both sides may survive as several fragments.
    Ordering is ``(-priority, original index, clip id)`` so equal-priority
    clips are decided by original order and then by stable id.

``preserve-duration``
    Clips are never shortened.  In original order (ties broken by clip id),
    each clip is pushed right until it starts where the previous one ends.
    Clip durations are preserved exactly; the sequence may grow, and the
    growth is reported as ``duration_delta_frames``.

Every produced clip is re-validated (length, source mapping, fades), and if a
slice would land between source frames the resolver raises a
:class:`~cutline.errors.ResolutionError` naming the track and clip.
"""

from __future__ import annotations

from dataclasses import dataclass, field, replace
from fractions import Fraction
from typing import Any, Iterable

from .errors import ResolutionError
from .model import Clip, RateSegment, Sequence, Track
from .overlap import scan_sequence
from .timebase import FrameSpan, require_int

STRATEGIES = ("priority", "preserve-duration")


@dataclass(frozen=True, slots=True)
class ResolvedInterval:
    track_id: str
    clip_id: str
    origin_clip_id: str
    timeline_start: int
    duration: int
    source_in: int
    fade_in: int
    fade_out: int
    priority: int

    @property
    def span(self) -> FrameSpan:
        return FrameSpan(self.timeline_start, self.duration)

    def to_json(self) -> dict[str, Any]:
        return {
            "track_id": self.track_id,
            "clip_id": self.clip_id,
            "origin_clip_id": self.origin_clip_id,
            "timeline_start": self.timeline_start,
            "duration": self.duration,
            "source_in": self.source_in,
            "fade_in": self.fade_in,
            "fade_out": self.fade_out,
            "priority": self.priority,
        }


@dataclass(frozen=True, slots=True)
class ResolutionResult:
    strategy: str
    sequence: Sequence
    intervals: tuple[ResolvedInterval, ...]
    conflicts_before: int
    conflicts_after: int
    duration_before: int
    duration_after: int
    # Resolved clip id -> input clip id.  Fragments get fresh ids, and this map
    # keeps provenance exact even when input ids themselves contain '#'.
    origins: dict[str, str] = field(default_factory=dict, compare=False, repr=False)

    @property
    def duration_delta_frames(self) -> int:
        return self.duration_after - self.duration_before

    def to_json(self) -> dict[str, Any]:
        return {
            "strategy": self.strategy,
            "conflicts_before": self.conflicts_before,
            "conflicts_after": self.conflicts_after,
            "duration_before": self.duration_before,
            "duration_after": self.duration_after,
            "duration_delta_frames": self.duration_delta_frames,
            "resolved_intervals": [interval.to_json() for interval in self.intervals],
        }


def resolve(sequence: Sequence, strategy: str) -> ResolutionResult:
    if strategy not in STRATEGIES:
        raise ResolutionError(
            f"unknown strategy {strategy!r}; expected one of {', '.join(STRATEGIES)}"
        )
    before = scan_sequence(sequence)
    if strategy == "priority":
        resolved, origins = _resolve_priority(sequence)
    else:
        resolved, origins = _resolve_preserve_duration(sequence)
    after = scan_sequence(resolved)
    if after.conflict_count:
        raise ResolutionError(
            f"strategy {strategy!r} left {after.conflict_count} conflict(s)",
            track_id=after.conflicts[0].track_id,
            clip_id=after.conflicts[0].first_id,
        )
    _verify_intervals(sequence, resolved)
    intervals = tuple(_intervals_for(resolved, origins))
    return ResolutionResult(
        strategy=strategy,
        sequence=resolved,
        intervals=intervals,
        conflicts_before=before.conflict_count,
        conflicts_after=after.conflict_count,
        duration_before=sequence.duration,
        duration_after=resolved.duration,
        origins=origins,
    )


def _intervals_for(sequence: Sequence,
                   origins: dict[str, str]) -> Iterable[ResolvedInterval]:
    for track in sequence.tracks:
        for clip in sorted(track.clips, key=lambda c: (c.timeline_start, c.id)):
            yield ResolvedInterval(
                track_id=track.id,
                clip_id=clip.id,
                origin_clip_id=origins.get(clip.id, clip.id),
                timeline_start=clip.timeline_start,
                duration=clip.duration,
                source_in=clip.source_in,
                fade_in=clip.fade_in,
                fade_out=clip.fade_out,
                priority=clip.priority,
            )


# --------------------------------------------------------------------------
# Clip slicing


def _exact_source_offset(clip: Clip, local_frame: int) -> int:
    consumed = Fraction(0)
    for segment in clip.rate_segments:
        if local_frame >= segment.end:
            consumed += segment.speed * segment.duration
            continue
        if local_frame > segment.offset:
            consumed += segment.speed * (local_frame - segment.offset)
        if consumed.denominator != 1:
            raise ResolutionError(
                f"cut at local frame {local_frame} falls between source frames "
                f"({consumed} source frames)",
                clip_id=clip.id,
            )
        return consumed.numerator
    if local_frame == clip.duration:
        return clip.source_frames
    raise ResolutionError(f"local frame {local_frame} outside clip", clip_id=clip.id)


def slice_clip(clip: Clip, start_local: int, end_local: int) -> Clip:
    """Return the clip-local range ``[start_local, end_local)`` as a new clip."""

    require_int(start_local, "slice start", minimum=0)
    require_int(end_local, "slice end", minimum=0)
    if not (start_local < end_local <= clip.duration):
        raise ResolutionError(
            f"invalid slice [{start_local}, {end_local}) for {clip.duration}-frame clip",
            clip_id=clip.id,
        )
    if start_local == 0 and end_local == clip.duration:
        return clip

    head_source = _exact_source_offset(clip, start_local)
    _exact_source_offset(clip, end_local)  # both cut points must be exact
    segments: list[RateSegment] = []
    for segment in clip.rate_segments:
        lo = max(start_local, segment.offset)
        hi = min(end_local, segment.end)
        if lo >= hi:
            continue
        # The segment consumes speed*hi - speed*lo source frames; this is an
        # integer because both endpoints were checked to land exactly.
        offset = lo - start_local
        segments.append(RateSegment(offset, hi - lo, segment.speed))
    if not segments:
        raise ResolutionError("slice produced no rate segments", clip_id=clip.id)

    fade_in = 0
    if start_local == 0:
        fade_in = clip.fade_in
    elif start_local < clip.fade_in:
        fade_in = clip.fade_in - start_local
    fade_out = 0
    if end_local == clip.duration:
        fade_out = clip.fade_out
    elif clip.duration - end_local < clip.fade_out:
        fade_out = clip.fade_out - (clip.duration - end_local)
    duration = end_local - start_local
    if fade_in + fade_out > duration:
        raise ResolutionError(
            f"slice fades {fade_in}+{fade_out} exceed fragment duration {duration}",
            clip_id=clip.id,
        )
    return Clip(
        id=clip.id,
        media_id=clip.media_id,
        timeline_start=clip.timeline_start + start_local,
        duration=duration,
        source_in=clip.source_in + head_source,
        source_duration=clip.source_duration,
        rate_segments=tuple(segments),
        fade_in=fade_in,
        fade_out=fade_out,
        priority=clip.priority,
    )


# --------------------------------------------------------------------------
# Strategies


def _subtract(span: FrameSpan, occupied: list[FrameSpan]) -> list[FrameSpan]:
    pieces = [span]
    for block in occupied:
        next_pieces: list[FrameSpan] = []
        for piece in pieces:
            if not piece.overlaps(block):
                next_pieces.append(piece)
                continue
            if block.start > piece.start:
                next_pieces.append(FrameSpan(piece.start, block.start - piece.start))
            if block.end < piece.end:
                next_pieces.append(FrameSpan(block.end, piece.end - block.end))
        pieces = next_pieces
    return pieces


def _fragment_id(origin_id: str, index: int, used: set[str]) -> str:
    """First fragment keeps the input id; later ones get a fresh ``#n`` suffix."""

    if index == 0:
        return origin_id
    ordinal = index + 1
    while f"{origin_id}#{ordinal}" in used:
        ordinal += 1
    return f"{origin_id}#{ordinal}"


def _resolve_priority(sequence: Sequence) -> tuple[Sequence, dict[str, str]]:
    resolved_tracks: list[Track] = []
    origins: dict[str, str] = {}
    for track in sequence.tracks:
        used = {clip.id for clip in track.clips}
        candidates = sorted(
            enumerate(track.clips),
            key=lambda item: (-item[1].priority, item[0], item[1].id),
        )
        accepted: list[Clip] = []
        for _, clip in candidates:
            occupied = sorted((other.span for other in accepted), key=lambda s: s.start)
            pieces = _subtract(clip.span, occupied)
            if not pieces:
                continue  # fully covered by higher-priority material
            pieces.sort(key=lambda piece: piece.start)
            for index, piece in enumerate(pieces):
                fragment = slice_clip(
                    clip,
                    piece.start - clip.timeline_start,
                    piece.end - clip.timeline_start,
                )
                fragment_id = _fragment_id(clip.id, index, used)
                used.add(fragment_id)
                origins[fragment_id] = clip.id
                accepted.append(replace(fragment, id=fragment_id))
        accepted.sort(key=lambda c: (c.timeline_start, c.id))
        resolved_tracks.append(track.with_clips(accepted))
    return sequence.with_tracks(resolved_tracks), origins


def _resolve_preserve_duration(sequence: Sequence) -> tuple[Sequence, dict[str, str]]:
    resolved_tracks: list[Track] = []
    origins: dict[str, str] = {}
    for track in sequence.tracks:
        # Ties are decided by original order, then by stable id.
        ordered = [clip for _, clip in sorted(enumerate(track.clips),
                                              key=lambda item: (item[0], item[1].id))]
        placed: list[Clip] = []
        cursor = 0
        for clip in ordered:
            start = max(clip.timeline_start, cursor)
            moved = replace(clip, timeline_start=start)
            placed.append(moved)
            origins[moved.id] = clip.id
            cursor = start + clip.duration
        placed.sort(key=lambda c: (c.timeline_start, c.id))
        resolved_tracks.append(track.with_clips(placed))
    return sequence.with_tracks(resolved_tracks), origins


# --------------------------------------------------------------------------
# Verification


def _verify_intervals(original: Sequence, resolved: Sequence) -> None:
    for track in resolved.tracks:
        for clip in track.clips:
            try:
                Clip(
                    id=clip.id,
                    media_id=clip.media_id,
                    timeline_start=clip.timeline_start,
                    duration=clip.duration,
                    source_in=clip.source_in,
                    source_duration=clip.source_duration,
                    rate_segments=clip.rate_segments,
                    fade_in=clip.fade_in,
                    fade_out=clip.fade_out,
                    priority=clip.priority,
                )
            except Exception as exc:  # pragma: no cover - defensive
                raise ResolutionError(
                    f"resolved clip is invalid: {exc}", track_id=track.id, clip_id=clip.id
                ) from exc
            if clip.duration <= 0:
                raise ResolutionError(
                    "resolved clip has non-positive duration",
                    track_id=track.id,
                    clip_id=clip.id,
                )
    if scan_sequence(resolved).conflict_count:
        raise ResolutionError("resolved sequence still contains overlaps")
