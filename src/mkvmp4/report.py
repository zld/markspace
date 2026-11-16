"""Terminal reporting: element tree summary, track map and timestamps.

The report is pure text (no GUI dependencies) and aggregates three views:

* :class:`TreeSummary` — node count, max depth and unknown-size master count
  collected by walking the EBML structure once;
* track mapping — source Matroska TrackNumber -> MP4 track index + four-CC;
* per-track sample timing after rebasing, plus the global rebase offset.

All numbers come from already-parsed objects, so reporting never re-reads
the (read-only) input file.
"""

from dataclasses import dataclass, field

from .ebml import schema
from .ebml.reader import EbmlReader, Element
from .mkv.segment import MatroskaFile
from .timeline import Timeline, TrackTimeline


@dataclass(slots=True)
class TreeSummary:
    """Statistics gathered while walking the raw EBML element tree."""

    nodes: int = 0
    max_depth: int = 0
    unknown_size_masters: int = 0
    element_names: list[str] = field(default_factory=list)

    def record(self, element: Element) -> None:
        self.nodes += 1
        self.max_depth = max(self.max_depth, element.depth)
        self.element_names.append(element.name)
        if element.unknown_size and schema.is_master(element.element_id):
            self.unknown_size_masters += 1


def summarize_tree(path: str) -> TreeSummary:
    """Stream the file once and collect structural statistics."""
    summary = TreeSummary()
    with EbmlReader(path) as reader:
        reader.open_root()
        _walk_level(reader, summary)
    return summary


def _walk_level(reader: EbmlReader, summary: TreeSummary) -> None:
    while True:
        element = reader.next_element()
        if element is None:
            return
        summary.record(element)
        if element.unknown_size or schema.is_master(element.element_id):
            reader.open_master(element)
            _walk_level(reader, summary)
            reader.close_master()


@dataclass(slots=True)
class TrackMapEntry:
    source_number: int
    mp4_index: int
    codec: str
    language: str
    track_type: int


def build_track_map(mkv: MatroskaFile) -> list[TrackMapEntry]:
    """Map Matroska track numbers to 1-based MP4 track indices."""
    return [
        TrackMapEntry(
            source_number=track.number,
            mp4_index=index,
            codec=track.mp4_codec,
            language=track.language,
            track_type=track.track_type,
        )
        for index, track in enumerate(mkv.tracks, start=1)
    ]


def format_tree_line(summary: TreeSummary) -> str:
    return (f"ELEMENT_TREE nodes={summary.nodes} "
            f"max_depth={summary.max_depth} "
            f"unknown_size_masters={summary.unknown_size_masters}")


def format_track_map_lines(entries: list[TrackMapEntry]) -> list[str]:
    return [
        f"TRACK_MAP source={entry.source_number}->mp4={entry.mp4_index} "
        f"codec={entry.codec} lang={entry.language}"
        for entry in entries
    ]


def format_rebase_line(timeline: Timeline) -> str:
    return f"rebase_ms={timeline.rebase_ms:.3f}"


def format_track_timing_lines(timeline: Timeline) -> list[str]:
    """Per-track sample count and first/last presentation timestamps."""
    lines: list[str] = []
    for track in timeline.tracks:
        kind = "video" if track.track.track_type == 1 else "audio"
        lines.append(
            f"{kind} samples={len(track.samples)} "
            f"first_pts_ms={track.first_pts_ms:.3f} "
            f"last_pts_ms={track.last_pts_ms:.3f}")
    return lines


def format_flat_lines(result) -> list[str]:
    """Flat-mode specific summary (sample total and offset index size)."""
    return [
        f"flat samples={result.total_samples} "
        f"{result.offset_box}_entries={result.offset_entries} "
        f"chunks={','.join(str(c) for c in result.chunks_per_track)}",
    ]


def format_fragmented_lines(result) -> list[str]:
    """Fragmented-mode specific summary (fragment and sidx counts)."""
    return [
        f"fragmented fragments={result.fragment_count} "
        f"sidx_refs={result.sidx_refs} "
        f"samples={result.total_samples}",
    ]


def render_report(summary: TreeSummary, mkv: MatroskaFile,
                  timeline: Timeline, mode_lines: list[str]) -> str:
    """Assemble the full terminal report as a single string."""
    lines = [format_tree_line(summary)]
    lines.extend(format_track_map_lines(build_track_map(mkv)))
    lines.append(format_rebase_line(timeline))
    lines.extend(format_track_timing_lines(timeline))
    lines.extend(mode_lines)
    return "\n".join(lines)
