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
            if not track.codec_supported or track.codec_id not in mp4.SUPPORTED_MP4_CODECS:
                raise UnsupportedError(
                    f"track {track.number}: codec {track.codec_id} is not in the "
                    f"supported subset {sorted(mp4.SUPPORTED_MP4_CODECS)}; "
                    "this build does not transcode",
                    offset=track.entry_offset,
                    path=track.path,
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
) -> list[mp4.TrackSpec]:
    specs: list[mp4.TrackSpec] = []
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
    return specs


def remux_file(path: str | Path, options: RemuxOptions) -> RemuxResult:
    """Parse, map, rewrite and mux; write atomically after a full verification."""
    if options.timescale <= 0:
        raise UsageError("--timescale must be a positive integer")
    output = Path(options.output)
    if output.exists() and output.resolve() == Path(path).resolve():
        raise UsageError("output path must differ from the input path")
    handle, data = open_ro(path)
    try:
        mk = parse_matroska(data, source=str(path))
        if not mk.clusters:
            raise InputError("Segment contains no Cluster elements", stage="remux")
        mapping = options.mapping.resolve(mk.tracks, derive=options.derive_mapping)
        _check_payload_bounds(mk)
        samples = _gather_samples(mk)
        timeline = rewrite_timeline(
            samples,
            timestamp_scale=mk.timestamp_scale,
            timescale=options.timescale,
            offset_ms=options.offset_ms,
            mode=options.mode,
            rounding=options.rounding,
            clip_negative=options.clip_negative,
            max_gap_ms=options.max_gap_ms,
        )
        specs = _build_track_specs(mk, mapping, timeline)
        result = _write_and_verify(mk, specs, timeline, options.timescale, output)
        result.output = str(output)
        result.mapping_derived = bool(options.derive_mapping and not options.mapping.by_source)
        return result
    finally:
        data.close()
        handle.close()


def _layout(movie_timescale: int, specs: list[mp4.TrackSpec]) -> tuple[bytes, bytes, int]:
    """Compute ftyp+moov with final chunk offsets.

    ``moov`` size is independent of the offset *values* only when stco/co64
    widths do not change, so we iterate to a fixed point and re-check.
    """
    ftyp = mp4.build_ftyp()
    total_payload = sum(sum(spec.sizes) for spec in specs)
    header = mp4.mdat_header(total_payload)
    # Start with an optimistic moov size, iterate until sizes stabilise.
    probe = mp4.build_moov(movie_timescale=movie_timescale, tracks=specs)
    for _ in range(8):
        cursor = len(ftyp) + len(probe) + len(header)
        for spec in specs:
            spec.chunk_offset = cursor
            cursor += sum(spec.sizes)
        candidate = mp4.build_moov(movie_timescale=movie_timescale, tracks=specs)
        if len(candidate) == len(probe):
            probe = candidate
            break
        probe = candidate
    else:
        raise OutputError("moov/offset layout did not converge")
    cursor = len(ftyp) + len(probe) + len(header)
    for spec in specs:
        if spec.chunk_offset != cursor:
            raise OutputError(
                f"track {spec.track_id}: chunk offset {spec.chunk_offset} does not "
                f"match layout position {cursor}"
            )
        cursor += sum(spec.sizes)
    if cursor != len(ftyp) + len(probe) + len(header) + total_payload:
        raise OutputError("mdat layout does not match the sum of sample sizes")
    return ftyp + probe, header, total_payload


def _write_and_verify(
    mk: MatroskaFile,
    specs: list[mp4.TrackSpec],
    timeline: TimelineReport,
    movie_timescale: int,
    output: Path,
) -> RemuxResult:
    plan = _payload_plan(specs, timeline)
    prefix, header, total_payload = _layout(movie_timescale, specs)

    try:
        output.parent.mkdir(parents=True, exist_ok=True)
        fd, temp_name = tempfile.mkstemp(
            prefix=f".{output.name}.", suffix=".tmp", dir=str(output.parent)
        )
    except OSError as exc:
        # Nothing has been written yet, so the existing target is untouched.
        raise OutputError(
            f"cannot create a temporary file next to {output}: {exc.strerror}",
            stage="write",
        ) from exc
    temp_path = Path(temp_name)
    try:
        try:
            sink_context = os.fdopen(fd, "wb")
        except OSError as exc:
            raise OutputError(
                f"cannot open the temporary file for writing: {exc.strerror}",
                stage="write",
            ) from exc
        with sink_context as sink:
            sink.write(prefix)
            sink.write(header)
            written = 0
            for source_track, pairs in plan:
                for sample_index, (offset, length) in enumerate(pairs):
                    # Slice from the mmap: one sample is copied at a time and
                    # released immediately, never the whole media track.
                    chunk = mk.data[offset : offset + length]
                    if len(chunk) != length:
                        raise OutputError(
                            "short read from the input while copying sample payload",
                            offset=offset,
                            track=source_track,
                            sample_index=sample_index,
                            stage="write",
                        )
                    sink.write(chunk)
                    written += length
            if written != total_payload:
                raise OutputError(
                    f"wrote {written} payload bytes but the layout expected "
                    f"{total_payload}",
                    stage="write",
                )
            sink.flush()
            os.fsync(sink.fileno())
        with temp_path.open("rb") as check_handle:
            mapped = mmap.mmap(check_handle.fileno(), 0, access=mmap.ACCESS_READ)
            try:
                inspection = mp4.inspect_mp4(mapped)
            finally:
                mapped.close()
        if not inspection.ok:
            raise VerifyError(
                "written MP4 failed index verification: " + "; ".join(inspection.failures),
                stage="verify",
            )
        expected_samples = sum(s.sample_count for s in timeline.tracks.values())
        if inspection.to_dict()["sample_count"] != expected_samples:
            raise VerifyError(
                f"written file has {inspection.to_dict()['sample_count']} samples "
                f"but {expected_samples} were expected",
                stage="verify",
            )
        try:
            os.replace(temp_path, output)
        except OSError as exc:
            raise OutputError(
                f"cannot replace {output}: {exc.strerror}", stage="write"
            ) from exc
    except BaseException:
        try:
            temp_path.unlink()
        except OSError:
            pass
        raise

    result = RemuxResult(
        output=str(output),
        timescale=movie_timescale,
        box_count=len(inspection.boxes),
        index_ok=inspection.ok,
        file_size=inspection.file_size,
    )
    reports = {t.track: t for t in timeline.tracks.values()}
    codecs = {t.number: t.codec_id for t in mk.tracks}
    for spec in specs:
        report = reports[spec.source_track]
        result.samples += len(spec.sizes)
        result.mdat_offsets[str(spec.track_id)] = spec.chunk_offset
        result.tracks.append(
            {
                "source_track": spec.source_track,
                "track_id": spec.track_id,
                "handler": spec.handler,
                "codec_id": codecs[spec.source_track],
                "sample_entry": mp4.SUPPORTED_MP4_CODECS[codecs[spec.source_track]],
                "samples": len(spec.sizes),
                "first_tick": int(report.ticks[0]),
                "last_tick": int(report.ticks[-1]),
                "last_end_tick": int(report.ticks[-1] + report.durations[-1]),
                **report.stats,
            }
        )
    corrections = {
        key: sum(int(t.stats.get(key, 0)) for t in timeline.tracks.values())
        for key in (
            "clipped",
            "rounded",
            "non_monotonic",
            "zero_duration",
            "gaps",
            "last_duration_fallback",
        )
    }
    result.corrections = corrections
    result.first_tick = min(int(t.ticks[0]) for t in timeline.tracks.values())
    result.last_end_tick = max(
        int(t.ticks[-1] + t.durations[-1]) for t in timeline.tracks.values()
    )
    return result


def verify_file(path: str | Path) -> dict[str, Any]:
    """Re-read a finished MP4 and report box/sample/index evidence."""
    p = Path(path)
    try:
        size = p.stat().st_size
    except OSError as exc:
        raise VerifyError(f"cannot stat {p}: {exc.strerror}", stage="verify") from exc
    if size == 0:
        raise VerifyError(f"{p} is empty", stage="verify")
    with p.open("rb") as handle:
        data = mmap.mmap(handle.fileno(), 0, access=mmap.ACCESS_READ)
        try:
            inspection = mp4.inspect_mp4(data)
            digest = hashlib.sha256(data).hexdigest()
        finally:
            data.close()
    if not inspection.ok:
        raise VerifyError(
            "index verification failed: " + "; ".join(inspection.failures),
            stage="verify",
        )
    payload = inspection.to_dict()
    return {
        "path": str(p),
        "file_size": size,
        "sha256": digest,
        "box_count": payload["box_count"],
        "boxes": payload["boxes"],
        "sample_count": payload["sample_count"],
        "index_ok": payload["index_ok"],
        "tracks": payload["tracks"],
        "failures": payload["failures"],
    }
