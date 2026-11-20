"""Independent, memory-bounded read-back audit of a written MP4.

The output is memory-mapped and its box tree re-parsed from scratch.  Sample
tables are expanded with NumPy (never into per-sample Python objects), then
verified against the source index and the ``mdat`` boundaries.
"""

from __future__ import annotations

import mmap
import os
import struct
from dataclasses import dataclass

import numpy as np

from .matroska import MAX_PACKET_BYTES, ParsedMatroska, TrackSamples
from .timeline import TrackTimeline

UINT32_MAX = (1 << 32) - 1
COMPARE_SLICE = 1 << 20


class AuditError(Exception):
    """The written MP4 failed structural or sample-index verification."""


@dataclass(slots=True)
class Box:
    kind: bytes
    offset: int
    header_size: int
    size: int
    data_offset: int

    @property
    def end(self) -> int:
        return self.offset + self.size


def _read_boxes(data, start: int, end: int) -> list[Box]:
    boxes: list[Box] = []
    cursor = start
    while cursor < end:
        if cursor + 8 > end:
            raise AuditError(f"truncated box header at {cursor}")
        size, kind = struct.unpack_from(">I4s", data, cursor)
        header = 8
        if size == 1:
            if cursor + 16 > end:
                raise AuditError(f"truncated largesize at {cursor}")
            size = struct.unpack_from(">Q", data, cursor + 8)[0]
            header = 16
        elif size == 0:
            size = end - cursor
        if size < header or cursor + size > end:
            raise AuditError(f"box {kind!r} at {cursor} has invalid size {size}")
        boxes.append(Box(kind=kind, offset=cursor, header_size=header, size=size,
                         data_offset=cursor + header))
        cursor += size
    if cursor != end:
        raise AuditError("boxes do not tile their parent exactly")
    return boxes


def _child(boxes: list[Box], kind: bytes) -> Box:
    for item in boxes:
        if item.kind == kind:
            return item
    raise AuditError(f"missing {kind!r} box")


def _u32(data, offset: int) -> int:
    return struct.unpack_from(">I", data, offset)[0]


def _array(data, offset: int, count: int, dtype: str) -> np.ndarray:
    """Read ``count`` big-endian integers at ``offset`` (offset need not align)."""

    raw = bytes(data[offset:offset + np.dtype(dtype).itemsize * count])
    if len(raw) != np.dtype(dtype).itemsize * count:
        raise AuditError("sample table is truncated")
    return np.frombuffer(raw, dtype=dtype).astype(np.int64, copy=True)


def _pair_table(data, offset: int, runs: int, value_dtype: str) -> tuple[np.ndarray, np.ndarray]:
    """Read packed (uint32 count, value) run pairs without per-sample objects."""

    width = np.dtype(value_dtype).itemsize
    stride = 4 + width
    raw = bytes(data[offset:offset + runs * stride])
    if len(raw) != runs * stride:
        raise AuditError("sample table is truncated")
    if runs == 0:
        return np.zeros(0, dtype=np.int64), np.zeros(0, dtype=np.int64)
    structured = np.frombuffer(raw, dtype=np.dtype([("count", ">u4"),
                                                    ("value", value_dtype)]))
    return (structured["count"].astype(np.int64),
            structured["value"].astype(np.int64))


def _runs_to_values(counts: np.ndarray, values: np.ndarray) -> np.ndarray:
    if counts.size == 0:
        return np.zeros(0, dtype=np.int64)
    if np.any(counts <= 0):
        raise AuditError("sample table contains a zero-length run")
    if values.size and values.max() > UINT32_MAX:
        raise AuditError("sample table value exceeds uint32")
    return np.repeat(values, counts)


def _version(data, box: Box) -> int:
    return int(data[box.data_offset])


def _parse_stbl(data, stbl: Box) -> dict:
    boxes = _read_boxes(data, stbl.data_offset, stbl.end)
    stsd = _child(boxes, b"stsd")
    if _u32(data, stsd.data_offset + 4) != 1:
        raise AuditError("stsd must contain exactly one sample entry")
    entry = _read_boxes(data, stsd.data_offset + 8, stsd.end)[0]
    if entry.kind not in (b"avc1", b"mp4a"):
        raise AuditError(f"unexpected sample entry {entry.kind!r}")

    stts = _child(boxes, b"stts")
    runs = _u32(data, stts.data_offset + 4)
    if stts.data_offset + 8 + runs * 8 != stts.end:
        raise AuditError("stts payload size mismatch")
    counts, deltas = _pair_table(data, stts.data_offset + 8, runs, ">u4")
    durations = _runs_to_values(counts, deltas)

    ctts_box = next((b for b in boxes if b.kind == b"ctts"), None)
    offsets = np.zeros(0, dtype=np.int64)
    ctts_version = None
    if ctts_box is not None:
        ctts_version = _version(data, ctts_box)
        runs = _u32(data, ctts_box.data_offset + 4)
        if ctts_box.data_offset + 8 + runs * 8 != ctts_box.end:
            raise AuditError("ctts payload size mismatch")
        dtype = ">i4" if ctts_version == 1 else ">u4"
        counts, values = _pair_table(data, ctts_box.data_offset + 8, runs, dtype)
        offsets = _runs_to_values(counts, values)

    stsz = _child(boxes, b"stsz")
    fixed = _u32(data, stsz.data_offset + 4)
    sample_count = _u32(data, stsz.data_offset + 8)
    if sample_count > 5_000_000:
        raise AuditError("stsz sample count exceeds limit")
    if fixed:
        sizes = np.full(sample_count, fixed, dtype=np.int64)
    else:
        if stsz.data_offset + 12 + sample_count * 4 != stsz.end:
            raise AuditError("stsz payload size mismatch")
        sizes = _array(data, stsz.data_offset + 12, sample_count, ">u4")

    stsc = _child(boxes, b"stsc")
    entries = _u32(data, stsc.data_offset + 4)
    if stsc.data_offset + 8 + entries * 12 != stsc.end:
        raise AuditError("stsc payload size mismatch")
    stsc_runs = []
    for index in range(entries):
        first, per, desc = struct.unpack_from(
            ">III", data, stsc.data_offset + 8 + index * 12)
        if desc != 1:
            raise AuditError("stsc references an unknown sample description")
        stsc_runs.append((first, per))

    stco_box = next((b for b in boxes if b.kind in (b"stco", b"co64")), None)
    if stco_box is None:
        raise AuditError("missing stco/co64")
    count = _u32(data, stco_box.data_offset + 4)
    width = 4 if stco_box.kind == b"stco" else 8
    if stco_box.data_offset + 8 + count * width != stco_box.end:
        raise AuditError("stco/co64 payload size mismatch")
    chunks = _array(data, stco_box.data_offset + 8, count,
                    ">u4" if width == 4 else ">u8")

    stss_box = next((b for b in boxes if b.kind == b"stss"), None)
    sync = None
    if stss_box is not None:
        count = _u32(data, stss_box.data_offset + 4)
        if stss_box.data_offset + 8 + count * 4 != stss_box.end:
            raise AuditError("stss payload size mismatch")
        sync = _array(data, stss_box.data_offset + 8, count, ">u4")
    return {"kind": entry.kind, "durations": durations, "ctts": offsets,
            "ctts_version": ctts_version, "sizes": sizes, "stsc": stsc_runs,
            "chunks": chunks, "sync": sync}


def _expand_chunks(stsc: list[tuple[int, int]], chunk_count: int) -> np.ndarray:
    if chunk_count == 0:
        if stsc:
            raise AuditError("stsc entries present with zero chunks")
        return np.zeros(0, dtype=np.int64)
    if not stsc or stsc[0][0] != 1:
        raise AuditError("stsc does not start at chunk 1")
    per_chunk = []
    for index, (first, per) in enumerate(stsc):
        limit = stsc[index + 1][0] if index + 1 < len(stsc) else chunk_count + 1
        if first < 1 or first > chunk_count or limit <= first:
            raise AuditError("stsc chunk runs overlap or are unordered")
        per_chunk.extend([per] * (limit - first))
    if len(per_chunk) != chunk_count:
        raise AuditError("stsc does not cover every chunk per chunk")
    return np.asarray(per_chunk, dtype=np.int64)


def _parse_moov_tracks(data, moov: Box, file_end: int) -> tuple[list[dict], int]:
    boxes = _read_boxes(data, moov.data_offset, moov.end)
    mvhd = _child(boxes, b"mvhd")
    version = _version(data, mvhd)
    timescale = _u32(data, mvhd.data_offset + (20 if version else 12))
    if timescale <= 0:
        raise AuditError("mvhd timescale is zero")
    if moov.end != file_end:
        raise AuditError("trailing bytes after moov")
    tracks = []
    seen_ids: set[int] = set()
    for trak in (b for b in boxes if b.kind == b"trak"):
        children = _read_boxes(data, trak.data_offset, trak.end)
        tkhd = _child(children, b"tkhd")
        version = _version(data, tkhd)
        track_id = _u32(data, tkhd.data_offset + (20 if version else 12))
        if track_id == 0 or track_id in seen_ids:
            raise AuditError(f"duplicate or zero track ID {track_id}")
        seen_ids.add(track_id)
        mdia = _child(children, b"mdia")
        mdia_boxes = _read_boxes(data, mdia.data_offset, mdia.end)
        mdhd = _child(mdia_boxes, b"mdhd")
        version = _version(data, mdhd)
        md_timescale = _u32(data, mdhd.data_offset + (20 if version else 12))
        if md_timescale != timescale:
            raise AuditError(f"track {track_id} timescale differs from mvhd")
        minf = _child(mdia_boxes, b"minf")
        stbl = _child(_read_boxes(data, minf.data_offset, minf.end), b"stbl")
        entry = _parse_stbl(data, stbl)
        entry["track_id"] = track_id
        entry["edits"] = _parse_edits(children, data)
        hdlr = _child(mdia_boxes, b"hdlr")
        entry["handler"] = bytes(data[hdlr.data_offset + 8:hdlr.data_offset + 12])
        tracks.append(entry)
    return tracks, timescale


def _parse_edits(children: list[Box], data) -> list[tuple[int, int]]:
    edts = next((box for box in children if box.kind == b"edts"), None)
    if edts is None:
        return []
    elst = _child(_read_boxes(data, edts.data_offset, edts.end), b"elst")
    version = _version(data, elst)
    count = _u32(data, elst.data_offset + 4)
    width = 8 if version else 4
    if elst.data_offset + 8 + count * (width * 2 + 4) != elst.end:
        raise AuditError("elst payload size mismatch")
    entries = []
    cursor = elst.data_offset + 8
    for _ in range(count):
        if version:
            duration, media_time = struct.unpack_from(">Qq", data, cursor)
        else:
            duration, media_time = struct.unpack_from(">Ii", data, cursor)
        entries.append((int(duration), int(media_time)))
        cursor += width * 2 + 4
    return entries


def _sample_offsets(chunks: np.ndarray, per_chunk: np.ndarray,
                    sizes: np.ndarray) -> np.ndarray:
    """Absolute file offset of every sample, derived from stsc/stco/stsz."""

    count = int(per_chunk.sum())
    if count != sizes.size:
        raise AuditError(f"stsc covers {count} samples, stsz has {sizes.size}")
    cumulative = np.cumsum(sizes)
    first = np.cumsum(per_chunk) - per_chunk
    chunk_base = cumulative[first] - sizes[first]
    within = cumulative - sizes - np.repeat(chunk_base, per_chunk)
    return np.repeat(chunks, per_chunk) + within


def _frame_spans(offsets: np.ndarray, sizes: np.ndarray) -> list[tuple[int, int, int, int]]:
    """Group adjacent source frames into contiguous read spans."""

    if offsets.size == 0:
        return []
    contiguous = offsets[1:] == offsets[:-1] + sizes[:-1]
    cuts = np.flatnonzero(~contiguous) + 1
    starts = np.concatenate(([0], cuts))
    ends = np.concatenate((cuts, [offsets.size]))
    return [(int(offsets[a]), int(offsets[b - 1] + sizes[b - 1]), int(a), int(b))
            for a, b in zip(starts.tolist(), ends.tolist())]


def _compare_bytes(mm, source, mp4_offsets: np.ndarray, source_offsets: np.ndarray,
                   sizes: np.ndarray, track_id: int) -> None:
    for src_start, src_end, first, last in _frame_spans(source_offsets, sizes):
        source.seek(src_start)
        remaining = src_end - src_start
        read_at = src_start
        while remaining:
            take = min(COMPARE_SLICE, remaining)
            original = source.read(take)
            if len(original) != take:
                raise AuditError("source frame is truncated during audit")
            mp4_at = int(mp4_offsets[first]) + (read_at - src_start)
            if mm[mp4_at:mp4_at + take] != original:
                raise AuditError(f"track {track_id}: frame bytes for samples "
                                 f"{first + 1}..{last} differ from source")
            read_at += take
            remaining -= take


def audit_mp4(path: str, source_path: str, samples: dict[int, TrackSamples],
              tracks: list[TrackTimeline], timescale: int) -> dict:
    """Read the output back and verify counts, mapping, offsets and mdat."""

    with open(path, "rb") as handle, open(source_path, "rb") as source:
        size = os.path.getsize(path)
        mm = mmap.mmap(handle.fileno(), 0, access=mmap.ACCESS_READ)
        try:
            return _audit(mm, size, source, samples, tracks, timescale)
        finally:
            mm.close()


def _audit(mm, file_size: int, source, samples: dict[int, TrackSamples],
           timeline_tracks: list[TrackTimeline], timescale: int) -> dict:
    top = _read_boxes(mm, 0, file_size)
    if not top or top[0].kind != b"ftyp":
        raise AuditError("output does not start with ftyp")
    mdat = _child(top, b"mdat")
    moov = _child(top, b"moov")
    if mdat.end > moov.offset:
        raise AuditError("moov must follow mdat (non-fragmented layout)")
    tracks, movie_timescale = _parse_moov_tracks(mm, moov, file_size)
    if movie_timescale != timescale:
        raise AuditError(f"output timescale {movie_timescale} != {timescale}")
    if len(tracks) != len(timeline_tracks):
        raise AuditError(f"track count {len(tracks)} != {len(timeline_tracks)}")
    mdat_bytes = mdat.end - mdat.data_offset
    total_sizes = 0
    total_samples = 0
    track_map = []
    for expected, entry in zip(timeline_tracks, tracks):
        track_id = entry["track_id"]
        if track_id != expected.mp4_track_id:
            raise AuditError(f"track ID {track_id} != {expected.mp4_track_id}")
        wanted_handler = expected.track.handler.encode("ascii")
        if entry["handler"] != wanted_handler:
            raise AuditError(f"track {track_id}: handler {entry['handler']!r} != "
                             f"{wanted_handler!r}")
        wanted_kind = b"avc1" if wanted_handler == b"vide" else b"mp4a"
        if entry["kind"] != wanted_kind:
            raise AuditError(f"track {track_id}: codec box {entry['kind']!r} != "
                             f"{wanted_kind!r}")
        expected_edits = []
        if expected.media_base > 0:
            expected_edits.append((expected.media_base, -1))
        expected_edits.append((expected.duration_ticks, 0))
        if entry["edits"] != expected_edits:
            raise AuditError(f"track {track_id}: edit list {entry['edits']} != "
                             f"{expected_edits}")
        sizes, durations = entry["sizes"], entry["durations"]
        if sizes.size != expected.sample_count:
            raise AuditError(f"track {track_id}: stsz count {sizes.size} != "
                             f"{expected.sample_count}")
        if durations.size != expected.sample_count:
            raise AuditError(f"track {track_id}: stts count {durations.size} != "
                             f"{expected.sample_count}")
        if not np.array_equal(sizes, expected.size):
            raise AuditError(f"track {track_id}: stsz sizes differ")
        if not np.array_equal(durations, expected.duration):
            raise AuditError(f"track {track_id}: stts deltas differ")
        offsets = entry["ctts"]
        if expected.has_ctts:
            if offsets.size == 0:
                raise AuditError(f"track {track_id}: ctts missing")
            if entry["ctts_version"] != 1:
                raise AuditError(f"track {track_id}: ctts is not version 1")
            if not np.array_equal(offsets, expected.ctts):
                raise AuditError(f"track {track_id}: ctts offsets differ")
        elif offsets.size and bool(np.any(offsets)):
            raise AuditError(f"track {track_id}: unexpected ctts")
        per_chunk = _expand_chunks(entry["stsc"], entry["chunks"].size)
        mp4_offsets = _sample_offsets(entry["chunks"], per_chunk, sizes)
        if sizes.size and (mp4_offsets.min() < mdat.data_offset or
                           (mp4_offsets + sizes).max() > mdat.end):
            raise AuditError(f"track {track_id}: sample outside mdat")
        chunk_first = np.cumsum(per_chunk) - per_chunk
        chunk_last = chunk_first + per_chunk - 1
        if per_chunk.size > 1:
            ends = mp4_offsets[chunk_last[:-1]] + sizes[chunk_last[:-1]]
            if np.any(entry["chunks"][1:] < ends):
                raise AuditError(f"track {track_id}: overlapping chunks")
        if entry["sync"] is not None:
            synced = np.zeros(expected.sample_count, dtype=bool)
            sync = entry["sync"]
            if sync.size and (sync.min() < 1 or sync.max() > expected.sample_count):
                raise AuditError(f"track {track_id}: bad stss index")
            synced[sync - 1] = True
            if not np.array_equal(synced, expected.keyframe):
                raise AuditError(f"track {track_id}: stss differs")
        elif not bool(np.all(expected.keyframe)):
            raise AuditError(f"track {track_id}: missing stss")
        source_index = samples[expected.track.number]
        _compare_bytes(mm, source, mp4_offsets, source_index.offset, sizes, track_id)
        total_sizes += int(sizes.sum())
        total_samples += expected.sample_count
        track_map.append({
            "mkv_track": expected.track.number,
            "mp4_track_id": expected.mp4_track_id,
            "codec": entry["kind"].decode("ascii"),
            "sample_count": expected.sample_count,
            "first_pts": expected.first_pts,
            "last_pts": expected.last_pts,
            "ctts": expected.has_ctts,
        })
    if total_sizes != mdat_bytes:
        raise AuditError(f"mdat holds {mdat_bytes} bytes but tables describe "
                         f"{total_sizes}")
    if total_samples > 5_000_000:
        raise AuditError("audited sample count exceeds limit")
    return {
        "dur": {"mdat_offset": mdat.offset, "mdat_end": mdat.end,
                "mdat_size": mdat.size, "moov_offset": moov.offset,
                "file_size": file_size},
        "track_map": track_map, "sample_count": total_samples,
        "timescale": movie_timescale,
    }
