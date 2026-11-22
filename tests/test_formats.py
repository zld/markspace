"""Tests for exchange formats, OTIO adapter, renderer and CLI behaviour."""

from __future__ import annotations

import json
from fractions import Fraction

import pytest

from timeline_core import format_v1, format_v2, otio_adapter
from timeline_core.cli import build_demo_document, run_pipeline
from timeline_core.model import Clip, FrameInterval, Timeline, Track
from timeline_core.render import render_manifest
from timeline_core.resolve import resolve_timeline

FPS = 24

V1_DOC = {
    "version": 1,
    "fps": FPS,
    "tracks": [
        {
            "name": "V1",
            "clips": [
                {"id": "A", "start_seconds": 0, "end_seconds": 5, "priority": 1},
                {"id": "C", "start_seconds": 6, "end_seconds": 9, "priority": 1},
            ],
        },
        {
            "name": "V2",
            "clips": [
                {"id": "B", "start_seconds": 3, "end_seconds": 7, "priority": 2},
            ],
        },
    ],
}

V2_DOC = {
    "version": 2,
    "rate": {"numerator": FPS, "denominator": 1},
    "tracks": [
        {
            "name": "V1",
            "clips": [
                {"clip_id": "A", "start_frame": 0, "end_frame": 120, "priority": 1},
                {"clip_id": "C", "start_frame": 144, "end_frame": 216, "priority": 1},
            ],
        },
        {
            "name": "V2",
            "clips": [
                {"clip_id": "B", "start_frame": 72, "end_frame": 168, "priority": 2},
            ],
        },
    ],
}

EXPECTED_INTERVALS = [(0, 72, "A"), (72, 168, "B"), (168, 216, "C")]


def _interval_keys(result):
    return [(i.start, i.end, i.clip_id) for i in result.intervals]


# --------------------------------------------------------------- formats


def test_v1_and_v2_parse_to_equivalent_models():
    t1 = format_v1.loads(V1_DOC)
    t2 = format_v2.loads(V2_DOC)
    assert t1.fps == t2.fps == FPS
    v1_clips = sorted((c.clip_id, c.interval.start, c.interval.end, c.priority) for c in t1.clips)
    v2_clips = sorted((c.clip_id, c.interval.start, c.interval.end, c.priority) for c in t2.clips)
    assert v1_clips == v2_clips


def test_v1_fractional_and_half_frame_inputs():
    doc = {
        "version": 1,
        "fps": FPS,
        "clips": [
            # exact half-frame tie at 1/48 s rounds to frame 0
            {"id": "X", "start_seconds": "1/48", "end_seconds": "1/24", "priority": 0},
        ],
    }
    timeline = format_v1.loads(doc)
    assert timeline.clips[0].interval == FrameInterval(0, 1)


def test_v1_v2_writer_round_trip():
    t1 = format_v1.loads(V1_DOC)
    assert format_v1.loads(format_v1.dumps(t1)).clips == t1.clips
    t2 = format_v2.loads(V2_DOC)
    assert format_v2.loads(format_v2.dumps(t2)).clips == t2.clips


def test_v2_reversed_record_order_gives_identical_result():
    reversed_doc = json.loads(json.dumps(V2_DOC))
    reversed_doc["tracks"][0]["clips"] = list(reversed(reversed_doc["tracks"][0]["clips"]))
    normal = resolve_timeline(format_v2.loads(V2_DOC))
    reversed_result = resolve_timeline(format_v2.loads(reversed_doc))
    assert _interval_keys(normal) == _interval_keys(reversed_result) == EXPECTED_INTERVALS
    assert normal.conflict_count == reversed_result.conflict_count == 2


def test_demo_documents_are_order_independent_across_formats():
    for fmt in ("v1", "v2"):
        forward = build_demo_document(fmt, FPS, reversed_order=False)
        backward = build_demo_document(fmt, FPS, reversed_order=True)
        r1, m1 = run_pipeline(forward, fmt)
        r2, m2 = run_pipeline(backward, fmt)
        assert _interval_keys(r1) == _interval_keys(r2) == EXPECTED_INTERVALS
        assert m1.to_dict()["frames"] == m2.to_dict()["frames"]


# ------------------------------------------------------------- validation


@pytest.mark.parametrize(
    "document,fmt",
    [
        ({"version": 9, "fps": FPS, "clips": []}, "v1"),
        ({"version": 1, "clips": []}, "v1"),  # missing fps
        (
            {"version": 1, "fps": FPS, "clips": [
                {"id": "A", "end_seconds": 1, "priority": 1},
            ]},
            "v1",
        ),  # missing start_seconds
        (
            {"version": 1, "fps": FPS, "clips": [
                {"id": "A", "start_seconds": 5, "end_seconds": 4, "priority": 1},
            ]},
            "v1",
        ),  # negative duration
        ({"version": 2, "rate": {"numerator": FPS}, "tracks": []}, "v1"),
        ({"version": 1, "rate": {"numerator": FPS}, "tracks": []}, "v2"),
        (
            {"version": 2, "rate": {"numerator": FPS}, "tracks": [
                {"name": "V", "clips": [
                    {"start_frame": 0, "end_frame": 1, "priority": 1},
                ]},
            ]},
            "v2",
        ),  # missing clip_id
        (
            {"version": 2, "rate": {"numerator": 24, "denominator": 5},
             "tracks": []},
            "v2",
        ),  # non-integer frame rate
    ],
)
def test_malformed_documents_raise(document, fmt):
    with pytest.raises(Exception):
        run_pipeline(document, fmt)


def test_duplicate_ids_rejected_in_both_formats():
    v1 = {"version": 1, "fps": FPS, "clips": [
        {"id": "A", "start_seconds": 0, "end_seconds": 1, "priority": 1},
        {"id": "A", "start_seconds": 0, "end_seconds": 1, "priority": 1},
    ]}
    v2 = {"version": 2, "rate": {"numerator": FPS}, "tracks": [
        {"name": "V", "clips": [
            {"clip_id": "A", "start_frame": 0, "end_frame": 1, "priority": 1},
            {"clip_id": "A", "start_frame": 0, "end_frame": 1, "priority": 1},
        ]},
    ]}
    with pytest.raises(Exception):
        format_v1.loads(v1)
    with pytest.raises(Exception):
        format_v2.loads(v2)


def test_reader_rejects_non_object_document():
    with pytest.raises(Exception):
        format_v1.loads([1, 2, 3])
    with pytest.raises(Exception):
        format_v2.loads("nope")


# ----------------------------------------------------------- OTIO adapter


def test_otio_round_trip_preserves_model():
    timeline = Timeline(
        FPS,
        (
            Track.from_clips("V1", (
                Clip("A", FrameInterval(0, 120), 1, "V1"),
                Clip("C", FrameInterval(144, 216), 1, "V1"),
            )),
            Track.from_clips("V2", (
                Clip("B", FrameInterval(72, 168), 2, "V2"),
            )),
        ),
    )
    back = otio_adapter.from_otio(otio_adapter.to_otio(timeline))
    assert back.fps == FPS
    key = lambda tl: sorted(
        (c.clip_id, c.interval.start, c.interval.end, c.priority, c.track)
        for c in tl.clips
    )
    assert key(back) == key(timeline)


def test_otio_export_inserts_gap_for_uncovered_span(tmp_path):
    timeline = Timeline.from_clips(
        FPS, [Clip("A", FrameInterval(10, 20), 1)]
    )
    otio_timeline = otio_adapter.to_otio(timeline)
    import opentimelineio as otio

    children = list(otio_timeline.tracks[0])
    assert [type(item).__name__ for item in children] == ["Gap", "Clip"]
    path = tmp_path / "sample.otio"
    otio.adapters.write_to_file(otio_timeline, str(path))
    reloaded = otio_adapter.read_otio_file(str(path))
    assert reloaded.clips[0].interval == FrameInterval(10, 20)


# --------------------------------------------------------------- renderer


def test_render_manifest_is_frame_accurate_and_referenced():
    result = resolve_timeline(format_v2.loads(V2_DOC))
    manifest = render_manifest(result)
    assert manifest.rendered_frames == 216
    assert [frame.frame for frame in manifest.frames] == list(range(216))
    assert manifest.owner_at(71) == "A"
    assert manifest.owner_at(72) == "B"
    assert manifest.owner_at(215) == "C"
    # Each frame references the interval that owns it.
    for frame in manifest.frames:
        interval = manifest.intervals[frame.interval_index]
        assert interval.start <= frame.frame < interval.end
        assert frame.clip_id == interval.clip_id


def test_render_manifest_json_is_deterministic():
    result = resolve_timeline(format_v1.loads(V1_DOC))
    first = json.dumps(render_manifest(result).to_dict(), sort_keys=True)
    second = json.dumps(render_manifest(result).to_dict(), sort_keys=True)
    assert first == second

