"""Timeline ordering, duplicate flagging and missing-interval inference."""

from __future__ import annotations

from datetime import datetime

from .models import Event, GapInterval


def sort_and_flag(events: list[Event]) -> list[Event]:
    """Stable sort by corrected UTC; duplicates are kept and flagged.

    An event is a duplicate when its id was already seen, or when another
    event has the identical (utc, type, description) content. The first
    occurrence is the canonical one; later ones point at it via
    ``duplicate_of`` and are excluded from gap inference.
    """
    ordered = sorted(events, key=lambda e: (e.utc_final, e.seq))
    seen_ids: dict[str, str] = {}
    seen_content: dict[tuple, str] = {}
    for event in ordered:
        content_key = (event.utc_final, event.event_type, event.description)
        if event.event_id in seen_ids:
            event.duplicate_of = seen_ids[event.event_id]
        elif content_key in seen_content:
            event.duplicate_of = seen_content[content_key]
        else:
            seen_ids[event.event_id] = event.event_id
            seen_content[content_key] = event.event_id
    return ordered


def find_gaps(
    ordered: list[Event],
    acquisition_start: datetime,
    acquisition_end: datetime,
    gap_threshold_seconds: int,
) -> list[GapInterval]:
    """Infer missing intervals strictly greater than the threshold.

    Head and tail intervals are measured against the explicit acquisition
    start/end boundaries from the manifest.
    """
    uniques = [e for e in ordered if e.duplicate_of is None]
    gaps: list[GapInterval] = []

    def add(start: datetime, end: datetime, kind: str) -> None:
        delta = int((end - start).total_seconds())
        if delta > gap_threshold_seconds:
            gaps.append(GapInterval(start, end, delta, kind))

    if not uniques:
        add(acquisition_start, acquisition_end, "interior")
        return gaps

    add(acquisition_start, uniques[0].utc_final, "head")
    for earlier, later in zip(uniques, uniques[1:]):
        add(earlier.utc_final, later.utc_final, "interior")
    add(uniques[-1].utc_final, acquisition_end, "tail")
    return gaps
