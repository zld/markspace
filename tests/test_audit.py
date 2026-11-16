"""Probe output and manifest comparison rule tests."""

from __future__ import annotations

import os
import sys

import pytest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "tools"))

import make_samples as samples  # noqa: E402

from media_audit.compare import (  # noqa: E402
    ManifestError,
    compare_batch,
    load_manifest,
)
from media_audit.probe import probe_file  # noqa: E402


def _write(tmp_path, name: str, data: bytes) -> str:
    path = os.path.join(str(tmp_path), name)
    with open(path, "wb") as handle:
        handle.write(data)
    return path


# --- probe ------------------------------------------------------------------


def test_probe_mp4_tracks_and_duration(tmp_path):
    path = _write(tmp_path, "a.mp4", samples.sample_cutscene_a())
    info = probe_file(path)
    assert info.ok
    assert info.container == "mp4"
    assert info.duration_ms == 10000
    counts = info.track_counts()
    assert counts["video"] == 1
    assert counts["audio"] == 1
    assert counts["subtitle"] == 1
    video = next(t for t in info.tracks if t.type == "video")
    assert video.codec_family == "avc"
    assert video.params["width"] == 1920
    audio = next(t for t in info.tracks if t.type == "audio")
    assert audio.params["sample_rate"] == 48000
    assert audio.params["channels"] == 2


def test_probe_mkv_timecode_scale_and_language(tmp_path):
    path = _write(tmp_path, "d.mkv", samples.sample_cutscene_d())
    info = probe_file(path)
    assert info.ok
    assert info.container == "mkv"
    assert info.duration_ms == 10000
    audio = next(t for t in info.tracks if t.type == "audio")
    assert audio.language == "eng"
    assert audio.codec_family == "aac"


def test_probe_missing_language_is_none_not_und(tmp_path):
    path = _write(tmp_path, "e.mkv", samples.sample_cutscene_e())
    info = probe_file(path)
    assert info.ok
    audio = next(t for t in info.tracks if t.type == "audio")
    assert audio.language is None


def test_probe_truncated_reports_offset_and_continues(tmp_path):
    path = _write(tmp_path, "bad.mkv", samples.sample_truncated())
    info = probe_file(path)
    assert not info.ok
    assert info.error_offset is not None
    assert "beyond parent boundary" in info.parse_error


def test_probe_unsupported_extension(tmp_path):
    path = _write(tmp_path, "notes.txt", b"not media")
    info = probe_file(path)
    assert not info.ok
    assert "unsupported extension" in info.parse_error


def test_probe_unknown_duration_is_none_never_zero(tmp_path):
    import struct

    # mvhd with timescale zero: duration is unknowable and must surface as
    # None, rather than being coerced to a zero-length container.
    mvhd_zero = struct.pack(">I", 0) + bytes(8) + struct.pack(">II", 0, 0)
    mvhd_box = samples.fullbox(b"mvhd", 0, 0, mvhd_zero)
    data = samples.ftyp() + samples.box(b"moov", mvhd_box)
    path = _write(tmp_path, "zero.mp4", data)
    info = probe_file(path)
    assert info.ok
    assert info.duration_ms is None


def test_probe_known_duration_is_int(tmp_path):
    path = _write(tmp_path, "a.mp4", samples.sample_cutscene_b())
    info = probe_file(path)
    assert info.duration_ms is not None
    assert isinstance(info.duration_ms, int)


# --- comparison -------------------------------------------------------------


def _manifest():
    return samples.build_manifest()


def _probe_group(tmp_path):
    names = [
        ("cutscene_a.mp4", samples.sample_cutscene_a()),
        ("cutscene_b.mp4", samples.sample_cutscene_b()),
        ("cutscene_d.mkv", samples.sample_cutscene_d()),
    ]
    infos = {}
    for name, data in names:
        probe = probe_file(_write(tmp_path, name, data))
        probe.name = name
        infos[name] = probe
    return infos


def test_compare_flags_duration_and_missing_language(tmp_path):
    infos = _probe_group(tmp_path)
    result = compare_batch(infos, _manifest(), tolerance_ms=80)
    kinds = {m.kind for m in result.mismatches}
    assert "duration_conflict" in kinds
    assert "missing_language" in kinds
    assert result.max_duration_delta_ms == 120


def test_compare_within_tolerance_is_clean_duration(tmp_path):
    infos = _probe_group(tmp_path)
    result = compare_batch(infos, _manifest(), tolerance_ms=500)
    assert all(
        m.kind != "duration_conflict" for m in result.mismatches
    )


def test_compare_duplicate_track_id(tmp_path):
    path = _write(tmp_path, "c.mov", samples.sample_cutscene_c())
    probe = probe_file(path)
    probe.name = "c.mov"
    manifest = {
        "groups": [
            {"name": "g", "files": ["c.mov"], "expect": []}
        ]
    }
    result = compare_batch({"c.mov": probe}, manifest, 0)
    assert any(m.kind == "duplicate_track_id" for m in result.mismatches)


def test_compare_mismatch_order_is_stable(tmp_path):
    infos = _probe_group(tmp_path)
    first = compare_batch(infos, _manifest(), 80)
    second = compare_batch(infos, _manifest(), 80)
    assert [m.to_dict() for m in first.mismatches] == [
        m.to_dict() for m in second.mismatches
    ]


def test_manifest_rejects_bad_structure(tmp_path):
    path = _write(tmp_path, "bad.json", b'{"groups": [{}]}')
    with pytest.raises(ManifestError):
        load_manifest(path)


def test_manifest_rejects_missing_file(tmp_path):
    path = os.path.join(str(tmp_path), "nope.json")
    with pytest.raises(ManifestError):
        load_manifest(path)

def test_probe_enforces_track_limit(tmp_path):
    path = _write(tmp_path, "a.mp4", samples.sample_cutscene_a())
    info = probe_file(path, {"max_tracks": 2})
    assert not info.ok
    assert "track count exceeds" in info.parse_error


def test_probe_ftyp_brands_skip_minor_version(tmp_path):
    path = _write(tmp_path, "a.mp4", samples.sample_cutscene_a())
    info = probe_file(path)
    assert info.brands[0] == "isom"
    # Minor version (0x00000200) must never appear as a brand.
    assert all(brand.isascii() and brand.isprintable()
               for brand in info.brands)


def test_probe_truncated_mp4_reports_offset(tmp_path):
    path = _write(tmp_path, "bad.mp4", samples.sample_truncated_mp4())
    info = probe_file(path)
    assert not info.ok
    assert info.error_offset is not None
    assert info.container == "mp4"


def test_probe_mov_zero_length_tail_box(tmp_path):
    path = _write(tmp_path, "tail.mov", samples.sample_mov_zero_tail())
    info = probe_file(path)
    assert info.ok
    assert info.container == "mov"
    assert info.track_counts()["video"] == 1
    assert info.duration_ms == 5000
