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


@dataclass
class RemuxOptions:
    output: str
    mapping: Mapping
    timescale: int = 1_000_000
    offset_ms: int = 0
    repair: bool = False
    rounding: str = "round"
    clip_negative: bool = True
    max_gap_ms: int | None = None
    movie_timescale: int | None = None
    derive_mapping: bool = False

    @property
    def mode(self) -> str:
        return "repair" if self.repair else "strict"


@dataclass
class RemuxResult:
    output: str
    tracks: list[dict[str, Any]] = field(default_factory=list)
    samples: int = 0
    corrections: dict[str, int] = field(default_factory=dict)
    first_tick: int | None = None
    last_end_tick: int | None = None
    timescale: int = 1_000_000
    box_count: int = 0
    index_ok: bool = False
    mdat_offsets: dict[str, int] = field(default_factory=dict)
    mapping_derived: bool = False
    file_size: int = 0

    def to_dict(self) -> dict[str, Any]:
        return {
            "output": self.output,
            "timescale": self.timescale,
            "tracks": self.tracks,
            "sample_count": self.samples,
            "corrections": self.corrections,
            "first_tick": self.first_tick,
            "last_end_tick": self.last_end_tick,
            "box_count": self.box_count,
            "index_ok": self.index_ok,
            "mdat_offsets": self.mdat_offsets,
            "mapping_derived": self.mapping_derived,
            "file_size": self.file_size,
        }


def _samples_for_track(mk: MatroskaFile, track: Track) -> list[SampleInput]:
    """Flatten blocks of one track in container (decode) order.

    Each laced frame becomes its own MP4 sample; ``intra_block_index`` carries
    its position so the timestamp stage can space frames that share a block
    timecode.
    """
    samples: list[SampleInput] = []
    for cluster in mk.clusters:
        for block in cluster.blocks:
            if block.track != track.number:
                continue
            samples.append(
                SampleInput(
                    track=track.number,
                    cluster_timestamp=cluster.timestamp,
                    block_timecode=block.timecode,
                    intra_block_index=block.lace_index,
                    intra_block_count=block.lace_count,
                    data_offset=block.data_offset,
                    data_length=block.data_length,
                    keyframe=block.keyframe,
                    default_duration_ns=track.default_duration_ns,
                    block_duration=block.block_duration,
                )
            )
    return samples


def _gather_samples(mk: MatroskaFile) -> list[SampleInput]:
    all_samples: list[SampleInput] = []
    for track in mk.tracks:
        samples = _samples_for_track(mk, track)
        if not samples:
            raise InputError(
                f"track {track.number} has no media blocks",
                offset=track.entry_offset,
                path=track.path,
                track=track.number,
                stage="remux",
            )
        all_samples.extend(samples)
    if not all_samples:
        raise InputError("input contains no media blocks", stage="remux")
    return all_samples


def _payload_plan(
    specs: list[mp4.TrackSpec], timeline: TimelineReport
) -> list[tuple[int, list[tuple[int, int]]]]:
    """Per-track contiguous payload plan: (source_track, [(offset, length)]).

    MP4 describes each track as chunks of samples, so payload is laid out
    grouped per track in decode order.  Only slice references are held; media
    bytes are streamed once during the write.
    """
    plan: list[tuple[int, list[tuple[int, int]]]] = []
    for spec in specs:
        report = timeline.tracks[spec.source_track]
        pairs = [
            (int(off), int(length))
            for off, length in zip(report.data_offsets, report.data_lengths)
        ]
        plan.append((spec.source_track, pairs))
    return plan


def _check_payload_bounds(mk: MatroskaFile) -> None:
    limit = len(mk.data)
    for cluster in mk.clusters:
        for block in cluster.blocks:
            if block.data_offset < 0 or block.data_offset + block.data_length > limit:
                raise InputError(
                    f"block payload [{block.data_offset}, "
                    f"{block.data_offset + block.data_length}) crosses the file end",
                    offset=block.data_offset,
                    track=block.track,
                    stage="remux",
                )
            if block.data_length <= 0:
                raise InputError(
                    "zero-length block payload",
                    offset=block.data_offset,
                    track=block.track,
                    stage="remux",
                )


def _build_track_specs(
    mk: MatroskaFile,
    mapping: dict[int, int],
    timeline: TimelineReport,
    *,
    min_movie_timescale: int,
) -> tuple[list[mp4.TrackSpec], dict[str, int]]:
    specs: list[mp4.TrackSpec] = []
    offsets: dict[str, int] = {}
    for track in mk.tracks:
        target_id = mapping[track.number]
        tl = timeline.tracks[track.number]
        entry = mp4.build_sample_entry(
            track.codec_id,
            codec_private=track.codec_private,
            width=track.pixel_width or 0,
            height=track.pixel_height or 0,
            channels=track.channels or 2,
            sample_rate=round(track.sampling_frequency or 48000),
        )
        spec = mp4.TrackSpec(
            track_id=target_id,
            source_track=track.number,
            handler=HANDLER_BY_TRACK_TYPE[track.track_type],
            timescale=timeline.timescale,
            sample_entry=entry,
            durations=[int(d) for d in tl.durations],
            sizes=[int(s) for s in tl.data_lengths],
            keyframes=[bool(k) for k in tl.keyframes],
            chunk_offset=0,
            first_tick=int(tl.ticks[0]),
            language=track.language,
            width=track.pixel_width or 0,
            height=track.pixel_height or 0,
            channels=track.channels or 2,
            sample_rate=round(track.sampling_frequency or 48000),
        )
        specs.append(spec)
        offsets[str(target_id)] = 0
    return specs, offsets
