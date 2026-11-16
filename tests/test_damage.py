"""Failure-path tests: damaged containers, unsupported features, IO errors."""

from __future__ import annotations

import struct
from pathlib import Path

import pytest

from sidx_tool import MAX_BOX_DEPTH
from sidx_tool.cli import main
from sidx_tool.errors import FileError, FormatError, LimitError, ValidationError
from sidx_tool.fixture import FixtureConfig, write_fixture
from sidx_tool.boxes import parse_file
from sidx_tool.rebuild import rebuild
from sidx_tool.sidx import SidxParseError, parse_sidx
from sidx_tool.validate import verify_bytes


@pytest.fixture()
def source(tmp_path: Path) -> Path:
    return write_fixture(FixtureConfig(), tmp_path / "source.mp4")


def test_truncated_box_header_is_format_error(tmp_path: Path) -> None:
    path = tmp_path / "trunc.mp4"
    path.write_bytes(b"\x00\x00\x00\x20mdat")  # claims 32 bytes, delivers none
    with pytest.raises(FormatError, match="truncated"):
        parse_file(path)
    # A half-written header is also a truncation, never a silent success.
    path2 = tmp_path / "trunc-header.mp4"
    path2.write_bytes(b"\x00\x00\x00")
    with pytest.raises(FormatError, match="truncated"):
        parse_file(path2)


def test_box_overrunning_parent_reports_path(tmp_path: Path) -> None:
    # moov claims a child extending past its own boundary.
    child = struct.pack(">I4s", 40, b"trak") + b"\x00" * 32
    moov = struct.pack(">I4s", 8 + len(child) - 8, b"moov") + child
    path = tmp_path / "overrun.mp4"
    path.write_bytes(moov)
    with pytest.raises(FormatError, match="moov/trak|beyond boundary"):
        parse_file(path)


def test_nested_box_depth_limit(tmp_path: Path) -> None:
    payload = b"x"
    for depth in range(MAX_BOX_DEPTH + 2):
        payload = struct.pack(">I4s", 8 + len(payload), b"moov") + payload
    path = tmp_path / "deep.mp4"
    path.write_bytes(payload)
    with pytest.raises(LimitError, match="depth"):
        parse_file(path)


def test_size_smaller_than_header_rejected(tmp_path: Path) -> None:
    path = tmp_path / "small.mp4"
    path.write_bytes(struct.pack(">I4s", 4, b"mdat"))
    with pytest.raises(FormatError, match="smaller than its header"):
        parse_file(path)


def test_unknown_boxes_are_skipped(source: Path, tmp_path: Path) -> None:
    data = source.read_bytes()
    top = parse_file(source)
    moov = next(b for b in top if b.type == "moov")
    stbl = moov.find_path("trak", "mdia", "minf", "stbl")
    # Append an unknown leaf at the end of stbl and grow every ancestor size,
    # so no existing child boundary is violated; the media region shifts by the
    # inserted length, exercising relocation recovery as well.
    extra_inner = struct.pack(">I4s", 16, b"zzzz") + b"\x00" * 8
    patched = bytearray(data)
    ancestors = [
        stbl,
        moov.find_path("trak", "mdia", "minf"),
        moov.find_path("trak", "mdia"),
        moov.find_path("trak"),
        moov,
    ]
    for box in ancestors:
        struct.pack_into(">I", patched, box.start, box.size + len(extra_inner))
    data = bytes(patched[:stbl.end]) + extra_inner + bytes(patched[stbl.end:])
    path = tmp_path / "unknown.mp4"
    path.write_bytes(data)
    re_parsed = parse_file(path)
    stbl_again = next(b for b in re_parsed if b.type == "moov").find_path(
        "trak", "mdia", "minf", "stbl")
    assert any(b.type == "zzzz" for b in stbl_again.children)
    output = tmp_path / "out.mp4"
    rebuild(path, output, segment_ms=500, max_drift_ms=80)
    assert verify_bytes(output).segment_count == 4


def test_damaged_sidx_is_reported_but_rebuild_succeeds(source: Path, tmp_path: Path) -> None:
    damaged = write_fixture(FixtureConfig(index_state="damaged"), tmp_path / "damaged.mp4")
    top = parse_file(damaged)
    sidx_box = next(b for b in top if b.type == "sidx")
    with damaged.open("rb") as stream:
        with pytest.raises(SidxParseError):
            parse_sidx(stream, sidx_box)
    output = tmp_path / "recovered.mp4"
    rebuild(damaged, output, segment_ms=500, max_drift_ms=80)
    assert verify_bytes(output).sidx_refs == 4


def test_missing_moov_fails_with_format_code(source: Path, tmp_path: Path) -> None:
    data = source.read_bytes()
    moov = next(b for b in parse_file(source) if b.type == "moov")
    stripped = data[:moov.start] + data[moov.end:]
    path = tmp_path / "no-moov.mp4"
    path.write_bytes(stripped)
    assert main(["rebuild", "--input", str(path), "--output", str(tmp_path / "x.mp4")]) == 2


def test_missing_input_is_file_error(tmp_path: Path) -> None:
    with pytest.raises(FileError):
        rebuild(tmp_path / "nope.mp4", tmp_path / "out.mp4", 500, 80)
    assert main(["rebuild", "--input", str(tmp_path / "nope.mp4"),
                 "--output", str(tmp_path / "x.mp4")]) == 4


def test_stale_sidx_fails_verification_exit_3(source: Path) -> None:
    assert main(["verify", "--input", str(source)]) == 3


def test_failed_write_removes_temp_and_preserves_input(source: Path, tmp_path: Path) -> None:
    # Target inside a non-existent nested path whose parent creation is blocked
    # by making its ancestor a regular file.
    blocker = tmp_path / "blocker"
    blocker.write_bytes(b"")
    blocked_output = blocker / "out.mp4"
    before = source.read_bytes()
    rc = main(["rebuild", "--input", str(source), "--output", str(blocked_output)])
    assert rc == 4
    assert source.read_bytes() == before
    assert list(tmp_path.glob("*.tmp")) == []
    assert not blocked_output.exists()


def test_validation_failure_leaves_no_half_output(source: Path, tmp_path: Path, monkeypatch) -> None:
    output = tmp_path / "out.mp4"

    def broken_verify(_path):
        raise ValidationError("synthetic verification failure")

    monkeypatch.setattr("sidx_tool.rebuild.verify_bytes", broken_verify)
    with pytest.raises(ValidationError):
        rebuild(source, output, segment_ms=500, max_drift_ms=80)
    assert not output.exists()
    assert list(tmp_path.glob("*.tmp")) == []


def test_corrupt_chunk_offset_outside_mdat(tmp_path: Path) -> None:
    source = write_fixture(FixtureConfig(), tmp_path / "source.mp4")
    top = parse_file(source)
    moov = next(b for b in top if b.type == "moov")
    stco_box = moov.find_path("trak", "mdia", "minf", "stbl", "stco")
    data = bytearray(source.read_bytes())
    # First chunk offset sits after the 8-byte box header, 4-byte fullbox
    # version/flags and 4-byte entry-count word.
    pos = stco_box.header_end + 4
    struct.pack_into(">I", data, pos, 0x7FFFFFF0)
    broken = tmp_path / "broken-offset.mp4"
    broken.write_bytes(data)
    with pytest.raises(FormatError):
        rebuild(broken, tmp_path / "out.mp4", 500, 80)


def test_overlapping_sample_ranges_rejected(source: Path, tmp_path: Path) -> None:
    top = parse_file(source)
    moov = next(b for b in top if b.type == "moov")
    stco_box = moov.find_path("trak", "mdia", "minf", "stbl", "stco")
    data = bytearray(source.read_bytes())
    first = stco_box.header_end + 4
    second = first + 4
    value = struct.unpack_from(">I", data, second)[0]
    struct.pack_into(">I", data, first, value)  # two chunks share one offset
    broken = tmp_path / "overlap.mp4"
    broken.write_bytes(data)
    with pytest.raises(FormatError):
        rebuild(broken, tmp_path / "out.mp4", 500, 80)
