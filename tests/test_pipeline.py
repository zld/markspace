"""Exchange format v1/v2, OTIO bridge, offline rendering and CLI flows."""

from __future__ import annotations

import json
import subprocess
import sys
from fractions import Fraction
from pathlib import Path

import opentimelineio as otio
import pytest

from cutline import exchange
from cutline.errors import BridgeError, ExchangeError, RenderError
from cutline.model import Clip, RateSegment, Sequence, Track
from cutline.otio_bridge import (
    deserialize_clip_json,
    serialize_clip_json,
    to_otio,
    to_otio_clip,
    verify_round_trip,
)
from cutline.render import frame_signature, render
from cutline.resolve import resolve
from cutline.timebase import FrameRate

from conftest import PROJECT_ROOT, cli_json, run_cli


# --------------------------------------------------------------------------
# Exchange formats


def test_v2_round_trip_preserves_every_field(fixture_path):
    original = exchange.load(fixture_path)
    text = exchange.dumps(original, 2)
    again = exchange.loads(text)
    assert exchange.dumps(again, 2) == text
    hero = again.track_by_id("v1").clip_by_id("b")
    assert (hero.timeline_start, hero.duration, hero.source_in, hero.priority) == (36, 36, 12, 4)


def test_v1_writes_integer_frames_and_constant_speed(tmp_path):
    rate = FrameRate(24)
    clip = Clip(id="c", media_id="m", timeline_start=0, duration=10,
                source_in=0, source_duration=20,
                rate_segments=(RateSegment(0, 10, Fraction(2)),), fade_in=2)
    sequence = Sequence(id="s", frame_rate=rate, tracks=(Track(id="t", clips=(clip,)),))
    document = exchange.to_document(sequence, 1)
    clip_doc = document["sequence"]["tracks"][0]["clips"][0]
    assert clip_doc["speed"] == 2 and "rate_segments" not in clip_doc
    assert document["sequence"]["frame_rate"] == 24
    assert exchange.from_document(document).track_by_id("t").clips[0] == clip


def test_v1_rejects_segmented_and_fractional_material():
    segmented = Clip(id="c", media_id="m", timeline_start=0, duration=10,
                     source_in=0, source_duration=30,
                     rate_segments=(RateSegment(0, 5, Fraction(2)), RateSegment(5, 5, Fraction(1))))
    sequence = Sequence(id="s", frame_rate=FrameRate(24),
                        tracks=(Track(id="t", clips=(segmented,)),))
    with pytest.raises(ExchangeError, match="segmented"):
        exchange.to_document(sequence, 1)
    ntsc = Sequence(id="s", frame_rate=FrameRate(30000, 1001))
    with pytest.raises(ExchangeError, match="frame rate"):
        exchange.to_document(ntsc, 1)


def test_v1_upgrade_applies_documented_defaults():
    document = {
        "version": 1,
        "sequence": {
            "id": "s",
            "frame_rate": 25,
            "tracks": [{"id": "t", "clips": [
                {"id": "c", "media_id": "m", "timeline_start": 0, "duration": 10,
                 "source_in": 0, "source_duration": 10},
            ]}],
        },
    }
    upgraded = exchange.upgrade_v1_to_v2(document)
    assert upgraded["version"] == 2
    assert upgraded["sequence"]["name"] == "sequence"
    assert upgraded["sequence"]["frame_rate"] == {"numerator": 25, "denominator": 1}
    clip_doc = upgraded["sequence"]["tracks"][0]["clips"][0]
    assert clip_doc["rate_segments"] == [
        {"offset": 0, "duration": 10, "speed": {"numerator": 1, "denominator": 1}}
    ]
    assert (clip_doc["fade_in"], clip_doc["fade_out"], clip_doc["priority"]) == (0, 0, 0)
    assert upgraded["sequence"]["tracks"][0]["kind"] == "video"
    assert upgraded["sequence"]["tracks"][0]["level"] == 0


@pytest.mark.parametrize("mutate, message", [
    (lambda d: d.update(version=3), "unsupported exchange version"),
    (lambda d: d["sequence"].pop("frame_rate"), "missing required field"),
    (lambda d: d["sequence"]["tracks"][0]["clips"][0].update(speed=1), "unsupported field"),
    (lambda d: d["sequence"]["tracks"][1]["clips"][1].update(id="a"), "duplicate clip id"),
    (lambda d: d["sequence"]["tracks"][0]["clips"][0].update(duration=-1), "must be >= 1"),
    (lambda d: d["sequence"]["tracks"][0]["clips"][0].update(rate_segments=[]),
     "non-empty array"),
    (lambda d: d["sequence"]["frame_rate"].update(denominator=0), "denominator must not be zero"),
])
def test_v2_documents_reject_bad_input(fixture_path, mutate, message):
    document = json.loads(Path(fixture_path).read_text())
    mutate(document)
    with pytest.raises(ExchangeError, match=message):
        exchange.from_document(document)


def test_load_reports_json_syntax_errors(tmp_path):
    bad = tmp_path / "bad.json"
    bad.write_text("{not json")
    with pytest.raises(ExchangeError, match="invalid JSON"):
        exchange.load(bad)


def test_write_is_atomic_and_does_not_clobber_input(sample_sequence, tmp_path):
    target = tmp_path / "out.json"
    exchange.write(sample_sequence, target, 2)
    before = target.read_bytes()
    assert not list(tmp_path.glob(".*.tmp"))
    exchange.write(sample_sequence, target, 2)
    assert target.read_bytes() == before
    with pytest.raises(ExchangeError):
        exchange.load(tmp_path / "missing.json")


# --------------------------------------------------------------------------
# OTIO bridge


def test_otio_export_round_trip_preserves_order_ranges_and_kind(sample_sequence):
    resolved = resolve(sample_sequence, "priority").sequence
    timeline = to_otio(resolved)
    infos = verify_round_trip(resolved, timeline)
    video = next(info for info in infos if info.track_id == "v1")
    # Priority keeps "b" (priority 4) whole and trims "a" and "c".
    assert video.clip_order == ("a", "b", "c")
    assert video.clip_ranges == ((0, 36), (36, 36), (72, 12))
    assert video.source_ranges == ((0, 36), (12, 36), (12, 12))
    assert video.kind == "video"
    assert next(info for info in infos if info.track_id == "a1").kind == "audio"


def test_otio_export_gaps_and_metadata(sample_sequence):
    resolved = resolve(sample_sequence, "priority").sequence
    timeline = to_otio(resolved)
    assert timeline.metadata["cutline"]["frame_rate"] == {"numerator": 30000, "denominator": 1001}
    assert timeline.metadata["cutline"]["sequence_id"] == "fixture"
    video = timeline.tracks[1]
    names = [item.name for item in video if not isinstance(item, otio.schema.Gap)]
    assert names == ["a", "b", "c"]


def test_otio_rejects_segmented_speed():
    ramp = Clip(id="ramp", media_id="m", timeline_start=0, duration=10,
                source_in=0, source_duration=30,
                rate_segments=(RateSegment(0, 5, Fraction(2)), RateSegment(5, 5, Fraction(1))))
    with pytest.raises(BridgeError, match="segmented rate mapping"):
        to_otio_clip(ramp, FrameRate(24))


def test_otio_clip_metadata_round_trip_is_lossless():
    ramp = Clip(id="ramp", media_id="m", timeline_start=0, duration=12,
                source_in=2, source_duration=30,
                rate_segments=(RateSegment(0, 6, Fraction(1, 2)), RateSegment(6, 6, Fraction(2))),
                fade_in=2, fade_out=3, priority=7)
    text = serialize_clip_json(ramp, FrameRate(24))
    assert deserialize_clip_json(text, FrameRate(24)) == ramp


def test_otio_rejects_overlapping_timeline_before_export():
    a = Clip(id="a", media_id="m", timeline_start=0, duration=10, source_in=0, source_duration=10)
    b = Clip(id="b", media_id="m", timeline_start=5, duration=10, source_in=0, source_duration=10)
    sequence = Sequence(id="s", frame_rate=FrameRate(24), tracks=(Track(id="t", clips=(a, b)),))
    with pytest.raises(BridgeError, match="resolve overlaps"):
        to_otio(sequence)


# --------------------------------------------------------------------------
# Render


def test_render_samples_cover_fades_and_source_mapping(sample_sequence):
    resolved = resolve(sample_sequence, "priority").sequence
    summary = render(resolved, step=1)
    assert summary.sample_count == resolved.duration
    first = summary.samples[0]
    assert (first.clip_id, first.source_frame, first.fade_weight) == ("a", 0, Fraction(0))
    assert summary.samples[7].fade_weight == Fraction(7, 7)
    assert summary.samples[8].fade_weight == Fraction(1)
    # The overlay track v1 wins over audio; "b" keeps its source offset.
    b_frames = [s for s in summary.samples if s.clip_id == "b"]
    assert b_frames[0].source_frame == 12 and b_frames[-1].source_frame == 47


def test_render_is_deterministic_and_respects_step(sample_sequence):
    resolved = resolve(sample_sequence, "priority").sequence
    one = frame_signature(render(resolved, step=3))
    two = frame_signature(render(resolved, step=3))
    assert one == two
    assert render(resolved, step=3).sample_count == len(range(0, resolved.duration, 3))


def test_render_rejects_unresolved_overlaps():
    a = Clip(id="a", media_id="m", timeline_start=0, duration=6, source_in=0, source_duration=6)
    b = Clip(id="b", media_id="m", timeline_start=2, duration=6, source_in=0, source_duration=6)
    sequence = Sequence(id="s", frame_rate=FrameRate(24), tracks=(Track(id="t", clips=(a, b)),))
    with pytest.raises(RenderError, match="overlapping clips"):
        render(sequence)


def test_render_after_resolution_matches_model(sample_sequence):
    result = resolve(sample_sequence, "priority")
    summary = render(result.sequence)
    assert summary.sample_count == result.duration_after
    assert all(sample.clip_id for sample in summary.samples)


# --------------------------------------------------------------------------
# CLI and fixture generator


def test_fixture_script_is_byte_reproducible(tmp_path):
    script = PROJECT_ROOT / "scripts" / "make_fixture.py"
    first, second = tmp_path / "one.json", tmp_path / "two.json"
    for target in (first, second):
        result = subprocess.run([sys.executable, str(script), "--output", str(target)],
                                capture_output=True, text=True, check=False)
        assert result.returncode == 0, result.stderr
    assert first.read_bytes() == second.read_bytes()


def test_acceptance_resolve_command(tmp_path):
    fixture = tmp_path / "cutline-v2.json"
    subprocess.run([sys.executable, str(PROJECT_ROOT / "scripts" / "make_fixture.py"),
                    "--output", str(fixture)], check=True)
    payload = cli_json("resolve", "--input", str(fixture), "--strategy", "priority", "--json")
    assert payload["conflict_count"] >= 2
    assert len(payload["resolved_intervals"]) >= 4
    assert 0 <= payload["duration_delta_frames"] <= 24
    assert payload["render_sample_count"] > 0


def test_cli_validate_demo_and_export(fixture_path, tmp_path):
    validated = cli_json("validate", "--input", str(fixture_path), "--json")
    assert validated["valid"] is True and validated["conflict_count"] >= 1

    demo = cli_json("demo", "--strategy", "preserve-duration", "--no-samples", "--json")
    assert demo["fixture"] == "built-in" and demo["remaining_conflicts"] == 0

    otio_path = tmp_path / "timeline.otio"
    exported = cli_json("export-otio", "--input", str(fixture_path),
                        "--output", str(otio_path), "--resolve", "--json")
    assert exported["track_count"] == 2 and otio_path.exists()


def test_cli_exit_codes_and_stderr(tmp_path, fixture_path):
    missing = run_cli("resolve", "--input", str(tmp_path / "nope.json"))
    assert missing.returncode == 2 and missing.stderr.startswith("cutline:")

    bad = tmp_path / "bad.json"
    bad.write_text("{\"version\": 9, \"sequence\": {}}")
    assert run_cli("validate", "--input", str(bad)).returncode == 2

    ramp = Clip(id="ramp", media_id="m", timeline_start=0, duration=10,
                source_in=0, source_duration=30,
                rate_segments=(RateSegment(0, 5, Fraction(2)), RateSegment(5, 5, Fraction(1))))
    source = tmp_path / "ramp.json"
    exchange.write(Sequence(id="s", frame_rate=FrameRate(24),
                            tracks=(Track(id="t", clips=(ramp,)),)), source, 2)
    exported = run_cli("export-otio", "--input", str(source),
                       "--output", str(tmp_path / "out.otio"))
    assert exported.returncode == 3 and "segmented" in exported.stderr

    before = fixture_path.read_bytes()
    guarded = run_cli("resolve", "--input", str(fixture_path), "--output", str(fixture_path))
    assert guarded.returncode == 2 and fixture_path.read_bytes() == before

    bad_args = run_cli("resolve", "--input", str(fixture_path), "--strategy", "nope")
    assert bad_args.returncode == 2


def test_cli_output_file_is_json(fixture_path, tmp_path):
    target = tmp_path / "summary.json"
    result = run_cli("resolve", "--input", str(fixture_path), "--json",
                     "--output", str(target), "--no-samples")
    assert result.returncode == 0 and result.stdout == ""
    payload = json.loads(target.read_text())
    assert "duration_delta_frames" in payload and "render_sample_count" in payload
