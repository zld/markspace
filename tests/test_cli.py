import json
from pathlib import Path

import pytest

from mediacheck import cli
from mediacheck.extract import (ExtractError, discover_media, summarize_file,
                                summarize_many)


def test_summary_has_null_reasons_for_missing_fields(sample_dir):
    summary = summarize_file(sample_dir / "level_intro_vo.wav")
    track = summary["tracks"][0]
    # A WAV has no width/height; those must be null with no false reason noise.
    assert track["width"] is None
    assert track["height"] is None
    assert track["duration_seconds"] == pytest.approx(2.0)
    assert track["start_seconds"] == 0.0
    assert summary["codec_summary"] == ["pcm_s16le"]


def test_summary_duration_uses_track_timescale(sample_dir):
    summary = summarize_file(sample_dir / "dialogue_scene2.mp4")
    video = next(t for t in summary["tracks"] if t["kind"] == "video")
    audio = next(t for t in summary["tracks"] if t["kind"] == "audio")
    # 48000 units at 24000 Hz and 88200 units at 44100 Hz are both 2s.
    assert video["duration_seconds"] == pytest.approx(2.0)
    assert audio["duration_seconds"] == pytest.approx(2.0)
    assert summary["duration_seconds"] == pytest.approx(2.0)


def test_edit_list_offsets_presentation_start(edge_dir):
    summary = summarize_file(edge_dir / "offset_start.mp4")
    track = summary["tracks"][0]
    assert track["presentation_offset_seconds"] == pytest.approx(1.0)
    assert track["start_seconds"] == pytest.approx(1.0)
    assert track["duration_seconds"] == pytest.approx(2.0)


def test_discover_is_sorted_and_recursive(tmp_path):
    (tmp_path / "z").mkdir()
    (tmp_path / "a").mkdir()
    for rel in ("z/b.wav", "a/c.mp4", "a/d.txt"):
        path = tmp_path / rel
        path.write_bytes(b"x" * 32)
    found = [p.relative_to(tmp_path).as_posix() for p in discover_media(tmp_path)]
    assert found == ["a/c.mp4", "z/b.wav"]
    flat = [p.name for p in discover_media(tmp_path, recursive=False)]
    assert flat == []


def test_discover_missing_path(tmp_path):
    with pytest.raises(ExtractError) as info:
        discover_media(tmp_path / "nope")
    assert info.value.code == "not_found"


def test_extract_error_has_location(tmp_path):
    path = tmp_path / "bad.mp4"
    path.write_bytes(b"\x00\x00\x00\x20ftyp" + b"isom" + b"\x00" * 12)
    with pytest.raises(ExtractError) as info:
        summarize_file(path)
    assert info.value.code
    assert info.value.path == str(path)
