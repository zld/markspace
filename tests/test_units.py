"""Unit tests for individual parsers and exact-rational time handling."""

from __future__ import annotations

import pytest

from media_index import sidx as sidx_mod
from media_index import stbl
from media_index._fixture_builders import (
    TIMESCALE,
    box,
    co64_box,
    fullbox,
    i32,
    stco_box,
    u16,
    u32,
    u64,
)
from media_index.boxes import iter_boxes, read_box_header
from media_index.errors import LimitError, ParseError
from media_index.limits import MAX_TABLE_ENTRIES
from media_index.model import Rational, ticks_to_rational


# ---------------------------------------------------------------- stz2 ----


def test_stz2_supports_4_8_16_bit_fields():
    for field_size, values in (
        (4, (1, 2, 3, 15, 0, 7)),
        (8, (255, 0, 128, 17)),
        (16, (65535, 1, 256)),
    ):
        if field_size == 4:
            nibbles = list(values) + [0] * (len(values) % 2)
            packed = bytes(
                (nibbles[i] << 4) | nibbles[i + 1]
                for i in range(0, len(nibbles), 2)
            )
        elif field_size == 8:
            packed = bytes(values)
        else:
            packed = b"".join(u16(v) for v in values)
        packed_blob = fullbox(
            "stz2", 0, 0,
            b"\x00\x00\x00" + bytes([field_size]) + u32(len(values)) + packed,
        )
        header, pay = next(iter_boxes(memoryview(packed_blob)))
        parsed = stbl.parse_stz2(header, pay)
        assert parsed.field_size == field_size
        assert parsed.entry_sizes == values
        assert parsed.sample_count == len(values)


def test_stz2_rejects_unknown_field_size():
    payload = b"\x00\x00\x00" + bytes([12]) + u32(0)
    blob = fullbox("stz2", 0, 0, payload)
    header, pay = next(iter_boxes(memoryview(blob)))
    with pytest.raises(ParseError):
        stbl.parse_stz2(header, pay)


def test_stz2_truncated_packed_bytes_detected():
    payload = b"\x00\x00\x00\x08" + u32(4) + b"\x01\x02"  # claims 4, carries 2
    blob = fullbox("stz2", 0, 0, payload)
    header, pay = next(iter_boxes(memoryview(blob)))
    with pytest.raises(ParseError):
        stbl.parse_stz2(header, pay)


# ------------------------------------------------------------- co64 -------


def test_co64_parses_64bit_offsets():
    offsets = (0, 1 << 32, (1 << 40) + 7)
    blob = co64_box(offsets)
    header, pay = next(iter_boxes(memoryview(blob)))
    parsed = stbl.parse_chunk_offsets(header, pay)
    assert parsed.box_type == "co64"
    assert parsed.offsets == offsets


def test_stco_rejects_overflowing_declared_count():
    blob = stco_box((10, 20, 30))
    header, pay = next(iter_boxes(memoryview(blob)))
    # Rewrite the entry count above the hard chunk limit.
    mutated = bytearray(blob)
    mutated[header.content_start + 4 : header.content_start + 8] = u32(
        MAX_TABLE_ENTRIES + 1
    )
    h2, p2 = next(iter_boxes(memoryview(mutated)))
    with pytest.raises(LimitError):
        stbl.parse_chunk_offsets(h2, p2)


# ------------------------------------------------------------- time -------


def test_rational_time_uses_cross_multiplication():
    # 3000/90000 == 1/30 but never compared via floats.
    assert ticks_to_rational(3000, 90000) == Rational(1, 30)
    assert ticks_to_rational(1, 3) < ticks_to_rational(334, 1000)
    assert ticks_to_rational(333, 1000) < ticks_to_rational(1, 3)
    with pytest.raises(ValueError):
        ticks_to_rational(1, 0)
    with pytest.raises(ValueError):
        Rational(1, 0)


def test_non_divisible_timescale_compares_exactly():
    # timescale 1001 (NTSC style) never yields a whole-second duration.
    value = ticks_to_rational(1001, 1001)
    assert value == Rational(1, 1)
    partial = ticks_to_rational(500, 1001)
    assert partial < Rational(1, 2)
    assert ticks_to_rational(501, 1001) > Rational(1, 2)


def test_ctts_version1_interprets_signed_offsets():
    body = u32(2) + u32(1) + i32(-200) + u32(1) + i32(200)
    blob = fullbox("ctts", 1, 0, body)
    header, pay = next(iter_boxes(memoryview(blob)))
    parsed = stbl.parse_ctts(header, pay)
    assert parsed.version == 1
    assert [e.sample_offset for e in parsed.entries] == [-200, 200]


def test_stts_overflow_guard_trips_on_huge_durations():
    # count*delta alone exceeds the 62-bit accumulation clamp.
    body = u32(1) + u32(1 << 31) + u32(1 << 31)
    blob = fullbox("stts", 0, 0, body)
    header, pay = next(iter_boxes(memoryview(blob)))
    with pytest.raises(ParseError) as exc:
        stbl.parse_stts(header, pay)
    assert "overflow" in exc.value.reason


# --------------------------------------------------------- box headers ----


def test_largesize_box_roundtrip():
    payload = b"x" * 16
    largesize = 16 + len(payload)
    blob = u32(1) + b"mdat" + u64(largesize) + payload
    header = read_box_header(memoryview(blob), 0, 0)
    assert header.type == "mdat"
    assert header.header_size == 16
    assert header.end == largesize


def test_truncated_largesize_header_detected():
    # A full 64-bit largesize header whose declared size extends past the view.
    blob = u32(1) + b"mdat" + u64(1 << 40)
    with pytest.raises(ParseError) as exc:
        read_box_header(memoryview(blob), 0, 0)
    assert exc.value.offset == 0
    # A fragment shorter than the eight-byte base header is also rejected.
    with pytest.raises(ParseError) as exc2:
        read_box_header(memoryview(b"\x00\x00\x00"), 0, 0)
    assert exc2.value.offset == 0


def test_size_zero_only_legal_as_final_box():
    inner = u32(0) + b"free"  # size 0, but another box follows
    blob = inner + box("free", b"y")
    with pytest.raises(ParseError):
        list(iter_boxes(memoryview(blob)))


# ------------------------------------------------------------ sidx --------


def test_sidx_version0_and_version1_layouts():
    from media_index.fixtures import sidx_box

    refs = [(0, 100, 500, True), (0, 200, 500, False)]
    for version in (0, 1):
        blob = sidx_box(7, TIMESCALE, 123, 4, refs, version=version)
        header, pay = next(iter_boxes(memoryview(blob)))
        parsed = sidx_mod.parse_sidx(header, pay)
        assert parsed.version == version
        assert parsed.earliest_presentation_time == 123
        assert parsed.first_offset == 4
        assert len(parsed.references) == 2
        assert parsed.references[1].byte_start == header.end + 4 + 100
        assert parsed.references[1].byte_end == parsed.references[1].byte_start + 200


def test_nesting_depth_limit_enforced(monkeypatch):
    monkeypatch.setattr(sidx_mod.limits, "MAX_SIDX_NESTING", 1)
    from media_index.fixtures import build_nested

    blob = build_nested()
    header, pay = next(iter_boxes(memoryview(blob)))
    with pytest.raises(ParseError):
        sidx_mod.resolve_sidx(memoryview(blob), 0, header, pay)


def test_sidx_entry_count_limit_blocks_allocation(monkeypatch):
    # A tiny payload claiming a huge entry count must fail before allocation.
    monkeypatch.setattr(sidx_mod.limits, "MAX_SIDX_REFERENCES", 10)
    body = u32(1) + u32(TIMESCALE) + u64(0) + u64(0) + u16(0) + u16(11)
    blob = fullbox("sidx", 1, 0, body)
    header, pay = next(iter_boxes(memoryview(blob)))
    with pytest.raises(LimitError):
        sidx_mod.parse_sidx(header, pay)
