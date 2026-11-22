"""Sample-table (stbl/mdhd) parsing and encoding for the baseline subset.

Parses stsz, stsc, stco/co64, stts and stss plus the mdhd timescale, and
expands the chunk map into a flat sample list cross-checked against stsz.
The encoders are used by the fixture generator and the index rebuilder.
"""
from __future__ import annotations

import struct
from typing import BinaryIO

from .box import Box, BoxError, find_child, read_payload
from .model import Sample, SampleTable


class StblError(BoxError):
    """Structural error inside the sample table."""


def _check_full(payload: bytes, name: str) -> None:
    if len(payload) < 4:
        raise StblError(f"{name}: payload too short for version/flags")
    if payload[0] != 0:
        raise StblError(f"{name}: unsupported version {payload[0]}")


def _entries(payload: bytes, name: str, fmt: str) -> list[tuple]:
    _check_full(payload, name)
    if len(payload) < 8:
        raise StblError(f"{name}: missing entry count")
    (count,) = struct.unpack_from(">I", payload, 4)
    width = struct.calcsize(fmt)
    if len(payload) < 8 + width * count:
        raise StblError(f"{name}: declares {count} entries but payload is truncated")
    out, off = [], 8
    for _ in range(count):
        out.append(struct.unpack_from(fmt, payload, off))
        off += width
    return out


def parse_stsz(payload: bytes) -> list[int]:
    _check_full(payload, "stsz")
    if len(payload) < 12:
        raise StblError("stsz: truncated fixed fields")
    sample_size, count = struct.unpack_from(">II", payload, 4)
    if sample_size:
        return [sample_size] * count
    if len(payload) < 12 + 4 * count:
        raise StblError(f"stsz: declares {count} sizes but payload is truncated")
    return list(struct.unpack_from(f">{count}I", payload, 12))


def parse_stsc(payload: bytes) -> list[tuple[int, int, int]]:
    return [(a, b, c) for a, b, c in _entries(payload, "stsc", ">III")]


def parse_stco(payload: bytes) -> list[int]:
    return [e[0] for e in _entries(payload, "stco", ">I")]


def parse_co64(payload: bytes) -> list[int]:
    return [e[0] for e in _entries(payload, "co64", ">Q")]


def parse_stts(payload: bytes) -> list[tuple[int, int]]:
    return [(a, b) for a, b in _entries(payload, "stts", ">II")]


def parse_stss(payload: bytes) -> list[int]:
    return [e[0] for e in _entries(payload, "stss", ">I")]


def parse_mdhd(payload: bytes) -> tuple[int, int]:
    _check_full(payload, "mdhd")
    if len(payload) < 20:
        raise StblError("mdhd: truncated fixed fields")
    timescale, duration = struct.unpack_from(">II", payload, 12)
    if timescale == 0:
        raise StblError("mdhd: timescale must be non-zero")
    return timescale, duration


def expand_sample_table(
    timescale: int,
    sizes: list[int],
    stsc_entries: list[tuple[int, int, int]],
    chunk_offsets: list[int],
    stts_entries: list[tuple[int, int]],
    sync: set[int],
    offset_width: int,
) -> SampleTable:
    """Expand the stsc chunk map and cross-check it against stsz/stts."""
    if not stsc_entries:
        raise StblError("stsc: no entries")
    entries = sorted(stsc_entries)
    if entries[0][0] != 1:
        raise StblError("stsc: first entry must start at chunk 1")
    for prev, nxt in zip(entries, entries[1:]):
        if nxt[0] <= prev[0]:
            raise StblError("stsc: first_chunk values must be strictly increasing")
    durations: list[int] = []
    for count, delta in stts_entries:
        if delta <= 0:
            raise StblError("stts: sample delta must be positive")
        durations.extend([delta] * count)
    if len(durations) != len(sizes):
        raise StblError(
            f"stts expands to {len(durations)} samples but stsz declares {len(sizes)}"
        )
    samples: list[Sample] = []
    size_idx = 0
    dts = 0
    for pos, (first_chunk, spc, _desc) in enumerate(entries):
        if spc <= 0:
            raise StblError("stsc: samples_per_chunk must be positive")
        last_chunk = entries[pos + 1][0] - 1 if pos + 1 < len(entries) else len(chunk_offsets)
        for chunk_no in range(first_chunk, last_chunk + 1):
            if chunk_no < 1 or chunk_no > len(chunk_offsets):
                raise StblError(
                    f"stsc references chunk {chunk_no} beyond stco count "
                    f"{len(chunk_offsets)}"
                )
            off = chunk_offsets[chunk_no - 1]
            for _ in range(spc):
                if size_idx >= len(sizes):
                    raise StblError(
                        f"stsc maps more samples than stsz declares ({len(sizes)})"
                    )
                sz = sizes[size_idx]
                samples.append(
                    Sample(size_idx + 1, off, sz, dts, durations[size_idx],
                           (size_idx + 1) in sync)
                )
                off += sz
                dts += durations[size_idx]
                size_idx += 1
    if size_idx != len(sizes):
        raise StblError(
            f"stsc maps {size_idx} samples but stsz declares {len(sizes)}"
        )
    return SampleTable(
        timescale=timescale,
        samples=samples,
        chunk_offsets=list(chunk_offsets),
        offset_width=offset_width,
        stts_entries=list(stts_entries),
        stsc_entries=list(stsc_entries),
        sync_indices=sorted(sync),
    )


def load_sample_table(fh: BinaryIO, moov: Box) -> SampleTable:
    """Navigate moov/trak/mdia/minf/stbl and build the expanded table."""
    trak = find_child(fh, moov, "trak")
    if trak is None:
        raise StblError("moov has no trak")
    mdia = find_child(fh, trak, "mdia")
    if mdia is None:
        raise StblError("trak has no mdia")
    mdhd = find_child(fh, mdia, "mdhd")
    minf = find_child(fh, mdia, "minf")
    if mdhd is None or minf is None:
        raise StblError("mdia lacks mdhd or minf")
    stbl = find_child(fh, minf, "stbl")
    if stbl is None:
        raise StblError("minf has no stbl")
    timescale, _duration = parse_mdhd(read_payload(fh, mdhd))
    payloads: dict[str, bytes | None] = {}
    for name in ("stts", "stsc", "stsz", "stss", "stco", "co64"):
        box = find_child(fh, stbl, name)
        payloads[name] = read_payload(fh, box) if box is not None else None
    for required in ("stts", "stsc", "stsz"):
        if payloads[required] is None:
            raise StblError(f"stbl lacks {required}")
    if payloads["stco"] is not None:
        offsets, width = parse_stco(payloads["stco"]), 4
    elif payloads["co64"] is not None:
        offsets, width = parse_co64(payloads["co64"]), 8
    else:
        raise StblError("stbl lacks stco/co64 chunk offsets")
    sync = parse_stss(payloads["stss"]) if payloads["stss"] is not None else []
    return expand_sample_table(
        timescale,
        parse_stsz(payloads["stsz"]),
        parse_stsc(payloads["stsc"]),
        offsets,
        parse_stts(payloads["stts"]),
        set(sync),
        width,
    )


def _full_box(type_: bytes, body: bytes) -> bytes:
    payload = b"\x00\x00\x00\x00" + body  # version 0, flags 0
    return struct.pack(">I4s", 8 + len(payload), type_) + payload


def encode_stts(entries: list[tuple[int, int]]) -> bytes:
    body = struct.pack(">I", len(entries))
    body += b"".join(struct.pack(">II", count, delta) for count, delta in entries)
    return _full_box(b"stts", body)


def encode_stsc(entries: list[tuple[int, int, int]]) -> bytes:
    body = struct.pack(">I", len(entries))
    body += b"".join(struct.pack(">III", *e) for e in entries)
    return _full_box(b"stsc", body)


def encode_stsz(sizes: list[int]) -> bytes:
    body = struct.pack(">II", 0, len(sizes))
    body += b"".join(struct.pack(">I", s) for s in sizes)
    return _full_box(b"stsz", body)


def encode_stco(offsets: list[int]) -> bytes:
    body = struct.pack(">I", len(offsets))
    body += b"".join(struct.pack(">I", o) for o in offsets)
    return _full_box(b"stco", body)


def encode_co64(offsets: list[int]) -> bytes:
    body = struct.pack(">I", len(offsets))
    body += b"".join(struct.pack(">Q", o) for o in offsets)
    return _full_box(b"co64", body)


def encode_stss(indices: list[int]) -> bytes:
    body = struct.pack(">I", len(indices))
    body += b"".join(struct.pack(">I", i) for i in indices)
    return _full_box(b"stss", body)


def encode_mdhd(timescale: int, duration: int) -> bytes:
    body = struct.pack(">IIIIHH", 0, 0, timescale, duration, 0x55C4, 0)
    return _full_box(b"mdhd", body)
