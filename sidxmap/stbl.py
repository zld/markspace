"""Expand sample tables (stbl) into absolute, byte-addressed samples."""

from __future__ import annotations

import struct

from .boxes import Box, read_fullbox
from .model import (
    DEFAULT_MAX_SAMPLES,
    MdatExtent,
    Sample,
    StructureError,
    Track,
)


def _check_count(count: int, limit: int) -> None:
    if count < 0 or count > limit:
        raise StructureError("sample table entry count outside configured limit")


def _parse_sizes(stbl: Box, count: int) -> list[int]:
    stsz = stbl.find(b"stsz")
    stz2 = stbl.find(b"stz2")
    if stsz is not None:
        p = stsz.payload
        if len(p) < 12:
            raise StructureError("truncated stsz")
        uniform = struct.unpack_from(">I", p, 4)[0]
        declared = struct.unpack_from(">I", p, 8)[0]
        if declared != count:
            raise StructureError("stsz sample_count disagrees with stts")
        if uniform:
            return [uniform] * count
        if len(p) < 12 + 4 * count:
            raise StructureError("truncated stsz entry table")
        sizes = list(struct.unpack_from(f">{count}I", p, 12))
        if any(size == 0 for size in sizes):
            raise StructureError("stsz contains zero-size sample")
        return sizes
    if stz2 is not None:
        p = stz2.payload
        if len(p) < 8:
            raise StructureError("truncated stz2")
        field_size = p[7]
        if field_size not in (4, 8, 16):
            raise StructureError(f"unsupported stz2 field size {field_size}")
        if field_size == 8:
            if len(p) < 8 + count:
                raise StructureError("truncated stz2 entry table")
            return list(p[8 : 8 + count])
        if field_size == 16:
            if len(p) < 8 + 2 * count:
                raise StructureError("truncated stz2 entry table")
            return list(struct.unpack_from(f">{count}H", p, 8))
        packed = (count + 1) // 2
        if len(p) < 8 + packed:
            raise StructureError("truncated stz2 entry table")
        nibbles: list[int] = []
        for byte in p[8 : 8 + packed]:
            nibbles.extend((byte >> 4, byte & 0x0F))
        return nibbles[:count]
    raise StructureError("stbl contains neither stsz nor stz2")


def _parse_chunks(stbl: Box) -> list[int]:
    stco = stbl.find(b"stco")
    co64 = stbl.find(b"co64")
    if co64 is not None:
        p = co64.payload
        if len(p) < 8:
            raise StructureError("truncated co64")
        count = struct.unpack_from(">I", p, 4)[0]
        if len(p) < 8 + 8 * count:
            raise StructureError("truncated co64 entry table")
        return list(struct.unpack_from(f">{count}Q", p, 8))
    if stco is None:
        raise StructureError("stbl contains neither stco nor co64")
    p = stco.payload
    if len(p) < 8:
        raise StructureError("truncated stco")
    count = struct.unpack_from(">I", p, 4)[0]
    if len(p) < 8 + 4 * count:
        raise StructureError("truncated stco entry table")
    return list(struct.unpack_from(f">{count}I", p, 8))


def _parse_stsc(stbl: Box) -> list[tuple[int, int, int]]:
    box = stbl.find(b"stsc")
    if box is None:
        raise StructureError("stbl missing stsc")
    p = box.payload
    if len(p) < 8:
        raise StructureError("truncated stsc")
    count = struct.unpack_from(">I", p, 4)[0]
    if len(p) < 8 + 12 * count:
        raise StructureError("truncated stsc entry table")
    entries = [
        struct.unpack_from(">III", p, 8 + 12 * i) for i in range(count)
    ]
    if any(first_chunk == 0 for first_chunk, _, _ in entries):
        raise StructureError("stsc first_chunk is 1-based, got zero")
    if [e[0] for e in entries] != sorted(set(e[0] for e in entries)):
        raise StructureError("stsc first_chunk entries not strictly ordered")
    return entries


def _samples_per_chunk(entries: list[tuple[int, int, int]], chunk_count: int) -> list[int]:
    result: list[int] = []
    for i in range(chunk_count):
        chunk_no = i + 1
        spc = entries[0][1]
        for first_chunk, samples_per_chunk, _desc in entries:
            if first_chunk <= chunk_no:
                spc = samples_per_chunk
            else:
                break
        result.append(spc)
    return result


def _parse_timing(stbl: Box, count: int) -> tuple[list[int], list[int], list[int]]:
    stts = stbl.find(b"stts")
    if stts is None:
        raise StructureError("stbl missing stts")
    p = stts.payload
    if len(p) < 8:
        raise StructureError("truncated stts")
    entries_n = struct.unpack_from(">I", p, 4)[0]
    if len(p) < 8 + 8 * entries_n:
        raise StructureError("truncated stts entry table")
    durations: list[int] = []
    for i in range(entries_n):
        run, delta = struct.unpack_from(">II", p, 8 + 8 * i)
        durations.extend([delta] * run)
    if len(durations) != count:
        raise StructureError("stts run total disagrees with sample count")
    ctts_deltas: list[int] = []
    ctts = stbl.find(b"ctts")
    if ctts is not None:
        version, _ = read_fullbox(ctts)
        p = ctts.payload
        if len(p) < 8:
            raise StructureError("truncated ctts")
        entries_n = struct.unpack_from(">I", p, 4)[0]
        if len(p) < 8 + 8 * entries_n:
            raise StructureError("truncated ctts entry table")
        signed = version == 1
        for i in range(entries_n):
            run, raw = struct.unpack_from(">II", p, 8 + 8 * i)
            delta = raw - (1 << 32) if signed and raw >= 1 << 31 else raw
            ctts_deltas.extend([delta] * run)
        if len(ctts_deltas) != count:
            raise StructureError("ctts run total disagrees with sample count")
    else:
        ctts_deltas = [0] * count
    sync: list[bool] = [True] * count
    stss = stbl.find(b"stss")
    if stss is not None:
        p = stss.payload
        if len(p) < 8:
            raise StructureError("truncated stss")
        entries_n = struct.unpack_from(">I", p, 4)[0]
        if len(p) < 8 + 4 * entries_n:
            raise StructureError("truncated stss entry table")
        numbers = list(struct.unpack_from(f">{entries_n}I", p, 8))
        if any(n < 1 or n > count for n in numbers):
            raise StructureError("stss references sample outside table")
        if numbers != sorted(set(numbers)):
            raise StructureError("stss entries not strictly ordered")
        sync = [False] * count
        for n in numbers:
            sync[n - 1] = True
    return durations, ctts_deltas, sync


def parse_tracks(root_boxes: tuple[Box, ...], mdats: list[MdatExtent]) -> list[Track]:
    tracks: list[Track] = []
    for trak in _find_traks(root_boxes):
        tracks.append(_parse_trak(trak, mdats))
    return tracks


def _find_traks(root_boxes: tuple[Box, ...]) -> list[Box]:
    moov = next((b for b in root_boxes if b.type == b"moov"), None)
    if moov is None:
        raise StructureError("file has no moov box")
    return moov.find_all(b"trak")


def _parse_trak(trak: Box, mdats: list[MdatExtent]) -> Track:
    tkhd = trak.find_path(b"tkhd")
    mdhd = trak.find_path(b"mdia", b"mdhd")
    hdlr = trak.find_path(b"mdia", b"hdlr")
    stbl = trak.find_path(b"mdia", b"minf", b"stbl")
    if tkhd is None or mdhd is None or hdlr is None or stbl is None:
        raise StructureError("trak missing tkhd/mdhd/hdlr/stbl")
    read_fullbox(tkhd)
    # tkhd layout: version/flags(4) creation(4/8) modification(4/8) track_ID(4)
    tkhd_version = tkhd.payload[0]
    track_id_pos = 4 + (8 if tkhd_version == 1 else 4) * 2
    track_id = struct.unpack_from(">I", tkhd.payload, track_id_pos)[0]
    mv, _ = read_fullbox(mdhd)
    timescale_pos = 20 if mv == 1 else 12
    timescale = struct.unpack_from(">I", mdhd.payload, timescale_pos)[0]
    if timescale == 0:
        raise StructureError("mdhd timescale is zero")
    handler = hdlr.payload[8:12].decode("latin-1") if len(hdlr.payload) >= 12 else "????"
    stts = stbl.find(b"stts")
    count = _sample_count(stts)
    _check_count(count, DEFAULT_MAX_SAMPLES)
    sizes = _parse_sizes(stbl, count)
    chunk_offsets = _parse_chunks(stbl)
    stsc_entries = _parse_stsc(stbl)
    per_chunk = _samples_per_chunk(stsc_entries, len(chunk_offsets))
    if sum(per_chunk) != count:
        raise StructureError("stsc/chunk mapping total disagrees with sample count")
    durations, ctts_deltas, sync_flags = _parse_timing(stbl, count)
    samples = _lay_out_samples(
        count, sizes, chunk_offsets, per_chunk, durations, ctts_deltas, sync_flags, mdats
    )
    return Track(
        track_id=track_id,
        handler=handler,
        timescale=timescale,
        samples=samples,
        has_stss=stbl.find(b"stss") is not None,
    )


def _sample_count(stts: Box | None) -> int:
    if stts is None:
        raise StructureError("stbl missing stts")
    p = stts.payload
    if len(p) < 8:
        raise StructureError("truncated stts")
    entries_n = struct.unpack_from(">I", p, 4)[0]
    if len(p) < 8 + 8 * entries_n:
        raise StructureError("truncated stts entry table")
    return sum(struct.unpack_from(">I", p, 8 + 8 * i)[0] for i in range(entries_n))


def _lay_out_samples(
    count, sizes, chunk_offsets, per_chunk, durations, ctts_deltas, sync_flags, mdats
) -> list[Sample]:
    samples: list[Sample] = []
    sample_idx = 0
    dts = 0
    for chunk_offset, in_chunk in zip(chunk_offsets, per_chunk):
        cursor = chunk_offset
        for _ in range(in_chunk):
            size = sizes[sample_idx]
            _ensure_in_mdat(cursor, size, mdats)
            samples.append(
                Sample(
                    index=sample_idx,
                    offset=cursor,
                    size=size,
                    duration=durations[sample_idx],
                    dts=dts,
                    cts_delta=ctts_deltas[sample_idx],
                    is_sync=sync_flags[sample_idx],
                )
            )
            cursor += size
            dts += durations[sample_idx]
            sample_idx += 1
    if sample_idx != count:
        raise StructureError("chunk layout produced wrong sample count")
    return samples


def _ensure_in_mdat(offset: int, size: int, mdats: list[MdatExtent]) -> None:
    end = offset + size
    for mdat in mdats:
        if offset >= mdat.offset and end <= mdat.end:
            return
    raise StructureError(f"sample range [{offset},{end}) lies outside any mdat")
