"""Read-only validation: index offsets, reference ranges, sample counts,
and per-track durations compared as exact rationals (Fraction), never
floats, so mixed-timescale multi-track assets compare cleanly.
"""
from __future__ import annotations

import struct
from dataclasses import dataclass, field
from fractions import Fraction

from .boxes import MAX_FILE_SIZE, StructuralError, find_child, parse_boxes
from .fragments import covered_by_ranges, orphan_mdats, scan_fragments
from .sidx import parse_sidx
from .stbl import (parse_stco, parse_stsc, parse_stsz, parse_stts,
                   samples_per_chunk)

MAX_DECODE_TIME = (1 << 63) - 1


@dataclass
class TrackInfo:
    track_id: int
    timescale: int
    duration_ticks: int
    duration: Fraction
    stts_samples: int
    stsz_samples: int
    chunk_offsets: list
    chunk_sizes: list
    errors: list = field(default_factory=list)

    @property
    def sample_entries(self):
        return self.stsz_samples

    @property
    def chunk_count(self):
        return len(self.chunk_offsets)


@dataclass
class Analysis:
    path: str
    data: bytes
    top_boxes: list
    tracks: list
    sidxes: list
    sidx_errors: list
    fragments: list
    warnings: list
    errors: list

    @property
    def sidx_offset_valid(self):
        if not self.sidxes or self.sidx_errors:
            return False
        ranges = [r for s in self.sidxes for r in s.reference_ranges()]
        closed = [(f.start_offset, f.end_offset)
                  for f in self.fragments if f.closed]
        return ranges == closed

    def report(self):
        return {
            "file": self.path,
            "file_size": len(self.data),
            "tracks": [{
                "track_id": t.track_id,
                "timescale": t.timescale,
                "sample_entries": t.sample_entries,
                "stts_samples": t.stts_samples,
                "chunk_count": t.chunk_count,
                "duration_ticks": t.duration_ticks,
                "duration_seconds": round(float(t.duration), 6),
                "consistent": not t.errors,
                "errors": list(t.errors),
            } for t in self.tracks],
            "sidx_offset_valid": self.sidx_offset_valid,
            "sidx": {
                "present": bool(self.sidxes),
                "count": len(self.sidxes),
                "references": sum(len(s.references) for s in self.sidxes),
                "hierarchical_references": sum(
                    1 for s in self.sidxes for r in s.references
                    if r.reference_type == 1),
                "errors": list(self.sidx_errors),
            },
            "fragments": [{
                "index": f.index,
                "start_offset": f.start_offset,
                "end_offset": f.end_offset,
                "byte_length": f.byte_length,
                "closed": f.closed,
                "note": f.note,
            } for f in self.fragments],
            "warnings": list(self.warnings),
            "errors": list(self.errors),
        }


def _parse_track(data, trak, file_size):
    errors = []
    track_id, timescale = 0, 0
    tkhd = find_child(trak, "tkhd")
    if tkhd is not None:
        payload = data[tkhd.data_offset:tkhd.end]
        off = 20 if payload[:1] == b"\x01" else 12
        if len(payload) >= off + 4:
            (track_id,) = struct.unpack_from(">I", payload, off)
        else:
            errors.append("tkhd too short to hold track_ID")
    else:
        errors.append("trak has no tkhd")
    mdia = find_child(trak, "mdia")
    mdhd = find_child(mdia, "mdhd") if mdia else None
    if mdhd is not None:
        payload = data[mdhd.data_offset:mdhd.end]
        off = 20 if payload[:1] == b"\x01" else 12
        if len(payload) >= off + 4:
            (timescale,) = struct.unpack_from(">I", payload, off)
        else:
            errors.append(f"track {track_id}: mdhd too short for timescale")
    else:
        errors.append(f"track {track_id}: trak has no mdhd")
    if mdia is None:
        errors.append(f"track {track_id}: trak has no mdia")

    stts_n = stsz_n = 0
    ticks = 0
    chunk_offsets, chunk_sizes = [], []
    stbl = None
    if mdia is not None:
        minf = find_child(mdia, "minf")
        stbl = find_child(minf, "stbl") if minf else None
    if stbl is not None:
        stts = parse_stts(data, find_child(stbl, "stts"))
        stsc = parse_stsc(data, find_child(stbl, "stsc"))
        stsz = parse_stsz(data, find_child(stbl, "stsz"))
        co = find_child(stbl, "stco") or find_child(stbl, "co64")
        if co is None:
            errors.append(f"track {track_id}: stbl has no stco/co64")
            offsets = ChunkOffsetsShim([])
        else:
            offsets = parse_stco(data, co)
        for i, (count, delta) in enumerate(stts.entries):
            ticks += count * delta
            if ticks > MAX_DECODE_TIME:
                errors.append(
                    f"timestamp overflow: track {track_id} stts entry {i} "
                    f"pushes cumulative decode time past 2^63-1")
                break
        stts_n, stsz_n = stts.sample_count, stsz.sample_count
        if stts_n != stsz_n:
            errors.append(
                f"sample count mismatch: track {track_id} stts expands to "
                f"{stts_n} samples but stsz declares {stsz_n}")
        per_chunk = samples_per_chunk(stsc.entries, len(offsets.offsets))
        if sum(per_chunk) != stsz_n:
            errors.append(
                f"sample count mismatch: track {track_id} stsc maps "
                f"{sum(per_chunk)} samples over chunks but stsz declares {stsz_n}")
        chunk_offsets = offsets.offsets
        if stsz.sample_size:
            chunk_sizes = [stsz.sample_size * n for n in per_chunk]
        else:
            chunk_sizes, pos = [], 0
            for n in per_chunk:
                chunk_sizes.append(sum(stsz.entries[pos:pos + n]))
                pos += n
        for k, (off, size) in enumerate(zip(chunk_offsets, chunk_sizes)):
            if off + size > file_size:
                errors.append(
                    f"chunk {k} of track {track_id} at offset {off} with "
                    f"{size} bytes exceeds file size {file_size}")
    if timescale == 0:
        errors.append(f"track {track_id}: timescale is zero")
    duration = Fraction(ticks, timescale) if timescale else Fraction(0, 1)
    return TrackInfo(track_id, timescale, ticks, duration,
                     stts_n, stsz_n, chunk_offsets, chunk_sizes, errors)


class ChunkOffsetsShim:
    def __init__(self, offsets):
        self.offsets = offsets


def analyze(path):
    """Parse and validate a file read-only; returns an Analysis.

    Raises StructuralError (exit code 2) for malformed box/table structure;
    recoverable inconsistencies are collected into Analysis.errors instead.
    """
    with open(path, "rb") as fh:
        data = fh.read()
    if len(data) > MAX_FILE_SIZE:
        raise StructuralError(
            f"file size {len(data)} exceeds {MAX_FILE_SIZE} byte limit")
    top = parse_boxes(data)
    if not any(b.type in ("ftyp", "moov") for b in top):
        raise StructuralError("not an ISO BMFF file: no ftyp or moov at top level")

    errors, warnings = [], []
    tracks = []
    for moov in (b for b in top if b.type == "moov"):
        for trak in (c for c in moov.children if c.type == "trak"):
            tracks.append(_parse_track(data, trak, len(data)))
    for t in tracks:
        errors.extend(t.errors)

    fragments = scan_fragments(top, data)
    for f in fragments:
        if not f.closed:
            warnings.append(
                f"unclosed fragment at offset {f.start_offset}: {f.note}")
    chunk_ranges = []
    for t in tracks:
        chunk_ranges.extend(zip(t.chunk_offsets,
                                (o + s for o, s in zip(t.chunk_offsets,
                                                       t.chunk_sizes))))
    for box in orphan_mdats(top, fragments):
        if covered_by_ranges(box, chunk_ranges):
            warnings.append(
                f"mdat at offset {box.offset} holds sample table chunks")
        else:
            warnings.append(
                f"mdat at offset {box.offset} is not attributable to any "
                f"fragment or the sample table")
    tfdts = [(f.index, f.tfdt) for f in fragments
             if f.closed and f.tfdt is not None]
    for (i0, t0), (i1, t1) in zip(tfdts, tfdts[1:]):
        if t1 < t0:
            errors.append(
                f"timestamp rollback: fragment {i1} tfdt {t1} < "
                f"fragment {i0} tfdt {t0}")

    sidxes, sidx_errors = [], []
    sidx_boxes = [b for b in top if b.type == "sidx"]
    if not sidx_boxes:
        errors.append("sidx missing: no segment index box found")
    prev_ept, ranges = None, []
    for box in sidx_boxes:
        s = parse_sidx(data, box)
        sidxes.append(s)
        ept = s.earliest_presentation_time
        if prev_ept is not None and ept < prev_ept:
            sidx_errors.append(
                f"timestamp rollback: sidx at offset {box.offset} has "
                f"earliest_presentation_time {ept} < {prev_ept}")
        prev_ept = ept
        span = ept + sum(r.subsegment_duration for r in s.references)
        if span > MAX_DECODE_TIME:
            sidx_errors.append(
                f"timestamp overflow: sidx at offset {box.offset} "
                f"presentation span exceeds 2^63-1")
        for (a, b) in s.reference_ranges():
            if a < 0 or b > len(data):
                sidx_errors.append(
                    f"sidx at offset {box.offset}: reference range "
                    f"[{a}, {b}) lies outside file of {len(data)} bytes")
            ranges.append((a, b, box.offset))
    ranges.sort()
    for (a0, b0, o0), (a1, b1, o1) in zip(ranges, ranges[1:]):
        if a1 < b0:
            sidx_errors.append(
                f"overlapping references: range [{a1}, {b1}) from sidx at "
                f"{o1} overlaps range [{a0}, {b0}) from sidx at {o0}")
    if sidxes and not any(r.reference_type == 1 for s in sidxes
                          for r in s.references):
        closed = [(f.start_offset, f.end_offset)
                  for f in fragments if f.closed]
        actual = [r for s in sidxes for r in s.reference_ranges()]
        if not sidx_errors and actual != closed:
            sidx_errors.append(
                "sidx references do not match the moof/mdat fragment "
                "boundaries")
    errors.extend(sidx_errors)
    return Analysis(path, data, top, tracks, sidxes, sidx_errors,
                    fragments, warnings, errors)
