"""Container boundary tests: malformed structures must fail with offsets."""

from __future__ import annotations

import os
import struct

import pytest

from media_audit.ebml import EBMLError, EBMLReader
from media_audit.iso_bmff import IsoBmffError, IsoBmffReader


def _write(tmp_path, name: str, data: bytes) -> str:
    path = os.path.join(str(tmp_path), name)
    with open(path, "wb") as handle:
        handle.write(data)
    return path


# --- ISO BMFF ---------------------------------------------------------------


def test_iso_walks_nested_boxes(tmp_path):
    inner = struct.pack(">I4s", 8, b"free")
    outer = struct.pack(">I4s", 8 + len(inner), b"moov") + inner
    path = _write(tmp_path, "a.mp4", outer)
    with IsoBmffReader(path) as reader:
        boxes = list(reader.walk())
    assert [box.type for box in boxes] == [b"moov", b"free"]


def test_iso_rejects_truncated_header(tmp_path):
    path = _write(tmp_path, "a.mp4", b"\x00\x00\x00\x10ftyp")
    with IsoBmffReader(path) as reader:
        with pytest.raises(IsoBmffError) as exc:
            list(reader.iter_children(0, reader.file_size))
    assert exc.value.offset == 0


def test_iso_rejects_box_overflowing_parent(tmp_path):
    # Declares 40 bytes but only 16 exist.
    data = struct.pack(">I4s", 40, b"moov") + b"\x00" * 8
    path = _write(tmp_path, "a.mp4", data)
    with IsoBmffReader(path) as reader:
        with pytest.raises(IsoBmffError) as exc:
            list(reader.iter_children(0, reader.file_size))
    assert exc.value.offset == 0


def test_iso_accepts_zero_length_tail_box(tmp_path):
    head = struct.pack(">I4s", 8, b"free")
    tail = struct.pack(">I4s", 0, b"skip") + b"payload"
    path = _write(tmp_path, "a.mp4", head + tail)
    with IsoBmffReader(path) as reader:
        boxes = list(reader.iter_children(0, reader.file_size))
    assert [box.type for box in boxes] == [b"free", b"skip"]
    assert boxes[1].end == reader.file_size


def test_iso_accepts_64bit_extended_size(tmp_path):
    payload = b"x" * 12
    data = struct.pack(">I4sQ", 1, b"ftyp", 16 + len(payload)) + payload
    path = _write(tmp_path, "a.mp4", data)
    with IsoBmffReader(path) as reader:
        box = next(reader.iter_children(0, reader.file_size))
    assert box.header_size == 16
    assert box.size == 28


def test_iso_enforces_depth_limit(tmp_path):
    # Nest moov boxes far deeper than the configured maximum.
    data = b"x"
    for _ in range(40):
        data = struct.pack(">I4s", 8 + len(data), b"moov") + data
    path = _write(tmp_path, "a.mp4", data)
    limits = {"max_depth": 8}
    with IsoBmffReader(path, limits) as reader:
        with pytest.raises(IsoBmffError, match="nesting deeper"):
            list(reader.walk())


def test_iso_skips_unknown_box_without_error(tmp_path):
    data = (
        struct.pack(">I4s", 8, b"zzzz")
        + struct.pack(">I4s", 8, b"free")
    )
    path = _write(tmp_path, "a.mp4", data)
    with IsoBmffReader(path) as reader:
        boxes = list(reader.iter_children(0, reader.file_size))
    assert [box.type for box in boxes] == [b"zzzz", b"free"]


# --- EBML -------------------------------------------------------------------


def _ebml_uint(element_id: int, value: int) -> bytes:
    body = value.to_bytes(1, "big") if value else b""
    id_bytes = element_id.to_bytes(
        max(1, (element_id.bit_length() + 7) // 8), "big"
    )
    width = max(1, (id_bytes[0].bit_length() + 7) // 8)
    id_bytes = bytes([id_bytes[0] | (1 << (8 - width))]) + id_bytes[1:]
    size = bytes([0x80 | len(body)])
    return id_bytes + size + body


def test_ebml_rejects_zero_leading_byte(tmp_path):
    path = _write(tmp_path, "a.mkv", b"\x00\x00\x00\x00")
    with EBMLReader(path) as reader:
        with pytest.raises(EBMLError) as exc:
            list(reader.iter_children(0, reader.file_size))
    assert exc.value.offset == 0


def test_ebml_rejects_child_beyond_parent(tmp_path):
    # Element 0x80 with a 4-byte size declaring 100 payload bytes, but only
    # three exist: the element end runs past its parent.
    element = b"\x80\x10\x00\x00\x00\x64" + b"abc"
    path = _write(tmp_path, "a.mkv", element)
    with EBMLReader(path) as reader:
        with pytest.raises(EBMLError, match="beyond parent boundary"):
            list(reader.iter_children(0, reader.file_size))


def test_ebml_unknown_size_bounded_by_parent(tmp_path):
    # Element ID 0x80 with all-ones 1-byte size (0xFF = unknown length).
    data = b"\x80\xff" + b"\xAA" * 4
    path = _write(tmp_path, "a.mkv", data)
    with EBMLReader(path) as reader:
        element = next(reader.iter_children(0, reader.file_size))
    assert element.size is None
    assert element.end == reader.file_size


def test_ebml_enforces_id_width_limit(tmp_path):
    # Five leading zero bits => 5-byte ID, beyond the default 4-byte limit.
    path = _write(tmp_path, "a.mkv", b"\x08\x80")
    with EBMLReader(path) as reader:
        with pytest.raises(EBMLError, match="ID width 5 exceeds limit"):
            list(reader.iter_children(0, reader.file_size))


def test_ebml_uint_decoding(tmp_path):
    element = _ebml_uint(0x80, 42)
    path = _write(tmp_path, "a.mkv", element)
    with EBMLReader(path) as reader:
        parsed = next(reader.iter_children(0, reader.file_size))
        assert reader.read_uint(parsed) == 42
