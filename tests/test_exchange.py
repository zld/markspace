"""Exchange format boundary tests: v1/v2 roundtrips and failure modes."""
from __future__ import annotations

import json
from fractions import Fraction

import pytest

from clipweave import exchange, fixtures
from clipweave.errors import (
    DowngradeError,
    FormatError,
    LimitError,
    ValidationError,
)
from clipweave.model import Clip, build_timeline

F24 = Fraction(24)


def f(frames: int) -> Fraction:
    return Fraction(frames, 24)


# -------------------------------------------------------------- roundtrips

def test_v1_roundtrip_preserves_model():
    timeline = fixtures.demo_timeline()
    assert exchange.loads(exchange.dumps(timeline, 1)) == timeline


def test_v2_roundtrip_preserves_model():
    timeline = fixtures.demo_timeline()
    assert exchange.loads(exchange.dumps(timeline, 2)) == timeline


def test_v2_roundtrip_with_offsets_and_metadata():
    timeline = fixtures.offset_timeline()
    assert exchange.roundtrip(timeline, 2) == timeline


def test_v2_times_are_exact_rationals():
    timeline = fixtures.demo_timeline()
    doc = json.loads(exchange.dumps(timeline, 2))
    clips = doc["tracks"][0]["clips"]
    by_id = {c["id"]: c for c in clips}
    assert by_id["C"]["start"] == "10/3"  # 80 frames at 24 fps
    assert by_id["A"]["duration"] == "35/6"
    # Re-parsed values are exact fractions, never floats.
    restored = exchange.loads(exchange.dumps(timeline, 2))
    for clip in restored.clips:
        assert isinstance(clip.start, Fraction)


def test_roundtrip_helper_detects_mismatch():
    timeline = fixtures.offset_timeline()
    with pytest.raises(DowngradeError):
        exchange.roundtrip(timeline, 1)


# --------------------------------------------------------------- downgrade

def test_downgrade_rejects_source_offset():
    timeline = build_timeline(
        fps=F24,
        tracks=[
            ("T", [Clip(id="S", start=f(0), duration=f(10), source_start=f(5))])
        ],
    )
    with pytest.raises(DowngradeError) as excinfo:
        exchange.dumps(timeline, 1)
    assert excinfo.value.clip_id == "S"
    assert excinfo.value.exit_code == 2


def test_downgrade_rejects_metadata():
    timeline = build_timeline(
        fps=F24,
        tracks=[
            (
                "T",
                [Clip(id="M", start=f(0), duration=f(10), metadata={"k": "v"})],
            )
        ],
    )
    with pytest.raises(DowngradeError):
        exchange.dumps(timeline, 1)


def test_v1_uses_integer_frames():
    doc = json.loads(exchange.dumps(fixtures.demo_timeline(), 1))
    clip = doc["tracks"][0]["clips"][0]
    assert clip == {
        "id": "A",
        "start_frame": 0,
        "duration_frames": 140,
        "priority": 1,
    }


# ------------------------------------------------------------- corrupt input

def test_invalid_json_rejected():
    with pytest.raises(FormatError):
        exchange.loads("{not json")


def test_unknown_version_rejected():
    text = json.dumps({"format": "clipweave", "version": 3, "fps": "24",
                       "tracks": []})
    with pytest.raises(FormatError) as excinfo:
        exchange.loads(text)
    assert "unknown exchange version" in str(excinfo.value)


def test_wrong_format_name_rejected():
    text = json.dumps({"format": "other", "version": 1, "fps": "24",
                       "tracks": []})
    with pytest.raises(FormatError):
        exchange.loads(text)


def test_missing_fps_rejected():
    text = json.dumps({"format": "clipweave", "version": 1, "tracks": []})
    with pytest.raises(FormatError):
        exchange.loads(text)


def test_v1_non_integer_frame_rejected():
    doc = {
        "format": "clipweave",
        "version": 1,
        "fps": "24",
        "tracks": [
            {"name": "T",
             "clips": [{"id": "A", "start_frame": 1.5,
                        "duration_frames": 10}]}
        ],
    }
    with pytest.raises(FormatError):
        exchange.loads(json.dumps(doc))


def test_v2_float_time_rejected():
    doc = {
        "format": "clipweave",
        "version": 2,
        "fps": "24",
        "tracks": [
            {"name": "T",
             "clips": [{"id": "A", "start": 0.5, "duration": "1"}]}
        ],
    }
    with pytest.raises(FormatError):
        exchange.loads(json.dumps(doc))


def test_v2_metadata_must_be_object():
    doc = {
        "format": "clipweave",
        "version": 2,
        "fps": "24",
        "tracks": [
            {"name": "T",
             "clips": [{"id": "A", "start": "0", "duration": "1",
                        "metadata": ["nope"]}]}
        ],
    }
    with pytest.raises(FormatError):
        exchange.loads(json.dumps(doc))


# ----------------------------------------------- model errors via the format

def test_zero_fps_rejected_on_load():
    text = json.dumps({"format": "clipweave", "version": 1, "fps": "0",
                       "tracks": []})
    with pytest.raises(ValidationError):
        exchange.loads(text)


def test_negative_duration_rejected_on_load():
    doc = {
        "format": "clipweave",
        "version": 1,
        "fps": "24",
        "tracks": [
            {"name": "T",
             "clips": [{"id": "A", "start_frame": 0, "duration_frames": -5}]}
        ],
    }
    with pytest.raises(ValidationError):
        exchange.loads(json.dumps(doc))


def test_duplicate_ids_rejected_on_load():
    doc = {
        "format": "clipweave",
        "version": 1,
        "fps": "24",
        "tracks": [
            {"name": "T",
             "clips": [
                 {"id": "A", "start_frame": 0, "duration_frames": 5},
                 {"id": "A", "start_frame": 10, "duration_frames": 5},
             ]}
        ],
    }
    with pytest.raises(ValidationError):
        exchange.loads(json.dumps(doc))


def test_non_frame_aligned_v2_time_rejected_on_load():
    doc = {
        "format": "clipweave",
        "version": 2,
        "fps": "24",
        "tracks": [
            {"name": "T",
             "clips": [{"id": "A", "start": "1/48", "duration": "1"}]}
        ],
    }
    with pytest.raises(ValidationError):
        exchange.loads(json.dumps(doc))


def test_clip_limit_enforced_on_load():
    doc = {
        "format": "clipweave",
        "version": 1,
        "fps": "24",
        "tracks": [
            {"name": "T",
             "clips": [
                 {"id": f"C{i}", "start_frame": i * 10, "duration_frames": 5}
                 for i in range(4)
             ]}
        ],
    }
    with pytest.raises(LimitError) as excinfo:
        exchange.loads(json.dumps(doc), max_clips=3)
    assert excinfo.value.exit_code == 3


# ------------------------------------------------------------------ file IO

def test_file_roundtrip(tmp_path):
    timeline = fixtures.offset_timeline()
    path = tmp_path / "timeline.cw2.json"
    exchange.dump_file(timeline, str(path), 2)
    assert exchange.load_file(str(path)) == timeline


def test_load_missing_file_raises_format_error(tmp_path):
    with pytest.raises(FormatError):
        exchange.load_file(str(tmp_path / "missing.json"))
