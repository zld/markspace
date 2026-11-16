"""Parse and expand ``stbl`` sample tables.

Covers stts, optional ctts, stsc, stsz/stz2, stco/co64 and stss, then expands
the compact table records into per-sample decode times, sizes, sync flags and
chunk byte ranges with full cross-consistency checking.
"""

from __future__ import annotations

import struct
from dataclasses import dataclass, field
from typing import BinaryIO

from .boxes import Box, read_fullbox_payload, read_payload
from .errors import FormatError

VIDEO_HANDLER = "vide"
AUDIO_HANDLER = "soun"


@dataclass
class Chunk:
    index: int  # 0-based chunk number
    offset: int
    samples: range  # 0-based sample indices covered


@dataclass
class SampleTable:
    track_id: int
    handler: str
    timescale: int
    duration: int
    count: int
    sizes: list[int]
    chunks: list[Chunk]
    dts: list[int]
    cts_offsets: list[int]
    sync_numbers: list[int]  # 1-based; empty means every sample is a sync sample
    co64: bool
    stsc_entries: list[tuple[int, int, int]] = field(default_factory=list)

    @property
    def all_sync(self) -> bool:
        return not self.sync_numbers

    def is_sync(self, sample_index: int) -> bool:
        return self.all_sync or (sample_index + 1) in self.sync_numbers

    def sample_range(self, start: int, end: int) -> tuple[int, int]:
        """Absolute [start_offset, end_offset) byte range of samples [start, end)."""
        if not (0 <= start <= end <= self.count):
            raise FormatError(f"sample range [{start},{end}) out of bounds (count={self.count})")
        offset = self.chunk_offsets_lookup()
        base = self._sample_offsets
        return base[start], base[end]

    def chunk_offsets_lookup(self) -> list[int]:
        return [c.offset for c in self.chunks]

    def __post_init__(self) -> None:
        # Absolute byte offset of every sample start, plus one sentinel at the end.
        offsets: list[int] = []
        running = -1
        for chunk in self.chunks:
            running = chunk.offset
            for sample_index in chunk.samples:
                offsets.append(running)
                running += self.sizes[sample_index]
        if len(offsets) != self.count:
            raise FormatError(f"track {self.track_id}: chunks cover {len(offsets)} samples, expected {self.count}")
        offsets.append(running)
        self._sample_offsets = offsets


def _u32_list(data: bytes, name: str) -> list[int]:
    if len(data) % 4:
        raise FormatError(f"{name} payload length {len(data)} not a multiple of 4")
    return list(struct.unpack(f">{len(data) // 4}I", data))


def _parse_stts(data: bytes, count: int) -> list[int]:
    values = _u32_list(data, "stts")
    if len(values) < 2 or len(values) != 2 * values[0]:
        raise FormatError("stts entry count does not match payload size")
    dts: list[int] = []
    running = 0
    for i in range(values[0]):
        entry_count, delta = values[1 + 2 * i], values[2 + 2 * i]
        if entry_count == 0:
            raise FormatError("stts entry with zero sample count")
        dts.extend([running] * entry_count)
        running += entry_count * delta
    if len(dts) != count:
        raise FormatError(f"stts covers {len(dts)} samples, stsz declares {count}")
    return dts


def _parse_ctts(version: int, data: bytes, count: int) -> list[int]:
    width = 4 if version == 0 else 8
    if len(data) < 4 or len(data) % (4 + width):
        raise FormatError("malformed ctts payload")
    entry_count = struct.unpack(">I", data[:4])[0]
    if len(data) != 4 + entry_count * (4 + width):
        raise FormatError("ctts entry count does not match payload size")
    offsets: list[int] = []
    pos = 4
    for _ in range(entry_count):
        entry_count = struct.unpack(">I", data[pos:pos + 4])[0]
        pos += 4
        if version == 0:
            value = struct.unpack(">i", data[pos:pos + 4])[0]
            pos += 4
        else:
            value = struct.unpack(">q", data[pos:pos + 8])[0]
            pos += 8
        offsets.extend([value] * entry_count)
    if len(offsets) != count:
        raise FormatError(f"ctts covers {len(offsets)} samples, expected {count}")
    return offsets


def _parse_sizes(stbl: Box, stream: BinaryIO) -> tuple[list[int], bool]:
    stsz = stbl.find("stsz")
    if stsz is not None:
        _, _, data = read_fullbox_payload(stream, stsz)
        if len(data) < 12:
            raise FormatError("stsz payload too short")
        uniform, sample_count = struct.unpack(">II", data[:8])
        array = _u32_list(data[8:], "stsz")
        if len(array) != sample_count:
            raise FormatError(f"stsz declares {sample_count} samples but contains {len(array)} sizes")
        sizes = [uniform] * sample_count if uniform else array
        if uniform and any(s == 0 for s in sizes):
            raise FormatError("stsz uniform sample size must be non-zero")
        return sizes, False
    stz2 = stbl.find("stz2")
    if stz2 is None:
        raise FormatError("stbl contains neither stsz nor stz2")
    _, _, data = read_fullbox_payload(stream, stz2)
    if len(data) < 8:
        raise FormatError("stz2 payload too short")
    field_size = data[3]
    sample_count = struct.unpack(">I", data[4:8])[0]
    packed = data[8:]
    if field_size == 4:
        sizes = [b >> 4 for b in packed] + [b & 0x0F for b in packed]
        sizes = sizes[:sample_count]
    elif field_size == 8:
        sizes = list(packed)
    elif field_size == 16:
        if len(packed) != 2 * sample_count:
            raise FormatError("stz2 16-bit payload does not match sample count")
        sizes = [struct.unpack(">H", packed[i:i + 2])[0] for i in range(0, 2 * sample_count, 2)]
    else:
        raise FormatError(f"unsupported stz2 field size {field_size}")
    if len(sizes) != sample_count or any(s == 0 for s in sizes):
        raise FormatError("stz2 sample count mismatch or zero-sized sample")
    return sizes, True


def _parse_stsc(data: bytes, chunk_count: int, count: int) -> tuple[list[int], list[tuple[int, int, int]]]:
    values = _u32_list(data, "stsc")
    if len(values) < 4 or len(values) != 1 + 3 * values[0]:
        raise FormatError("stsc entry count does not match payload size")
    entries = [(values[1 + 3 * i], values[2 + 3 * i], values[3 + 3 * i]) for i in range(values[0])]
    if entries[0][0] != 1 or any(entries[i][0] >= entries[i + 1][0] for i in range(len(entries) - 1)):
        raise FormatError("stsc first_chunk values must start at 1 and be strictly increasing")
    per_chunk: list[int] = []
    entry_index = 0
    for chunk_number in range(1, chunk_count + 1):
        while entry_index + 1 < len(entries) and entries[entry_index + 1][0] <= chunk_number:
            entry_index += 1
        per_chunk.append(entries[entry_index][1])
    if sum(per_chunk) != count:
        raise FormatError(f"stsc expands to {sum(per_chunk)} samples, stsz declares {count}")
    return per_chunk, entries


def _parse_offsets(stbl: Box, stream: BinaryIO) -> tuple[list[int], bool]:
    co64_box = stbl.find("co64")
    if co64_box is not None:
        _, _, data = read_fullbox_payload(stream, co64_box)
        if len(data) < 4 or len(data) % 4:
            raise FormatError("malformed co64 payload")
        entry_count = struct.unpack(">I", data[:4])[0]
        offsets = list(struct.unpack(f">{entry_count}Q", data[4:])) if entry_count else []
        if len(offsets) != entry_count:
            raise FormatError("co64 entry count does not match payload size")
        return offsets, True
    stco = stbl.find("stco")
    if stco is None:
        raise FormatError("stbl contains neither stco nor co64")
    _, _, data = read_fullbox_payload(stream, stco)
    if len(data) < 4:
        raise FormatError("malformed stco payload")
    entry_count = struct.unpack(">I", data[:4])[0]
    offsets = _u32_list(data[4:], "stco")
    if len(offsets) != entry_count:
        raise FormatError("stco entry count does not match payload size")
    return offsets, False


def _read_trak_headers(trak: Box, stream: BinaryIO) -> tuple[int, int, int, str]:
    tkhd = trak.find_path("tkhd")
    mdhd = trak.find_path("mdia", "mdhd")
    hdlr = trak.find_path("mdia", "hdlr")
    if mdhd is None or hdlr is None or tkhd is None:
        raise FormatError("trak missing tkhd/mdhd/hdlr")
    version, _, tkhd_data = read_fullbox_payload(stream, tkhd)
    track_id = struct.unpack(">I", tkhd_data[16:20] if version == 1 else tkhd_data[8:12])[0]
    md_version, _, md_data = read_fullbox_payload(stream, mdhd)
    timescale = struct.unpack(">I", md_data[8:12])[0]
    if timescale <= 0:
        raise FormatError(f"track {track_id}: non-positive media timescale")
    duration = struct.unpack(">Q" if md_version == 1 else ">I", md_data[12:20 if md_version == 1 else 16])[0]
    hdlr_payload = read_payload(stream, hdlr)
    if len(hdlr_payload) < 12:
        raise FormatError("hdlr payload too short")
    handler = hdlr_payload[8:12].decode("ascii", errors="replace")
    return track_id, timescale, duration, handler


def parse_trak(trak: Box, stream: BinaryIO) -> SampleTable:
    track_id, timescale, duration, handler = _read_trak_headers(trak, stream)
    stbl = trak.find_path("mdia", "minf", "stbl")
    if stbl is None:
        raise FormatError(f"track {track_id}: missing stbl")
    stts, stsc = stbl.find("stts"), stbl.find("stsc")
    if stts is None or stsc is None:
        raise FormatError(f"track {track_id}: stbl missing stts/stsc")
    sizes, _stz2 = _parse_sizes(stbl, stream)
    count = len(sizes)
    _, _, stts_data = read_fullbox_payload(stream, stts)
    dts = _parse_stts(stts_data, count)
    ctts_box = stbl.find("ctts")
    cts: list[int] = [0] * count
    if ctts_box is not None:
        ctts_version, _, ctts_data = read_fullbox_payload(stream, ctts_box)
        cts = _parse_ctts(ctts_version, ctts_data, count)
    offsets, co64 = _parse_offsets(stbl, stream)
    _, _, stsc_data = read_fullbox_payload(stream, stsc)
    per_chunk, stsc_entries = _parse_stsc(stsc_data, len(offsets), count)
    chunks: list[Chunk] = []
    sample_cursor = 0
    for chunk_index, (offset, samples_in_chunk) in enumerate(zip(offsets, per_chunk)):
        if offset < 0:
            raise FormatError(f"track {track_id}: negative chunk offset at chunk {chunk_index}")
        chunks.append(Chunk(chunk_index, offset, range(sample_cursor, sample_cursor + samples_in_chunk)))
        sample_cursor += samples_in_chunk
    stss = stbl.find("stss")
    sync_numbers: list[int] = []
    if stss is not None:
        _, _, stss_data = read_fullbox_payload(stream, stss)
        values = _u32_list(stss_data, "stss")
        if len(values) < 1 or values[0] != len(values) - 1:
            raise FormatError("stss entry count does not match payload")
        sync_numbers = values[1:]
        if any(not 1 <= n <= count for n in sync_numbers) or len(set(sync_numbers)) != len(sync_numbers):
            raise FormatError("stss contains out-of-range or duplicate sample numbers")
    table = SampleTable(
        track_id, handler, timescale, duration, count, sizes, chunks, dts, cts, sync_numbers, co64, stsc_entries
    )
    if count > 1 and duration and sum_run(table) != duration:
        raise FormatError(f"track {track_id}: stts duration {sum_run(table)} != mdhd duration {duration}")
    return table


def sum_run(table: SampleTable) -> int:
    """Media duration implied by stts (last DTS + final sample delta)."""
    if table.count < 2:
        return table.dts[-1] if table.dts else 0
    return table.dts[-1] + (table.dts[-1] - table.dts[-2])


def validate_mdat_coverage(tables: list[SampleTable], mdats: list[Box]) -> None:
    """Every sample byte must lie inside one mdat, with no overlapping ranges."""
    ranges = sorted((m.start, m.end, m.path) for m in mdats)
    spans: list[tuple[int, int, str]] = []
    for table in tables:
        for chunk in table.chunks:
            start = chunk.offset
            end = start + sum(table.sizes[i] for i in chunk.samples)
            if not any(m_start <= start and end <= m_end for m_start, m_end, _ in ranges):
                raise FormatError(f"track {table.track_id} chunk {chunk.index} bytes [{start},{end}) outside any mdat")
            spans.append((start, end, f"track {table.track_id} chunk {chunk.index}"))
    spans.sort()
    for (s1, e1, p1), (s2, e2, p2) in zip(spans, spans[1:]):
        if s2 < e1:
            raise FormatError(f"overlapping sample byte ranges: {p1} [{s1},{e1}) vs {p2} [{s2},{e2})")
