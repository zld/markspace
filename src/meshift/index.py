"""Read-back verification of the MP4 sample index after serialization."""

from __future__ import annotations

import struct
from dataclasses import dataclass

from .errors import IndexCheckError
from .timeline import NormalizedTrack

_CONTAINERS = {b"moov", b"trak", b"mdia", b"minf", b"stbl", b"dinf", b"udta"}


@dataclass
class Box:
    box_type: bytes
    start: int
    header_len: int
    end: int
    data: bytes
    children: list["Box"]

    @property
    def payload(self) -> bytes:
        return self.data[self.start + self.header_len:self.end]

    def find(self, box_type: bytes) -> "Box | None":
        return next((c for c in self.children if c.box_type == box_type), None)

    def find_all(self, box_type: bytes) -> list["Box"]:
        return [c for c in self.children if c.box_type == box_type]


def _iter_boxes(data: bytes, start: int, end: int) -> list[Box]:
    boxes: list[Box] = []
    pos = start
    while pos < end:
        if end - pos < 8:
            raise IndexCheckError(f"trailing {end - pos} bytes in box stream")
        size = struct.unpack_from(">I", data, pos)[0]
        box_type = data[pos + 4:pos + 8]
        header_len = 8
        if size == 1:
            size = struct.unpack_from(">Q", data, pos + 8)[0]
            header_len = 16
        elif size == 0:
            size = end - pos
        if size < header_len or pos + size > end:
            raise IndexCheckError(
                f"box {box_type!r} declares invalid size {size} at {pos}")
        box = Box(box_type, pos, header_len, pos + size, data, [])
        if box_type in _CONTAINERS:
            box.children = _iter_boxes(data, pos + header_len, pos + size)
        boxes.append(box)
        pos += size
    return boxes


def parse_mp4(data: bytes) -> list[Box]:
    return _iter_boxes(data, 0, len(data))


def _fullbox_head(box: Box) -> tuple[int, int]:
    payload = box.payload
    if len(payload) < 4:
        raise IndexCheckError(f"{box.box_type!r} missing fullbox header")
    version = payload[0]
    flags = struct.unpack(">I", payload)[0] & 0x00FFFFFF
    return version, flags


def _read_table(box: Box, width: int) -> list[tuple[int, ...]]:
    payload = box.payload[4:]
    count = struct.unpack_from(">I", payload, 0)[0]
    entries = []
    pos = 4
    for _ in range(count):
        if width == 2:
            entries.append(struct.unpack_from(">iI", payload, pos))
            pos += 8
        else:
            entries.append((struct.unpack_from(">I", payload, pos)[0],))
            pos += 4
    return entries


def verify_index(data: bytes, tracks: list[NormalizedTrack],
                 mdat_start: int, mdat_end: int) -> list[dict]:
    """Rebuild the index from *data* and compare it against *tracks*."""
    roots = parse_mp4(data)
    moov = next((b for b in roots if b.box_type == b"moov"), None)
    mdat = next((b for b in roots if b.box_type == b"mdat"), None)
    if moov is None or mdat is None:
        raise IndexCheckError("moov or mdat box missing")
    traks = moov.find_all(b"trak")
    if len(traks) != len(tracks):
        raise IndexCheckError(
            f"track count {len(traks)} != expected {len(tracks)}")

    index_entries: list[dict] = []
    for trak, track in zip(traks, tracks):
        stbl = trak.find(b"mdia").find(b"minf").find(b"stbl")
        stsz = stbl.find(b"stsz")
        stts = stbl.find(b"stts")
        ctts = stbl.find(b"ctts")
        co64 = stbl.find(b"co64")
        if None in (stsz, stts, ctts, co64):
            raise IndexCheckError("required sample table box missing")

        size_payload = stsz.payload[4:]
        sample_sizes = list(struct.unpack_from(
            f">{len(track.samples)}I", size_payload, 8))
        if sample_sizes != [len(s.payload) for s in track.samples]:
            raise IndexCheckError(
                f"track {track.output_number}: stsz size mismatch")

        offsets = [struct.unpack_from(
            ">Q", co64.payload, 12 + 8 * idx)[0]
            for idx in range(len(track.samples))]
        if any(not (mdat_start <= off < mdat_end) for off in offsets):
            raise IndexCheckError(
                f"track {track.output_number}: chunk offset outside mdat")

        for idx, (offset, size, sample) in enumerate(
                zip(offsets, sample_sizes, track.samples)):
            if data[offset:offset + size] != sample.payload:
                raise IndexCheckError(
                    f"track {track.output_number} sample {idx}: payload "
                    "bytes at index offset do not match")
            index_entries.append({
                "track": track.output_number,
                "sample": idx + 1,
                "offset": offset,
                "size": size,
            })

        stts_entries = _read_table(stts, width=2)
        expanded_duration: list[int] = []
        for count, delta in stts_entries:
            expanded_duration.extend([delta] * count)
        if expanded_duration != [s.duration_ns for s in track.samples]:
            raise IndexCheckError(
                f"track {track.output_number}: stts duration mismatch")

        version, _ = _fullbox_head(ctts)
        if version != 1:
            raise IndexCheckError("ctts must use version 1 for signed offsets")
        ctts_entries = _read_table(ctts, width=2)
        expanded_offsets: list[int] = []
        for count, value in ctts_entries:
            expanded_offsets.extend([value] * count)
        if expanded_offsets != [s.composition_offset_ns
                                for s in track.samples]:
            raise IndexCheckError(
                f"track {track.output_number}: ctts offset mismatch")

    total = sum(len(t.samples) for t in tracks)
    if len(index_entries) != total:
        raise IndexCheckError(
            f"index entry count {len(index_entries)} != sample count {total}")
    if not index_entries:
        raise IndexCheckError("index contains no entries")
    return index_entries
