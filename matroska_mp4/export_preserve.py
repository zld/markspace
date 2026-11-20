"""Preserve path: keep per-track offsets/gaps and reject reordered frames.

The preserve export is the strict passthrough: every track's PTS must be
monotonic in decode order, the inter-track offset is retained through the
global timeline, and gaps are represented with ``stts``/``edts`` rather than
being compressed.  Any reordering fails before a byte is written.
"""

from __future__ import annotations

import os
from dataclasses import dataclass

from .matroska import ParsedMatroska
from .mp4_audit import AuditError, audit_mp4
from .mp4_writer import temporary_output, write_mp4
from .timeline import TimebaseOverflow, Timeline, TimelineError, prepare


class PreserveError(Exception):
    """The input cannot be preserved byte-faithfully (reordering, gaps)."""


@dataclass(slots=True)
class ExportResult:
    path: str
    mode: str
    timeline: Timeline
    audit: dict
    element_tree: dict
    unknown_size: dict
    track_map: list[dict]

    def report(self) -> dict:
        return {
            "mode": self.mode,
            "output": self.path,
            "element_tree": self.element_tree,
            "unknown_size": self.unknown_size,
            "track_map": self.track_map,
            "first_pts_ms": _ms(self.timeline.global_first_pts,
                                self.timeline.timescale),
            "last_pts_ms": _ms(self.timeline.global_last_pts,
                               self.timeline.timescale),
            "sample_count": self.timeline.sample_count,
            "ctts_tracks": self.timeline.ctts_tracks,
            "timescale": self.timeline.timescale,
            "source_first_pts_ms": _ms(self.timeline.source_first_pts,
                                       self.timeline.timescale),
            "normalized_start": self.timeline.normalized,
            "mdat": self.audit["dur"],
        }


def _ms(ticks: int, timescale: int) -> int:
    return int((ticks * 1000) // timescale)


def _element_tree(mkv: ParsedMatroska) -> dict:
    counts = mkv.document.counts()
    wanted = ("EBML", "Segment", "Info", "Tracks", "TrackEntry", "Cluster",
              "SimpleBlock", "BlockGroup", "Block", "Cues", "Attachments")
    tree = {name: counts.get(name, 0) for name in wanted}
    return {name: value for name, value in tree.items() if value}


def _track_map(timeline: Timeline) -> list[dict]:
    entries = []
    for track in timeline.tracks:
        entries.append({
            "mkv_track": track.track.number,
            "mp4_track_id": track.mp4_track_id,
            "codec": "avc1" if track.track.handler == "vide" else "mp4a",
            "sample_count": track.sample_count,
            "first_pts_ms": _ms(track.first_pts, timeline.timescale),
            "last_pts_ms": _ms(track.last_pts, timeline.timescale),
            "gaps": bool(track.media_base > 0),
            "has_intra_track_gaps": track.has_intra_gaps,
        })
    return entries


def run_preserve(mkv: ParsedMatroska, timescale: int, output_path: str) -> ExportResult:
    try:
        timeline = prepare(mkv, timescale, "preserve")
    except TimebaseOverflow as exc:
        raise PreserveError(str(exc)) from exc
    except TimelineError as exc:
        message = str(exc)
        if any(word in message for word in ("monotonic", "reordered", "decode order")):
            raise PreserveError(
                f"preserve mode refuses reordered frames: {message}") from exc
        raise PreserveError(f"preserve mode refused the input: {message}") from exc
    result = ExportResult(path=output_path, mode="preserve", timeline=timeline,
                          audit={}, element_tree=_element_tree(mkv),
                          unknown_size=mkv.document.unknown_sizes(),
                          track_map=_track_map(timeline))
    _write_and_audit(mkv, timeline, output_path, result)
    return result


def _write_and_audit(mkv: ParsedMatroska, timeline: Timeline, output_path: str,
                     result: ExportResult) -> None:
    """Write, audit, and only then atomically publish the output."""

    with temporary_output(output_path) as temp_path:
        write_mp4(mkv.path, mkv, timeline, temp_path)
        audit = audit_mp4(temp_path, mkv.path, mkv.samples, timeline.tracks,
                          timeline.timescale)
        if audit["sample_count"] != timeline.sample_count:
            raise AuditError("audited sample count differs from the timeline")
        os.replace(temp_path, output_path)
    result.audit = audit
