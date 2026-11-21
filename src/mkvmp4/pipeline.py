"""End-to-end orchestration: parse, map, rewrite timestamps, mux, verify.

The pipeline is the only place that knows about both container worlds.  It
keeps a single EBML parse for the inspect path and the remux path, streams
payload through ``mmap`` slices instead of copying media into memory, and
writes the MP4 to a temporary file that is atomically renamed only after the
written index has been re-read and checked against the data.
"""

from __future__ import annotations

import hashlib
import mmap
import os
import tempfile
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from . import mp4
from .errors import (
    InputError,
    OutputError,
    TrackMappingError,
    UnsupportedError,
    UsageError,
    VerifyError,
)
from .matroska import MatroskaFile, Track, parse_matroska
from .timestamps import SampleInput, TimelineReport, rewrite_timeline
from .tree import parse_tree, summarize

HANDLER_BY_TRACK_TYPE = {"video": "vide", "audio": "soun", "subtitle": "sbtl"}


@dataclass
class Mapping:
    """Explicit source-track -> MP4-track-id table plus per-track settings."""

    by_source: dict[int, int]

    @classmethod
    def parse(cls, spec: str) -> "Mapping":
        if not spec or not spec.strip():
            return cls({})
        by_source: dict[int, int] = {}
        seen_targets: dict[int, int] = {}
        for chunk in spec.split(","):
            chunk = chunk.strip()
            if not chunk:
                continue
            parts = chunk.split(":")
            if len(parts) != 2 or not all(p.strip().isdigit() for p in parts):
                raise UsageError(
                    f"invalid mapping entry {chunk!r}: expected SOURCE:TARGET",
                    stage="mapping",
                )
            source, target = int(parts[0]), int(parts[1])
            if source == 0 or target == 0:
                raise UsageError(
                    f"mapping entry {chunk!r}: track numbers must be positive",
                    stage="mapping",
                )
            if source in by_source:
                raise UsageError(
                    f"mapping entry {chunk!r}: source track {source} mapped twice",
                    stage="mapping",
                )
            if target in seen_targets:
                raise TrackMappingError(
                    f"target track ID {target} is used by both source track "
                    f"{seen_targets[target]} and {source}",
                    track=source,
                    stage="mapping",
                )
            by_source[source] = target
            seen_targets[target] = source
        return cls(by_source)

    def resolve(self, tracks: list[Track], *, derive: bool = False) -> dict[int, int]:
        """Validate the table against the parsed tracks before any output.

        ``derive`` allows an absent table for the demo path: each source track
        keeps its own number as the MP4 track ID.  Explicit tables are always
        validated strictly, including duplicate targets.
        """
        if not self.by_source and derive:
            by_source = {t.number: t.number for t in tracks}
            return self._validate(tracks, by_source)
        if not self.by_source:
            raise TrackMappingError(
                "no track mapping supplied; pass --map SOURCE:TARGET[,SOURCE:TARGET] "
                f"for source tracks {[t.number for t in tracks]}",
                stage="mapping",
            )
        return self._validate(tracks, self.by_source)

    def _validate(self, tracks: list[Track], by_source: dict[int, int]) -> dict[int, int]:
        targets: dict[int, int] = {}
        for source, target in by_source.items():
            if target in targets and targets[target] != source:
                raise TrackMappingError(
                    f"target track ID {target} is used by both source track "
                    f"{targets[target]} and {source}",
                    track=source,
                    stage="mapping",
                )
            targets[target] = source
        missing = [t.number for t in tracks if t.number not in by_source]
        if missing:
            first = next(t for t in tracks if t.number == missing[0])
            raise TrackMappingError(
                f"source track {first.number} ({first.track_type}, {first.codec_id}) "
                f"has no mapping; missing source tracks: {missing}",
                offset=first.entry_offset,
                path=first.path,
                track=first.number,
                stage="mapping",
            )
        unknown = sorted(set(by_source) - {t.number for t in tracks})
        if unknown:
            raise TrackMappingError(
                f"mapping references source tracks that do not exist: {unknown}",
                track=unknown[0],
                stage="mapping",
            )
        for track in tracks:
            if not track.codec_supported:
                raise UnsupportedError(
                    f"track {track.number}: codec {track.codec_id} is not in the "
                    f"supported subset {sorted(mp4.SUPPORTED_MP4_CODECS)}",
                    offset=track.entry_offset,
                    path=track.path,
                    track=track.number,
                    stage="mapping",
                )
            if track.codec_id not in mp4.SUPPORTED_MP4_CODECS:
                raise UnsupportedError(
                    f"track {track.number}: codec {track.codec_id} has no MP4 "
                    "sample entry in this build",
                    offset=track.entry_offset,
                    track=track.number,
                    stage="mapping",
                )
        return dict(by_source)


def open_ro(path: str | Path) -> tuple[Any, mmap.mmap]:
    """Open the input read-only and map it; payload is never copied wholesale."""
    try:
        handle = open(path, "rb")
    except OSError as exc:
        raise InputError(f"cannot open {path}: {exc.strerror}", stage="open") from exc
    try:
        size = os.fstat(handle.fileno()).st_size
        if size == 0:
            raise InputError(f"{path} is empty", stage="open")
        return handle, mmap.mmap(handle.fileno(), 0, access=mmap.ACCESS_READ)
    except Exception:
        handle.close()
        raise


def inspect_file(path: str | Path, *, max_depth: int = 64, max_elements: int = 200_000) -> dict[str, Any]:
    """Structural summary shared by ``inspect`` and the remux parse path."""
    handle, data = open_ro(path)
    try:
        file_size = len(data)
        top_nodes, stats = parse_tree(
            data, max_depth=max_depth, max_elements=max_elements
        )
        summary = summarize(top_nodes, stats)
        mk = parse_matroska(data, source=str(path))
    finally:
        data.close()
        handle.close()
    segment_ranges = [
        n for n in summary.nodes if n.name in ("Segment", "Cluster")
    ]
    return {
        "path": str(path),
        "file_size": file_size,
        "doc_type": mk.doc_type,
        "timestamp_scale_ns": mk.timestamp_scale,
        "tree": summary.to_dict(),
        "element_count": summary.total_count,
        "tracks": [t.to_dict() for t in mk.tracks],
        "track_count": len(mk.tracks),
        "segments_and_clusters": [
            {
                "name": n.name,
                "level": n.level,
                "offset": n.offset,
                "end_offset": n.end_offset,
                "size": n.size,
                "unknown_size": n.unknown_size,
            }
            for n in segment_ranges
        ],
        "cluster_count": len(mk.clusters),
        "block_count": sum(len(c.blocks) for c in mk.clusters),
    }


# ---------------------------------------------------------------------------
# Remux
# ---------------------------------------------------------------------------
