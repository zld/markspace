"""Deterministic generators for test media; no external files are referenced."""

from __future__ import annotations

import struct
from dataclasses import dataclass, field
from pathlib import Path

from .mp4write import (
    SegmentRefSpec,
    box,
    box_to_end,
    full_box,
    ftpy,
    s32,
    sidx_box,
    u16,
    u24,
    u32,
    u64,
)

DEFAULT_SAMPLES = 48
DEFAULT_TIMESCALE = 90_000
DEFAULT_SAMPLE_DURATION = 3_750
DEFAULT_SEGMENT_TICKS = 45_000
CHUNK_SAMPLES = 4


@dataclass
class MovieSpec:
    sample_count: int = DEFAULT_SAMPLES
    timescale: int = DEFAULT_TIMESCALE
    sample_duration: int = DEFAULT_SAMPLE_DURATION
    chunk_samples: int = CHUNK_SAMPLES
    sync_samples: list[int] = field(default_factory=list)
    ctts_offsets: list[int] = field(default_factory=list)
    use_stz2: bool = True
    stz2_field_size: int = 16
    use_co64: bool = True
    sample_sizes: list[int] = field(default_factory=list)
    include_stss: bool = True
    include_ctts: bool = True
    old_index: str = "wrong-offset"  # wrong-offset | none | correct
    offset_bias: int = 0
    stts_extra_samples: int = 0
    stts_missing_samples: int = 0
    chunk_offset_bias: int = 0
    truncate_at: int | None = None
    append_moof: bool = False
    encrypted_sample_entry: bool = False
    sidx_version: int = 0
    sidx_omit_tail: int = 0     # drop this many trailing references from the old index
    free_box_size: int = 0      # insert a 'free' box after the old SIDX

    def __post_init__(self) -> None:
        if not self.sample_sizes:
            if self.use_stz2 and self.stz2_field_size == 4:
                modulus = 15
            elif self.use_stz2 and self.stz2_field_size == 8:
                modulus = 255
            else:
                modulus = 37
            self.sample_sizes = [
                1 + (64 + index * 7) % modulus for index in range(self.sample_count)
            ]
        if not self.sync_samples:
            step = self.sample_count // 4 or 1
            self.sync_samples = list(range(1, self.sample_count + 1, step))
        if not self.ctts_offsets:
            self.ctts_offsets = [0] * self.sample_count
            if self.sample_count > 2:
                self.ctts_offsets[1] = -self.sample_duration // 2
                if self.sample_count > 6:
                    self.ctts_offsets[5] = -self.sample_duration


def _run_length(values: list[int]) -> list[tuple[int, int]]:
    runs: list[tuple[int, int]] = []
    for value in values:
        if runs and runs[-1][1] == value:
            runs[-1] = (runs[-1][0] + 1, value)
        else:
            runs.append((1, value))
    return runs


def _moov(spec: MovieSpec, chunk_offsets: list[int], width: int = 320, height: int = 240) -> bytes:
    duration = spec.sample_count * spec.sample_duration
    mvhd = full_box(
        "mvhd",
        0,
        0,
        u32(0) + u32(0) + u32(spec.timescale) + u32(duration)
        + u32(0x00010000) + u16(0x0100) + b"\x00" * 10
        + b"\x00\x01\x00\x00" * 2 + b"\x00" * 4
        + u32(0) + u32(0) + b"\x00" * 20
        + u32(0x00010000) + u32(0) + u32(0) + u32(0)
        + u32(0x00010000) + u32(0) + u32(0) + u32(0)
        + u32(0x40000000)
        + u32(0) * 6
        + u32(1) + u32(0) + u32(0) + u32(0) + u32(0) + u32(0) + u32(2),
    )
    tkhd = full_box(
        "tkhd",
        0,
        7,
        u32(0) + u32(0) + u32(1) + u32(0) + u32(duration)
        + u32(0) + u32(0) + u16(0) + u16(0) + u16(0) + u16(0)
        + b"\x00" * 16
        + u32(0x00010000) + u32(0) + u32(0)
        + u32(0) + u32(0x00010000) + u32(0)
        + u32(0) + u32(0) + u32(0x40000000)
        + u32(width << 16) + u32(height << 16),
    )
    mdhd = full_box(
        "mdhd",
        0,
        0,
        u32(0) + u32(0) + u32(spec.timescale) + u32(duration)
        + u16(0x55C4) + u16(0),
    )
    hdlr = full_box("hdlr", 0, 0, u32(0) + b"vide" + b"\x00" * 12 + b"VideoHandler\x00")
    vmhd = full_box("vmhd", 0, 1, u16(0) + u16(0) + u16(0) + u16(0))
    url = full_box("url ", 0, 1, b"")
    dref = full_box("dref", 0, 0, u32(1) + url)
    dinf = box("dinf", dref)
    entry_type = "encv" if spec.encrypted_sample_entry else "avc1"
    visual_header = (
        b"\x00" * 6 + u16(1) + u16(0) + u16(0) + u32(0) * 3
        + u16(width) + u16(height) + u32(0x00480000) + u32(0x00480000)
        + u32(0) + u16(1) + b"\x00" * 32 + u16(0x0018) + u16(0xFFFF)
    )
    avcc = box("avcC", bytes([1, 0x64, 0, 0x0A, 0xFF, 0xE1, 0, 0, 0]))
    stsd = full_box("stsd", 0, 0, u32(1) + box(entry_type, visual_header + avcc))
    stts_values = [spec.sample_duration] * (spec.sample_count + spec.stts_extra_samples)
    stts_values = stts_values[: max(0, len(stts_values) - spec.stts_missing_samples)]
    stts = full_box(
        "stts",
        0,
        0,
        u32(len(_run_length(stts_values)))
        + b"".join(u32(count) + u32(value) for count, value in _run_length(stts_values)),
    )
    parts = [stsd, stts]
    if spec.include_ctts:
        runs = _run_length(spec.ctts_offsets)
        ctts = full_box(
            "ctts",
            1,
            0,
            u32(len(runs)) + b"".join(u32(count) + s32(value) for count, value in runs),
        )
        parts.append(ctts)
    chunk_count = (spec.sample_count + spec.chunk_samples - 1) // spec.chunk_samples
    stsc = full_box(
        "stsc", 0, 0, u32(1) + u32(1) + u32(spec.chunk_samples) + u32(1)
    )
    parts.append(stsc)
    if spec.use_stz2:
        field = spec.stz2_field_size
        if field == 4:
            raw = bytearray()
            for index in range(0, len(spec.sample_sizes), 2):
                high = spec.sample_sizes[index] & 0x0F
                low = spec.sample_sizes[index + 1] & 0x0F if index + 1 < len(spec.sample_sizes) else 0
                raw.append((high << 4) | low)
            size_payload = u24(0) + bytes([field]) + u32(len(spec.sample_sizes)) + bytes(raw)
        elif field == 8:
            size_payload = u24(0) + bytes([field]) + u32(len(spec.sample_sizes)) + bytes(
                [size & 0xFF for size in spec.sample_sizes]
            )
        else:
            size_payload = u24(0) + bytes([field]) + u32(len(spec.sample_sizes)) + b"".join(
                u16(size) for size in spec.sample_sizes
            )
        parts.append(full_box("stz2", 0, 0, size_payload))
    else:
        parts.append(
            full_box(
                "stsz",
                0,
                0,
                u32(0) + u32(len(spec.sample_sizes))
                + b"".join(u32(size) for size in spec.sample_sizes),
            )
        )
    biased = [offset + spec.chunk_offset_bias for offset in chunk_offsets]
    if spec.use_co64:
        table = full_box(
            "co64", 0, 0, u32(len(biased)) + b"".join(u64(offset) for offset in biased)
        )
    else:
        table = full_box(
            "stco", 0, 0, u32(len(biased)) + b"".join(u32(offset) for offset in biased)
        )
    parts.append(table)
    if spec.include_stss:
        parts.append(
            full_box(
                "stss",
                0,
                0,
                u32(len(spec.sync_samples))
                + b"".join(u32(sample) for sample in spec.sync_samples),
            )
        )
    stbl = box("stbl", b"".join(parts))
    minf = box("minf", vmhd + dinf + stbl)
    mdia = box("mdia", mdhd + hdlr + minf)
    trak = box("trak", tkhd + mdia)
    assert chunk_count == len(chunk_offsets)
    return box("moov", mvhd + trak)


def _segment_specs(
    spec: MovieSpec, samples: list[tuple[int, int]], segment_ticks: int
) -> list[SegmentRefSpec]:
    boundaries = [0]
    for index, (offset, size) in enumerate(samples, start=1):
        if index in spec.sync_samples and index - 1 > boundaries[-1]:
            if (index - 1) * spec.sample_duration - boundaries[-1] * spec.sample_duration >= segment_ticks:
                boundaries.append(index - 1)
    boundaries.append(len(samples))
    refs: list[SegmentRefSpec] = []
    for start, end in zip(boundaries, boundaries[1:]):
        byte_start = samples[start][0]
        byte_end = samples[end - 1][0] + samples[end - 1][1]
        refs.append(
            SegmentRefSpec(
                size=byte_end - byte_start,
                duration=(end - start) * spec.sample_duration,
            )
        )
    return refs


def build_movie(
    spec: MovieSpec | None = None, *, segment_ticks: int = DEFAULT_SEGMENT_TICKS
) -> bytes:
    spec = spec or MovieSpec()
    ftyp = ftpy()
    probe_sizes = spec.sample_sizes
    sample_count = len(probe_sizes)
    # SIDX size depends only on its version and reference count, so the layout
    # can be solved before the chunk offsets are known.
    ref_count = 4 if sample_count % 12 == 0 else max(1, sample_count // 12 + 1)
    if spec.old_index == "none":
        sidx_placeholder_size = 0
    else:
        placeholder_refs = ref_count - min(spec.sidx_omit_tail, ref_count)
        sidx_placeholder_size = (
            8 + (20 if spec.sidx_version == 0 else 28) + 4 + 12 * placeholder_refs
        )
    mdat_payload_start = (
        len(ftyp) + sidx_placeholder_size + spec.free_box_size + 8
    )
    positions: list[tuple[int, int]] = []
    cursor = mdat_payload_start
    for size in probe_sizes:
        positions.append((cursor, size))
        cursor += size
    chunk_offsets = [
        positions[index][0]
        for index in range(0, sample_count, spec.chunk_samples)
    ]
    moov = _moov(spec, chunk_offsets)
    mdat_payload = b"".join(
        bytes(((index * 31 + 7) % 251 for index in range(size)))
        for size in probe_sizes
    )
    mdat = box("mdat", mdat_payload)
    refs = _segment_specs(spec, positions, segment_ticks)
    if spec.sidx_omit_tail:
        if spec.sidx_omit_tail >= len(refs):
            raise ValueError("cannot omit every reference from the old index")
        refs = refs[: len(refs) - spec.sidx_omit_tail]
    if spec.old_index == "none":
        sidx = b""
    else:
        sidx_start = len(ftyp)
        sidx_end = sidx_start + sidx_placeholder_size
        first_offset = mdat_payload_start - sidx_end
        if spec.old_index == "wrong-offset":
            first_offset += 24
        sidx = sidx_box(
            version=spec.sidx_version,
            reference_id=1,
            timescale=spec.timescale,
            earliest_presentation_time=0,
            first_offset=first_offset,
            references=refs,
        )
    free_box = b""
    if spec.free_box_size:
        free_box = struct.pack(">I4s", spec.free_box_size, b"free") + b"\x00" * (
            spec.free_box_size - 8
        )
    expected_size = sidx_placeholder_size + len(free_box)
    if sidx and len(sidx) + len(free_box) != expected_size:
        raise AssertionError(
            f"fixture layout mismatch: SIDX {len(sidx)} + free {len(free_box)} vs "
            f"placeholder {expected_size}"
        )
    tail = b""
    if spec.append_moof:
        tail = box("moof", box("mfhd", full_box("mfhd", 0, 0, u32(1))))
    data = ftyp + sidx + free_box + mdat + moov + tail
    if spec.truncate_at is not None:
        data = data[: spec.truncate_at]
    return data


def write_fixture(path: Path, spec: MovieSpec | None = None, **kwargs: object) -> Path:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(build_movie(spec, **kwargs))  # type: ignore[arg-type]
    return path


def write_demo(directory: Path, spec: MovieSpec | None = None) -> tuple[Path, Path]:
    """Write the canonical original.mp4 and rebuilt.mp4 pair for ``demo``."""
    directory.mkdir(parents=True, exist_ok=True)
    original = directory / "original.mp4"
    rebuilt = directory / "rebuilt.mp4"
    data = build_movie(spec)
    original.write_bytes(data)
    rebuilt.write_bytes(data)
    return original, rebuilt


def zero_padding_box(box_type: str, size: int) -> bytes:
    """Box whose payload is ``size - 8`` zero bytes; handy for layout tests."""
    payload = b"\x00" * (size - 8)
    return struct.pack(">I", size) + box_type.encode("ascii") + payload


__all__ = [
    "MovieSpec",
    "build_movie",
    "write_demo",
    "write_fixture",
    "zero_padding_box",
    "DEFAULT_SAMPLES",
    "DEFAULT_TIMESCALE",
    "DEFAULT_SAMPLE_DURATION",
    "DEFAULT_SEGMENT_TICKS",
]
