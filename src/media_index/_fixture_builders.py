"""Deterministic ISO BMFF fixture builders and corruption injectors.

Generated files are fully self-contained: no external media assets are read.
All numeric fields use fixed constants so byte layouts are reproducible. The
factory produces both a valid reference file and a family of corrupted
variants used by the test-suite and by the CLI first-run generator.
"""

from __future__ import annotations

import os

# Deterministic layout constants.
TIMESCALE = 90000
SAMPLE_SIZES = (400, 420, 380, 440, 400, 460)  # six samples
SAMPLE_DELTA = 3000  # 90k ticks => 1/30 s per sample
CHUNK_PLAN = (2, 2, 2)  # three chunks, two samples each
SEGMENT_PLAN = (2, 2, 2)  # three sidx media references, one chunk each
SYNC_SAMPLES = (1, 3, 5)
CTTS_OFFSETS = (0, 100, 0, -100, 0, 0)  # requires version 1 ctts

# Fixed first bytes so generated media content is deterministic.
MEDIA_FILL = 0xA5


def u8(value: int) -> bytes:
    return value.to_bytes(1, "big")


def u16(value: int) -> bytes:
    return value.to_bytes(2, "big")


def u32(value: int) -> bytes:
    return value.to_bytes(4, "big", signed=False)


def u64(value: int) -> bytes:
    return value.to_bytes(8, "big", signed=False)


def i32(value: int) -> bytes:
    return value.to_bytes(4, "big", signed=True)


def fullbox(box_type: str, version: int, flags: int, body: bytes) -> bytes:
    head = u8(version) + u24(flags)
    return box(box_type, head + body)


def u24(value: int) -> bytes:
    return value.to_bytes(3, "big")


def box(box_type: str, payload: bytes) -> bytes:
    if len(box_type) != 4:
        raise ValueError("box type must be four characters")
    return u32(8 + len(payload)) + box_type.encode("latin-1") + payload


def container(box_type: str, children: list[bytes]) -> bytes:
    return box(box_type, b"".join(children))


def ftyp_box() -> bytes:
    # major_brand minor_version compatible_brands
    payload = b"iso5" + u32(1) + b"iso5dash"
    return box("ftyp", payload)


def stsz_box(sizes: tuple[int, ...]) -> bytes:
    body = u32(0) + u32(len(sizes)) + b"".join(u32(s) for s in sizes)
    return fullbox("stsz", 0, 0, body)


def stts_box(delta: int, sample_count: int) -> bytes:
    if sample_count == 0:
        return fullbox("stts", 0, 0, u32(0))
    body = u32(1) + u32(sample_count) + u32(delta)
    return fullbox("stts", 0, 0, body)


def ctts_box(offsets: tuple[int, ...]) -> bytes:
    body = u32(len(offsets)) + b"".join(u32(1) + i32(o) for o in offsets)
    return fullbox("ctts", 1, 0, body)


def stsc_box(plan: tuple[int, ...]) -> bytes:
    # One entry per chunk is simplest and fully deterministic.
    entries = b"".join(
        u32(chunk_no) + u32(samples) + u32(1)
        for chunk_no, samples in enumerate(plan, start=1)
    )
    return fullbox("stsc", 0, 0, u32(len(plan)) + entries)


def stco_box(offsets: tuple[int, ...]) -> bytes:
    body = u32(len(offsets)) + b"".join(u32(o) for o in offsets)
    return fullbox("stco", 0, 0, body)


def co64_box(offsets: tuple[int, ...]) -> bytes:
    body = u32(len(offsets)) + b"".join(u64(o) for o in offsets)
    return fullbox("co64", 0, 0, body)


def stss_box(sync: tuple[int, ...]) -> bytes:
    body = u32(len(sync)) + b"".join(u32(s) for s in sync)
    return fullbox("stss", 0, 0, body)


def mdat_box(chunks: list[bytes]) -> bytes:
    return box("mdat", b"".join(chunks))


def media_chunk(size: int, seed: int) -> bytes:
    """Deterministic payload bytes for one media chunk."""

    return bytes((MEDIA_FILL + seed) & 0xFF for _ in range(size))


def ensure_dir(path: str) -> None:
    os.makedirs(path, exist_ok=True)
