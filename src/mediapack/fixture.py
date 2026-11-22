"""Generate baseline MP4 fixtures: valid files and controlled corruptions.

Used by the CLI demo and the test-suite; nothing here reads external
media. Every sample embeds a magic tag, its own index, its payload length
and a sha1 of the payload, so the validator can detect offsets that are
numerically in range but point at the wrong bytes.
"""
from __future__ import annotations

import hashlib
import struct
from pathlib import Path

from .model import SidxReference
from .rebuild import _box, build_moov
from .sidx import encode_sidx
from .stbl import expand_sample_table
from .validate import SAMPLE_HEAD, SAMPLE_MAGIC

TIMESCALE = 1000
TAIL_PAD = 64  # slack so a small offset drift stays inside mdat bounds


def make_sample(index: int, payload_len: int) -> bytes:
    payload = bytes(((index * 31 + i * 7) & 0xFF) for i in range(payload_len))
    head = (
        SAMPLE_MAGIC
        + struct.pack(">II", index, payload_len)
        + hashlib.sha1(payload).digest()
    )
    assert len(head) == SAMPLE_HEAD
    return head + payload


def _ftyp() -> bytes:
    body = b"mpk1" + struct.pack(">I", 0) + b"mpk1" + b"isom"
    return _box(b"ftyp", body)


def _chunk_groups(sample_count: int) -> list[list[int]]:
    """1-based sample indices grouped two per chunk."""
    indices = list(range(1, sample_count + 1))
    return [indices[i : i + 2] for i in range(0, sample_count, 2)]


def _stsc_for(groups: list[list[int]]) -> list[tuple[int, int, int]]:
    if len(groups) == 1:
        return [(1, len(groups[0]), 1)]
    entries = [(1, len(groups[0]), 1)]
    if len(groups[-1]) != len(groups[0]):
        entries.append((len(groups), len(groups[-1]), 1))
    return entries


def build_fixture(
    path,
    *,
    sample_count: int = 5,
    sample_duration: int = TIMESCALE,
    sync: tuple[int, ...] = (1, 3, 5),
    stbl_offset_delta: int = 0,
    stale_sidx: bool = True,
    drop_sidx: bool = False,
    use_co64: bool = False,
    truncate: int = 0,
    stsc_override: list[tuple[int, int, int]] | None = None,
    tamper_sample: int | None = None,
) -> dict:
    """Write a fixture MP4 and return layout info for tests."""
    samples = [make_sample(i, 48 + 8 * i) for i in range(1, sample_count + 1)]
    sizes = [len(s) for s in samples]
    groups = _chunk_groups(sample_count)
    stsc = _stsc_for(groups)
    stts = [(sample_count, sample_duration)]
    width = 8 if use_co64 else 4
    mdat_payload = b"".join(samples) + b"\x00" * TAIL_PAD

    sidx_bytes = b""
    if not drop_sidx and stale_sidx:
        # an index from an older layout: one reference, wrong size/duration
        refs = [SidxReference(sum(sizes) + 16, sample_duration * sample_count, True, 0)]
        sidx_bytes = encode_sidx(TIMESCALE, 0, 0, refs)

    stsc_for_moov = stsc_override if stsc_override is not None else stsc
    draft = expand_sample_table(
        TIMESCALE, sizes, stsc, [0] * len(groups), stts, set(sync), width
    )
    draft.stsc_entries = stsc_for_moov
    moov_draft = build_moov(draft)
    payload_start = len(_ftyp()) + len(sidx_bytes) + len(moov_draft) + 8

    offsets, pos = [], payload_start
    for group in groups:
        offsets.append(pos)
        pos += sum(sizes[i - 1] for i in group)
    stored = [o + stbl_offset_delta for o in offsets]

    table = expand_sample_table(
        TIMESCALE, sizes, stsc, stored, stts, set(sync), width
    )
    table.stsc_entries = stsc_for_moov
    blob = bytearray()
    blob += _ftyp()
    blob += sidx_bytes
    blob += build_moov(table)
    blob += _box(b"mdat", mdat_payload)

    if tamper_sample is not None:
        real = expand_sample_table(
            TIMESCALE, sizes, stsc, offsets, stts, set(sync), width
        )
        off = real.samples[tamper_sample - 1].offset + SAMPLE_HEAD + 8
        blob[off] ^= 0xFF
    if truncate:
        del blob[len(blob) - truncate :]
    Path(path).write_bytes(bytes(blob))
    return {"offsets": offsets, "sizes": sizes, "path": str(path)}
