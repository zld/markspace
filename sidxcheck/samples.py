"""Read stbl tables and build a sample map without touching media payloads.

All tables are read from box payloads only; mdat contents are never loaded.
"""

from __future__ import annotations

import struct
from dataclasses import dataclass
from .boxes import Box
from .errors import BoxLimitError, MediaStructureError, UnsupportedFeatureError

MAX_SAMPLES = 1_000_000
MAX_TABLE_ENTRIES = 4_000_000
VALID_SAMPLE_SIZES = (4, 8, 16)


@dataclass(frozen=True)
class TimeToSampleEntry:
    sample_count: int
    sample_delta: int


@dataclass(frozen=True)
class CompositionOffsetEntry:
    sample_count: int
    sample_offset: int


@dataclass(frozen=True)
class SampleToChunkEntry:
    first_chunk: int
    samples_per_chunk: int
    sample_description_index: int


@dataclass(frozen=True)
class MovieHeader:
    version: int
    timescale: int
    duration: int


@dataclass(frozen=True)
class TrackHeader:
    version: int
    track_id: int
    duration: int


@dataclass
class SampleTables:
    track_id: int
    timescale: int
    duration: int
    handler_type: str
    stts: list[TimeToSampleEntry]
    ctts: list[CompositionOffsetEntry] | None
    stsc: list[SampleToChunkEntry]
    sample_sizes: list[int]
    chunk_offsets: list[int]
    sync_samples: list[int] | None
    stsz_kind: str
    sample_description_count: int
    language: str = "und"
    mdhd_version: int = 0

    @property
    def sample_count(self) -> int:
        return len(self.sample_sizes)

    @property
    def all_samples_sync(self) -> bool:
        return self.sync_samples is None


class _Reader:
    """Cursor over a metadata payload with bounds-checked primitives."""

    __slots__ = ("data", "position", "what")

    def __init__(self, data: bytes, what: str) -> None:
        self.data = data
        self.position = 0
        self.what = what

    def take(self, length: int) -> bytes:
        end = self.position + length
        if end > len(self.data):
            raise MediaStructureError(
                f"{self.what} is truncated: needs {length} byte(s) at offset "
                f"{self.position}, only {len(self.data) - self.position} left",
                location=self.what,
            )
        chunk = self.data[self.position : end]
        self.position = end
        return chunk

    def u8(self) -> int:
        return self.take(1)[0]

    def u16(self) -> int:
        return struct.unpack(">H", self.take(2))[0]

    def u24(self) -> int:
        return int.from_bytes(self.take(3), "big")

    def u32(self) -> int:
        return struct.unpack(">I", self.take(4))[0]

    def s32(self) -> int:
        return struct.unpack(">i", self.take(4))[0]

    def u64(self) -> int:
        return struct.unpack(">Q", self.take(8))[0]

    def full_box(self) -> tuple[int, bytes]:
        raw = self.take(4)
        return raw[0], raw[1:]

    def remaining(self) -> int:
        return len(self.data) - self.position


def _entries(count: int, what: str) -> int:
    if count < 0 or count > MAX_TABLE_ENTRIES:
        raise BoxLimitError(
            f"{what} declares {count} entries, limit is {MAX_TABLE_ENTRIES}", location=what
        )
    return count


def parse_mvhd(box: Box, payload: bytes) -> MovieHeader:
    r = _Reader(payload, box.path)
    version, _ = r.full_box()
    if version == 0:
        r.take(8)  # creation, modification
        timescale = r.u32()
        duration = r.u32()
    elif version == 1:
        r.take(16)
        timescale = r.u32()
        duration = r.u64()
    else:
        raise MediaStructureError(f"unsupported mvhd version {version}", location=box.path)
    if timescale == 0:
        raise MediaStructureError("mvhd timescale is 0", location=box.path)
    return MovieHeader(version=version, timescale=timescale, duration=duration)


def parse_tkhd(box: Box, payload: bytes) -> TrackHeader:
    r = _Reader(payload, box.path)
    version, _ = r.full_box()
    if version == 0:
        r.take(8)
        track_id = r.u32()
        r.take(4)
        duration = r.u32()
    elif version == 1:
        r.take(16)
        track_id = r.u32()
        r.take(4)
        duration = r.u64()
    else:
        raise MediaStructureError(f"unsupported tkhd version {version}", location=box.path)
    if track_id == 0:
        raise MediaStructureError("tkhd track_ID is 0", location=box.path)
    return TrackHeader(version=version, track_id=track_id, duration=duration)


def parse_mdhd(box: Box, payload: bytes) -> tuple[int, int, int, str]:
    r = _Reader(payload, box.path)
    version, _ = r.full_box()
    if version == 0:
        r.take(8)
        timescale = r.u32()
        duration = r.u32()
    elif version == 1:
        r.take(16)
        timescale = r.u32()
        duration = r.u64()
    else:
        raise MediaStructureError(f"unsupported mdhd version {version}", location=box.path)
    if timescale == 0:
        raise MediaStructureError("mdhd timescale is 0", location=box.path)
    packed_language = r.u16()
    if packed_language & 0x8000:
        raise MediaStructureError("mdhd language uses pad bit 15", location=box.path)
    letters = [(packed_language >> shift) & 0x1F for shift in (10, 5, 0)]
    if any(letter < 1 or letter > 26 for letter in letters):
        raise MediaStructureError(
            f"mdhd language code {packed_language:#06x} is invalid", location=box.path
        )
    language = "".join(chr(ord("a") + letter - 1) for letter in letters)
    return timescale, duration, version, language


def parse_hdlr(box: Box, payload: bytes) -> str:
    r = _Reader(payload, box.path)
    r.full_box()
    r.u32()  # pre_defined
    handler = r.take(4)
    try:
        return handler.decode("ascii")
    except UnicodeDecodeError:
        raise MediaStructureError(
            f"hdlr handler_type {handler!r} is not ASCII", location=box.path
        ) from None


def parse_stts(box: Box, payload: bytes) -> list[TimeToSampleEntry]:
    r = _Reader(payload, box.path)
    r.full_box()
    count = _entries(r.u32(), "stts")
    entries: list[TimeToSampleEntry] = []
    total = 0
    for index in range(count):
        sample_count = r.u32()
        sample_delta = r.u32()
        if sample_count == 0:
            raise MediaStructureError(
                f"stts entry {index} has sample_count 0", location=box.path
            )
        total += sample_count
        if total > MAX_SAMPLES:
            raise BoxLimitError(
                f"stts totals more than {MAX_SAMPLES} samples", location=box.path
            )
        entries.append(TimeToSampleEntry(sample_count, sample_delta))
    if r.remaining():
        raise MediaStructureError(
            f"stts has {r.remaining()} trailing byte(s)", location=box.path
        )
    return entries


def parse_ctts(box: Box, payload: bytes) -> list[CompositionOffsetEntry]:
    r = _Reader(payload, box.path)
    version, _ = r.full_box()
    if version not in (0, 1):
        raise UnsupportedFeatureError(
            f"unsupported ctts version {version}", location=box.path
        )
    count = _entries(r.u32(), "ctts")
    entries: list[CompositionOffsetEntry] = []
    total = 0
    for index in range(count):
        sample_count = r.u32()
        sample_offset = r.u32() if version == 0 else r.s32()
        if sample_count == 0:
            raise MediaStructureError(
                f"ctts entry {index} has sample_count 0", location=box.path
            )
        total += sample_count
        if total > MAX_SAMPLES:
            raise BoxLimitError(
                f"ctts totals more than {MAX_SAMPLES} samples", location=box.path
            )
        entries.append(CompositionOffsetEntry(sample_count, sample_offset))
    if r.remaining():
        raise MediaStructureError(
            f"ctts has {r.remaining()} trailing byte(s)", location=box.path
        )
    return entries


def parse_stsc(box: Box, payload: bytes) -> list[SampleToChunkEntry]:
    r = _Reader(payload, box.path)
    r.full_box()
    count = _entries(r.u32(), "stsc")
    entries: list[SampleToChunkEntry] = []
    for index in range(count):
        first_chunk = r.u32()
        samples_per_chunk = r.u32()
        description_index = r.u32()
        if first_chunk == 0:
            raise MediaStructureError(
                f"stsc entry {index} has first_chunk 0 (chunks are 1-based)",
                location=box.path,
            )
        if index == 0 and first_chunk != 1:
            raise MediaStructureError(
                f"stsc entry 0 has first_chunk {first_chunk}, expected 1",
                location=box.path,
            )
        if index and first_chunk <= entries[-1].first_chunk:
            raise MediaStructureError(
                f"stsc entry {index} first_chunk {first_chunk} is not increasing",
                location=box.path,
            )
        if samples_per_chunk == 0:
            raise MediaStructureError(
                f"stsc entry {index} has samples_per_chunk 0", location=box.path
            )
        if description_index == 0:
            raise MediaStructureError(
                f"stsc entry {index} has sample_description_index 0", location=box.path
            )
        entries.append(
            SampleToChunkEntry(first_chunk, samples_per_chunk, description_index)
        )
    if r.remaining():
        raise MediaStructureError(
            f"stsc has {r.remaining()} trailing byte(s)", location=box.path
        )
    return entries


def parse_stsz(box: Box, payload: bytes) -> tuple[list[int], str]:
    r = _Reader(payload, box.path)
    r.full_box()
    uniform_size = r.u32()
    count = _entries(r.u32(), "stsz")
    if uniform_size:
        if r.remaining():
            raise MediaStructureError(
                f"stsz uses constant size {uniform_size} but has "
                f"{r.remaining()} extra byte(s)",
                location=box.path,
            )
        return [uniform_size] * count, "stsz-constant"
    sizes = [r.u32() for _ in range(count)]
    if r.remaining():
        raise MediaStructureError(
            f"stsz has {r.remaining()} trailing byte(s) after {count} entries",
            location=box.path,
        )
    return sizes, "stsz"


def parse_stz2(box: Box, payload: bytes) -> tuple[list[int], str]:
    r = _Reader(payload, box.path)
    r.full_box()
    reserved = r.u24()
    field_size = r.u8()
    if reserved != 0:
        raise MediaStructureError(
            f"stz2 reserved field is {reserved}, expected 0", location=box.path
        )
    if field_size not in VALID_SAMPLE_SIZES:
        raise MediaStructureError(
            f"stz2 field_size {field_size} is not one of {VALID_SAMPLE_SIZES}",
            location=box.path,
        )
    count = _entries(r.u32(), "stz2")
    sizes: list[int] = []
    if field_size == 4:
        raw = r.take((count + 1) // 2)
        for index in range(count):
            byte = raw[index // 2]
            sizes.append((byte >> 4) if index % 2 == 0 else (byte & 0x0F))
    elif field_size == 8:
        sizes = list(r.take(count))
    else:
        raw = r.take(count * 2)
        sizes = list(struct.unpack(f">{count}H", raw)) if count else []
    if r.remaining():
        raise MediaStructureError(
            f"stz2 has {r.remaining()} trailing byte(s)", location=box.path
        )
    return sizes, f"stz2-{field_size}"


def parse_stco(box: Box, payload: bytes) -> list[int]:
    r = _Reader(payload, box.path)
    r.full_box()
    count = _entries(r.u32(), "stco")
    offsets = [r.u32() for _ in range(count)]
    if r.remaining():
        raise MediaStructureError(
            f"stco has {r.remaining()} trailing byte(s)", location=box.path
        )
    return offsets


def parse_co64(box: Box, payload: bytes) -> list[int]:
    r = _Reader(payload, box.path)
    r.full_box()
    count = _entries(r.u32(), "co64")
    offsets = [r.u64() for _ in range(count)]
    if r.remaining():
        raise MediaStructureError(
            f"co64 has {r.remaining()} trailing byte(s)", location=box.path
        )
    return offsets


def parse_stss(box: Box, payload: bytes) -> list[int]:
    r = _Reader(payload, box.path)
    r.full_box()
    count = _entries(r.u32(), "stss")
    samples = [r.u32() for _ in range(count)]
    if r.remaining():
        raise MediaStructureError(
            f"stss has {r.remaining()} trailing byte(s)", location=box.path
        )
    if samples != sorted(set(samples)):
        raise MediaStructureError(
            "stss sample numbers must be strictly increasing", location=box.path
        )
    if samples and samples[0] == 0:
        raise MediaStructureError(
            "stss sample numbers are 1-based but entry 0 is 0", location=box.path
        )
    return samples


def parse_stsd(box: Box, payload: bytes) -> tuple[int, list[str]]:
    r = _Reader(payload, box.path)
    r.full_box()
    count = r.u32()
    if count == 0:
        raise MediaStructureError("stsd declares no sample descriptions", location=box.path)
    if count > 1024:
        raise BoxLimitError(f"stsd declares {count} entries, limit is 1024", location=box.path)
    types: list[str] = []
    for index in range(count):
        if r.remaining() < 8:
            raise MediaStructureError(
                f"stsd entry {index} is truncated", location=box.path
            )
        size = r.u32()
        raw_type = r.take(4)
        try:
            entry_type = raw_type.decode("ascii")
        except UnicodeDecodeError:
            raise MediaStructureError(
                f"stsd entry {index} has non-ASCII type {raw_type!r}", location=box.path
            ) from None
        if size < 8:
            raise MediaStructureError(
                f"stsd entry {index} ({entry_type}) has invalid size {size}",
                location=box.path,
            )
        if size - 8 > r.remaining():
            raise MediaStructureError(
                f"stsd entry {index} ({entry_type}) size {size} overruns stsd",
                location=box.path,
            )
        r.take(size - 8)
        types.append(entry_type)
    return count, types
