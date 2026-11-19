"""Round-trip and edge-case tests for the meshift pipeline."""

from __future__ import annotations

import importlib.util
import struct
from pathlib import Path

import pytest

from meshift import ebml
from meshift.cli import build_parser, main
from meshift.errors import (EBMLError, IndexCheckError, MatroskaError,
                            SizeLimitError, UnsupportedCodecError, UsageError)
from meshift.index import parse_mp4, verify_index
from meshift.matroska import parse_matroska
from meshift.mp4 import build_mp4
from meshift.timeline import normalize, half_div

REPO = Path(__file__).resolve().parents[1]
FIXTURE_SCRIPT = REPO / "scripts" / "make_fixture.py"


def _load_fixture_module():
    spec = importlib.util.spec_from_file_location("make_fixture",
                                                  FIXTURE_SCRIPT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


@pytest.fixture(scope="module")
def fixture_bytes():
    return _load_fixture_module().build_main_fixture()


@pytest.fixture(scope="module")
def normalized(fixture_bytes):
    mkv = parse_matroska(fixture_bytes)
    return mkv, normalize(mkv)


def test_vint_widths():
    assert ebml._vint_width(0x80, 0) == 1
    assert ebml._vint_width(0x42, 0) == 2
    assert ebml._vint_width(0x21, 0) == 3
    assert ebml._vint_width(0x1A, 0) == 4
    with pytest.raises(EBMLError):
        ebml._vint_width(0x00, 0)


def test_element_id_retains_marker():
    ident, width = ebml.read_element_id(bytes.fromhex("1a45dfa3"), 0)
    assert ident == 0x1A45DFA3 and width == 4
    with pytest.raises(EBMLError):
        ebml.read_element_id(bytes.fromhex("ff"), 0)


def test_size_vint_strips_marker_and_unknown():
    value, width, unknown = ebml.read_vint(bytes.fromhex("80"), 0, kind="s")
    assert (value, width, unknown) == (0, 1, False)
    value, _, unknown = ebml.read_vint(
        bytes.fromhex("01ffffffffffffff"), 0, kind="s")
    assert unknown and value == (1 << 56) - 1
    with pytest.raises(ebml.TruncatedInputError):
        ebml.read_vint(bytes.fromhex("1a45"), 0, kind="s")


def test_half_div_rounding():
    assert half_div(5, 2) == 3
    assert half_div(-5, 2) == -3
    assert half_div(4, 2) == 2


def test_fixture_structure(normalized):
    mkv, tracks = normalized
    assert len(mkv.tracks) == 2
    assert sum(len(t.samples) for t in tracks) == 12
    assert ebml.count_elements(mkv.roots) >= 20


def test_unknown_length_cluster_boundary(fixture_bytes):
    roots = ebml.parse_tree(fixture_bytes)
    segment = next(r for r in roots if r.element_id == ebml.SEGMENT)
    clusters = segment.find_all(ebml.CLUSTER)
    assert len(clusters) == 3
    unknown = clusters[1]
    assert unknown.size is None
    # Unknown cluster must close exactly where the next sibling starts.
    assert unknown.end == clusters[2].start
    assert len(unknown.children) == 5


def test_timestamps_zero_origin_and_range(normalized):
    _, tracks = normalized
    for track in tracks:
        assert track.first_dts_ns == 0
        assert track.monotonic_ok()
        assert 500_000_000 <= track.last_pts_ns <= 2_000_000_000


def test_negative_composition_offset_preserved(normalized):
    _, tracks = normalized
    video = tracks[0]
    offsets = {s.composition_offset_ns for s in video.samples}
    assert -50_000_000 in offsets
    ctts = [s.composition_offset_ns for s in video.samples]
    assert ctts[4] == -50_000_000


def test_mp4_roundtrip_and_index(normalized):
    _, tracks = normalized
    layout = build_mp4(tracks)
    entries = verify_index(layout.payload, tracks,
                           layout.mdat_start, layout.mdat_end)
    assert len(entries) == 12
    roots = parse_mp4(layout.payload)
    assert {b.box_type for b in roots} == {b"ftyp", b"moov", b"mdat"}
    assert len(layout.payload) <= 64 * 1024 * 1024


def test_unsupported_codec_rejected(fixture_bytes):
    mkv = parse_matroska(fixture_bytes)
    mkv.tracks[0].codec_id = "V_SOMETHING_RARE"
    tracks = normalize(mkv)
    with pytest.raises(UnsupportedCodecError):
        build_mp4(tracks)


def test_truncated_input_raises(fixture_bytes):
    with pytest.raises(ebml.TruncatedInputError):
        parse_matroska(fixture_bytes[: len(fixture_bytes) - 7])


def test_empty_track_handled(fixture_bytes):
    mkv = parse_matroska(fixture_bytes)
    mkv.blocks = [b for b in mkv.blocks if b.track_number != 2]
    tracks = normalize(mkv)
    assert tracks[1].samples == []
    assert any("empty track" in w for w in tracks[1].warnings)


def test_size_limit_enforced(monkeypatch, normalized):
    _, tracks = normalized
    monkeypatch.setattr("meshift.mp4.MAX_OUTPUT_BYTES", 64)
    with pytest.raises(SizeLimitError):
        build_mp4(tracks)


def test_cli_refuses_overwrite(tmp_path, fixture_bytes):
    source = tmp_path / "in.mkv"
    target = tmp_path / "out.mp4"
    source.write_bytes(fixture_bytes)
    target.write_bytes(b"existing")
    rc = main(["convert", str(source), str(target)])
    assert rc == 3
    assert target.read_bytes() == b"existing"


def test_cli_overwrite_and_report(tmp_path, fixture_bytes):
    source = tmp_path / "in.mkv"
    target = tmp_path / "out.mp4"
    report = tmp_path / "report.json"
    source.write_bytes(fixture_bytes)
    rc = main(["convert", str(source), str(target), "--report",
               str(report), "--overwrite"])
    assert rc == 0
    import json
    payload = json.loads(report.read_text())
    assert payload["elements"] >= 20
    assert payload["tracks"] == 2
    assert payload["samples"] == 12
    assert payload["index_entries"] == 12


def test_cli_track_map(tmp_path, fixture_bytes):
    source = tmp_path / "in.mkv"
    target = tmp_path / "out.mp4"
    source.write_bytes(fixture_bytes)
    rc = main(["convert", str(source), str(target),
               "--track-map", "1=2", "--track-map", "2=1"])
    assert rc == 0


def test_cli_bad_track_map(tmp_path, fixture_bytes):
    source = tmp_path / "in.mkv"
    source.write_bytes(fixture_bytes)
    rc = main(["convert", str(source), str(tmp_path / "o.mp4"),
               "--track-map", "bogus"])
    assert rc == 3


def test_cli_segmented_export(tmp_path, fixture_bytes):
    source = tmp_path / "in.mkv"
    target = tmp_path / "seg.mp4"
    source.write_bytes(fixture_bytes)
    rc = main(["convert", str(source), str(target),
               "--segment", "100_000_000", "500_000_000"])
    assert rc == 0
    assert target.exists()


def _wrap(segment_body):
    fx = _load_fixture_module()
    return fx.make_ebml_header() + fx.element(
        ebml.SEGMENT, fx.make_info() + fx.make_tracks() + segment_body)


def test_duplicate_timestamps_forced_increasing(fixture_bytes):
    fx = _load_fixture_module()
    body = (fx.make_simple_block(1, 0, True, 0)
            + fx.make_simple_block(1, 0, False, 1))
    data = _wrap(fx.make_cluster(100, body, False))
    tracks = normalize(parse_matroska(data))
    dts = [s.dts_ns for s in tracks[0].samples]
    assert dts == [0, 1]
    assert any("non-increasing" in w for w in tracks[0].warnings)


def test_untimestamped_samples_imputed_with_warning():
    fx = _load_fixture_module()
    headless = fx.element(
        ebml.CLUSTER,
        fx.make_simple_block(1, 0, True, 0)
        + fx.make_simple_block(1, 50, False, 1))
    anchor = fx.make_cluster(200, fx.make_simple_block(1, 0, True, 2), False)
    data = _wrap(headless + anchor)
    tracks = normalize(parse_matroska(data))
    assert len(tracks[0].samples) == 3
    assert tracks[0].monotonic_ok()
    assert any("untimestamped" in w for w in tracks[0].warnings)


def test_index_detects_corrupted_payload(normalized):
    _, tracks = normalized
    layout = build_mp4(tracks)
    tampered = bytearray(layout.payload)
    # Flip a byte inside mdat so the index payload comparison fails.
    tampered[layout.mdat_start + 1] ^= 0xFF
    with pytest.raises(IndexCheckError):
        verify_index(bytes(tampered), tracks,
                     layout.mdat_start, layout.mdat_end)


def test_illegal_zero_octet_vint_rejected():
    with pytest.raises(EBMLError):
        ebml._vint_width(0x00, 0)


def test_out_of_bounds_element_rejected(fixture_bytes):
    # Corrupt the Segment size into an impossibly large value.
    corrupted = bytearray(fixture_bytes)
    corrupted[9] = 0x7F  # size VINT now claims a huge payload
    with pytest.raises((EBMLError, MatroskaError)):
        parse_matroska(bytes(corrupted))
