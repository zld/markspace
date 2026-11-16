"""Unit tests for sample-table wire-format parsers (ctts, stz2, stts edge cases)."""

from __future__ import annotations

import struct

import pytest

from sidx_tool.errors import FormatError
from sidx_tool.sample_tables import _parse_ctts, _parse_sizes, _parse_stts
import sidx_tool.sample_tables as sample_tables


class _StblFake:
    """Minimal stbl stand-in that only answers find('stz2')."""

    def __init__(self) -> None:
        self.type = "stbl"
        self._stz2 = object()

    def find(self, box_type: str):
        return self._stz2 if box_type == "stz2" else None


@pytest.fixture(autouse=True)
def _restore_reader():
    original = sample_tables.read_fullbox_payload
    yield
    sample_tables.read_fullbox_payload = original


def _feed_stz2(payload: bytes):
    sample_tables.read_fullbox_payload = lambda stream, box: (0, 0, payload)
    return _parse_sizes(_StblFake(), None)


def test_ctts_version0_signed_offsets() -> None:
    data = struct.pack(">I", 2) + struct.pack(">Ii", 3, 10) + struct.pack(">Ii", 3, -5)
    assert _parse_ctts(0, data, 6) == [10, 10, 10, -5, -5, -5]


def test_ctts_version1_signed_64bit_offsets() -> None:
    data = struct.pack(">I", 1) + struct.pack(">Iq", 2, -1000)
    assert _parse_ctts(1, data, 2) == [-1000, -1000]


def test_ctts_count_mismatch_rejected() -> None:
    data = struct.pack(">I", 1) + struct.pack(">Ii", 2, 1)
    with pytest.raises(FormatError, match="ctts"):
        _parse_ctts(0, data, 3)


def test_stz2_four_bit_high_nibble_first() -> None:
    payload = bytes([0, 0, 0, 4]) + struct.pack(">I", 3) + bytes([0xAB, 0xCD])
    sizes, is_z2 = _feed_stz2(payload)
    assert is_z2 and sizes == [10, 11, 12]


def test_stz2_eight_and_sixteen_bit() -> None:
    p8 = bytes([0, 0, 0, 8]) + struct.pack(">I", 3) + bytes([10, 20, 30])
    assert _feed_stz2(p8)[0] == [10, 20, 30]
    p16 = bytes([0, 0, 0, 16]) + struct.pack(">I", 2) + struct.pack(">HH", 1000, 2000)
    assert _feed_stz2(p16)[0] == [1000, 2000]


def test_stz2_unsupported_field_size() -> None:
    payload = bytes([0, 0, 0, 2]) + struct.pack(">I", 1) + b"\x00"
    with pytest.raises(FormatError, match="field size"):
        _feed_stz2(payload)


def test_stz2_zero_size_rejected() -> None:
    payload = bytes([0, 0, 0, 8]) + struct.pack(">I", 2) + bytes([5, 0])
    with pytest.raises(FormatError, match="zero-sized"):
        _feed_stz2(payload)


def test_stts_expands_running_dts() -> None:
    # 3 samples with delta 10 then 2 samples with delta 25.
    data = struct.pack(">I", 2) + struct.pack(">II", 3, 10) + struct.pack(">II", 2, 25)
    assert _parse_stts(data, 5) == [0, 10, 20, 30, 55]


def test_stts_zero_entry_count_rejected() -> None:
    data = struct.pack(">I", 1) + struct.pack(">II", 0, 10)
    with pytest.raises(FormatError, match="zero sample count"):
        _parse_stts(data, 0)
