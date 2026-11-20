"""Independent read-back audit of a written MP4.

The audit re-parses the output with the same low-level box reader, rebuilds the
sample index from ``stts``/``ctts``/``stsc``/``stsz``/``stss``/``stco``/``co64``
and verifies it against the source frames and the ``mdat`` boundaries.
"""

from __future__ import annotations

import struct
from dataclasses import dataclass

import numpy as np

from .matroska import MAX_PACKET_BYTES, ParsedMatroska
from .timeline import Timeline

UINT64_MAX = (1 << 64) - 1


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


def _read_boxes(data: bytes, start: int, end: int) -> list[Box]:
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


def _children(boxes: list[Box], kind: bytes) -> list[Box]:
    return [item for item in boxes if item.kind == kind]


def _u32(data: bytes, offset: int) -> int:
    return struct.unpack_from(">I", data, offset)[0]


def _u64(data: bytes, offset: int) -> int:
    return struct.unpack_from(">Q", data, offset)[0]


def _full_header(data: bytes, box: Box) -> tuple[int, int]:
    return data[box.data_offset], int.from_bytes(
        data[box.data_offset + 1:box.data_offset + 4], "big")


def _parse_stbl(data: bytes, stbl: Box) -> dict:
    boxes = _read_boxes(data, stbl.data_offset, stbl.end)
    stsd = _child(boxes, b"stsd")
    entry_count = _u32(data, stsd.data_offset + 4)
    if entry_count != 1:
        raise AuditError(f"stsd entry count {entry_count} != 1")
    entry = _read_boxes(data, stsd.data_offset + 8, stsd.end)[0]
    if entry.kind not in (b"avc1", b"mp4a"):
        raise AuditError(f"unexpected sample entry {entry.kind!r}")
    stts = _child(boxes, b"stts")
    count = _u32(data, stts.data_offset + 4)
    durations: list[int] = []
    pos = stts.data_offset + 8
    for _ in range(count):
        run, delta = _u32(data, pos), _u32(data, pos + 4)
        if run == 0 or delta == 0:
            raise AuditError("stts contains a zero run or delta")
        durations.extend([delta] * run)
        pos += 8
    if pos != stts.end:
        raise AuditError("stts payload size mismatch")
    ctts_box = next((b for b in boxes if b.kind == b"ctts"), None)
    offsets: list[int] = []
    ctts_version = 0
    if ctts_box is not None:
        ctts_version, _ = _full_header(data, ctts_box)
        count = _u32(data, ctts_box.data_offset + 4)
        pos = ctts_box.data_offset + 8
        for _ in range(count):
            run = _u32(data, pos)
            if ctts_version == 1:
                offset = struct.unpack_from(">i", data, pos + 4)[0]
            else:
                offset = _u32(data, pos + 4)
            offsets.extend([offset] * run)
            pos += 8
        if pos != ctts_box.end:
            raise AuditError("ctts payload size mismatch")
    stsz = _child(boxes, b"stsz")
    fixed = _u32(data, stsz.data_offset + 4)
    sample_count = _u32(data, stsz.data_offset + 8)
    if fixed:
        sizes = [fixed] * sample_count
    else:
        sizes = list(struct.unpack_from(f">{sample_count}I", data,
                                        stsz.data_offset + 12))
    stsc = _child(boxes, b"stsc")
    entries = _u32(data, stsc.data_offset + 4)
    per_chunk: list[tuple[int, int]] = []
    pos = stsc.data_offset + 8
    for _ in range(entries):
        first, per, desc = struct.unpack_from(">III", data, pos)
        if desc != 1:
            raise AuditError("stsc references an unknown sample description")
        per_chunk.append((first, per))
        pos += 12
    stco_box = next((b for b in boxes if b.kind in (b"stco", b"co64")), None)
    if stco_box is None:
        raise AuditError("missing stco/co64")
    count = _u32(data, stco_box.data_offset + 4)
    if stco_box.kind == b"stco":
        chunk_offsets = list(struct.unpack_from(f">{count}I", data,
                                                stco_box.data_offset + 8))
    else:
        chunk_offsets = list(struct.unpack_from(f">{count}Q", data,
                                                stco_box.data_offset + 8))
    stss_box = next((b for b in boxes if b.kind == b"stss"), None)
    sync: set[int] | None = None
    if stss_box is not None:
        count = _u32(data, stss_box.data_offset + 4)
        sync = set(struct.unpack_from(f">{count}I", data, stss_box.data_offset + 8))
    return {"kind": entry.kind, "durations": durations, "ctts": offsets,
            "ctts_version": ctts_version, "sizes": sizes, "stsc": per_chunk,
            "chunks": chunk_offsets, "sync": sync}


def _expand_chunks(stsc: list[tuple[int, int]], chunk_count: int) -> list[int]:
    if chunk_count == 0:
        if stsc:
            raise AuditError("stsc entries present with zero chunks")
        return []
    if not stsc or stsc[0][0] != 1:
        raise AuditError("stsc does not start at chunk 1")
    per_chunk: list[int] = []
    for index, (first, per) in enumerate(stsc):
        limit = stsc[index + 1][0] if index + 1 < len(stsc) else chunk_count + 1
        if first < 1 or first > chunk_count or limit <= first:
            raise AuditError("stsc chunk runs overlap or are unordered")
        per_chunk.extend([per] * (limit - first))
    if len(per_chunk) != chunk_count:
        raise AuditError("stsc does not cover every chunk")
    return per_chunk


def _parse_moov_tracks(data: bytes, moov: Box) -> tuple[list[dict], int]:
    boxes = _read_boxes(data, moov.data_offset, moov.end)
    mvhd = _child(boxes, b"mvhd")
    version, _ = _full_header(data, mvhd)
    timescale = _u32(data, mvhd.data_offset + (20 if version else 12))
    if timescale <= 0:
        raise AuditError("mvhd timescale is zero")
    tracks = []
    for trak in _children(boxes, b"trak"):
        children = _read_boxes(data, trak.data_offset, trak.end)
        tkhd = _child(children, b"tkhd")
        version, _ = _full_header(data, tkhd)
        track_id = _u32(data, tkhd.data_offset + (20 if version else 12))
        mdia = _child(children, b"mdia")
        mdia_boxes = _read_boxes(data, mdia.data_offset, mdia.end)
        mdhd = _child(mdia_boxes, b"mdhd")
        version, _ = _full_header(data, mdhd)
        md_timescale = _u32(data, mdhd.data_offset + (20 if version else 12))
        if md_timescale != timescale:
            raise AuditError(f"track {track_id} timescale differs from mvhd")
        minf = _child(mdia_boxes, b"minf")
        minf_boxes = _read_boxes(data, minf.data_offset, minf.end)
        stbl = _child(minf_boxes, b"stbl")
        entry = _parse_stbl(data, stbl)
        entry.update(track_id=track_id, mdia=mdia_boxes, children=children)
        tracks.append(entry)
    return tracks, timescale


def audit_mp4(path: str, mkv: ParsedMatroska, timeline: Timeline) -> dict:
    """Read the output back and verify counts, mapping, offsets and mdat."""

    with open(path, "rb") as handle:
        data = handle.read()
    source = open(mkv.path, "rb")
    try:
        return _audit_loaded(data, source, mkv, timeline, path)
    finally:
        source.close()


def _audit_loaded(data: bytes, source, mkv: ParsedMatroska, timeline: Timeline,
                  path: str) -> dict:
    top = _read_boxes(data, 0, len(data))
    if top[0].kind != b"ftyp":
        raise AuditError("output does not start with ftyp")
    mdat = _child(top, b"mdat")
    moov = _child(top, b"moov")
    if mdat.end > moov.offset:
        raise AuditError("moov must follow mdat (non-fragmented layout)")
    if moov.end != len(data):
        raise AuditError("trailing bytes after moov")
    tracks, timescale = _parse_moov_tracks(data, moov)
    if timescale != timeline.timescale:
        raise AuditError(f"output timescale {timescale} != {timeline.timescale}")
    if len(tracks) != len(timeline.tracks):
        raise AuditError(f"track count {len(tracks)} != {len(timeline.tracks)}")
    total_samples = 0
    track_map = []
    for expected, entry in zip(timeline.tracks, tracks):
        if entry["track_id"] != expected.mp4_track_id:
            raise AuditError(f"track ID {entry['track_id']} != {expected.mp4_track_id}")
        sizes = entry["sizes"]
        durations = entry["durations"]
        offsets = entry["ctts"]
        if len(sizes) != expected.sample_count:
            raise AuditError(f"track {entry['track_id']}: stsz count "
                             f"{len(sizes)} != {expected.sample_count}")
        if len(durations) != expected.sample_count:
            raise AuditError(f"track {entry['track_id']}: stts count mismatch")
        if not np.array_equal(np.asarray(sizes, dtype=np.int64), expected.size):
            raise AuditError(f"track {entry['track_id']}: stsz sizes differ")
        if not np.array_equal(np.asarray(durations, dtype=np.int64), expected.duration):
            raise AuditError(f"track {entry['track_id']}: stts deltas differ")
        if expected.has_ctts:
            if not offsets:
                raise AuditError(f"track {entry['track_id']}: ctts missing")
            if entry["ctts_version"] != 1:
                raise AuditError(f"track {entry['track_id']}: ctts is not version 1")
            if not np.array_equal(np.asarray(offsets, dtype=np.int64), expected.ctts):
                raise AuditError(f"track {entry['track_id']}: ctts offsets differ")
        elif offsets and any(offsets):
            raise AuditError(f"track {entry['track_id']}: unexpected ctts")
        per_chunk = _expand_chunks(entry["stsc"], len(entry["chunks"]))
        if sum(per_chunk) != expected.sample_count:
            raise AuditError(f"track {entry['track_id']}: stsc sample count mismatch")
        if entry["sync"] is not None:
            synced = np.zeros(expected.sample_count, dtype=bool)
            for index in entry["sync"]:
                if index < 1 or index > expected.sample_count:
                    raise AuditError(f"track {entry['track_id']}: bad stss index")
                synced[index - 1] = True
            if not np.array_equal(synced, expected.keyframe):
                raise AuditError(f"track {entry['track_id']}: stss differs")
        elif not bool(np.all(expected.keyframe)):
            raise AuditError(f"track {entry['track_id']}: missing stss")
        sample = 0
        previous_chunk_end = mdat.data_offset
        for chunk_index, (chunk_offset, count) in enumerate(
                zip(entry["chunks"], per_chunk)):
            if chunk_index and chunk_offset < previous_chunk_end:
                raise AuditError(f"track {entry['track_id']}: overlapping chunks")
            if chunk_offset < mdat.data_offset or chunk_offset > mdat.end:
                raise AuditError(f"track {entry['track_id']}: chunk outside mdat")
            cursor = chunk_offset
            for _ in range(count):
                size = sizes[sample]
                if size <= 0 or size > MAX_PACKET_BYTES:
                    raise AuditError("invalid sample size")
                if cursor + size > mdat.end:
                    raise AuditError(f"track {entry['track_id']}: sample "
                                     f"{sample + 1} overruns mdat")
                source_offset = int(expected.offset[sample])
                source.seek(source_offset)
                original = source.read(size)
                if len(original) != size:
                    raise AuditError("source frame is truncated during audit")
                if data[cursor:cursor + size] != original:
                    raise AuditError(f"track {entry['track_id']}: sample "
                                     f"{sample + 1} bytes differ from source")
                cursor += size
                sample += 1
            if cursor > mdat.end:
                raise AuditError("chunk overruns mdat")
            previous_chunk_end = cursor
        if sample != expected.sample_count:
            raise AuditError(f"track {entry['track_id']}: chunk coverage mismatch")
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
    if total_samples > 5_000_000:
        raise AuditError("audited sample count exceeds limit")
    if mdat.size < 8 or mdat.data_offset > mdat.end:
        raise AuditError("mdat is invalid")
    return {
        "dur": {"mdat_offset": mdat.offset, "mdat_end": mdat.end,
                "mdat_size": mdat.size, "moov_offset": moov.offset,
                "file_size": len(data)},
        "track_map": track_map, "sample_count": total_samples,
        "timescale": timescale,
    }

