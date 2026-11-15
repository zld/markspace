"""Integration tests: fixtures -> validate -> detect -> resolve -> export."""
from __future__ import annotations

import json
from fractions import Fraction

import pytest

from clipweave import cli, fixtures
from clipweave.detect import find_conflicts
from clipweave.errors import LimitError, ValidationError
from clipweave.model import Clip, build_timeline, validate_timeline
from clipweave.pipeline import run_pipeline
from clipweave.resolve import resolve_timeline

F24 = Fraction(24)


def f(frames: int) -> Fraction:
    return Fraction(frames, 24)


def frame_window(clip) -> tuple[int, int]:
    return (int(clip.start * 24), int(clip.end * 24))


# ------------------------------------------------------------- acceptance

def test_demo_acceptance_numbers():
    report = run_pipeline(fixtures.demo_timeline()).report
    assert report["conflict_count"] == 3
    assert report["resolved_intervals"] == [
        "A[0,80)",
        "C[80,120)",
        "B[120,150)",
    ]
    assert report["total_duration_before_frames"] == 230
    assert report["total_duration_after_frames"] == 150
    assert report["duration_delta_frames"] == -80
    assert report["duration_deviation_frames"] == 80
    # 80 frames at 24 fps is exactly 10/3 seconds, no float rounding.
    assert report["duration_deviation_seconds"] == "10/3"


def test_cli_demo_roundtrip_and_otio(capsys, tmp_path):
    otio_path = tmp_path / "demo.otio"
    code = cli.main(
        [
            "demo",
            "--roundtrip",
            "v1,v2",
            "--otio-out",
            str(otio_path),
            "--report-json",
            "-",
        ]
    )
    assert code == 0
    report = json.loads(capsys.readouterr().out)
    assert report["conflict_count"] == 3
    assert report["roundtrip"] == {"v1": "ok", "v2": "ok"}
    assert otio_path.exists()

    import opentimelineio as otio

    exported = otio.adapters.read_from_file(str(otio_path))
    (track,) = exported.tracks
    assert [c.name for c in track] == ["A", "C", "B"]
    assert [int(c.source_range.duration.value) for c in track] == [80, 40, 30]


# -------------------------------------------------------------- resolver

def test_nested_overlap_splits_and_source_offsets():
    resolved = resolve_timeline(fixtures.nested_timeline())
    got = [(c.id, *frame_window(c)) for c in resolved.tracks[0].clips]
    assert got == [
        ("X#1", 0, 10),
        ("Y#1", 10, 20),
        ("Z#1", 20, 30),
        ("W", 30, 70),
        ("Z#2", 70, 80),
        ("Y#2", 80, 90),
        ("X#2", 90, 100),
    ]
    by_id = {c.id: c for c in resolved.tracks[0].clips}
    # Source offsets shift in lockstep with the timeline trim.
    assert by_id["X#2"].source_start == f(90)
    assert by_id["Z#2"].source_start == f(50)  # Z starts at frame 20
    assert by_id["W"].source_start == f(0)  # W survives untrimmed
    # Fragment ids are reproducible across runs.
    again = resolve_timeline(fixtures.nested_timeline())
    assert [c.id for c in again.tracks[0].clips] == [g[0] for g in got]


def test_same_start_end_tie_broken_by_input_order():
    timeline = build_timeline(
        fps=F24,
        tracks=[
            (
                "T",
                [
                    Clip(id="P", start=f(10), duration=f(10), priority=5),
                    Clip(id="Q", start=f(10), duration=f(10), priority=5),
                ],
            )
        ],
    )
    assert len(find_conflicts(timeline)) == 1
    resolved = resolve_timeline(timeline)
    assert [c.id for c in resolved.tracks[0].clips] == ["P"]


def test_empty_track_resolves_to_empty():
    timeline = fixtures.empty_track_timeline()
    assert find_conflicts(timeline) == []
    resolved = resolve_timeline(timeline)
    assert resolved.tracks[0].clips == ()
    report = run_pipeline(timeline).report
    assert report["total_duration_after_frames"] == 0


def test_long_tail_is_trimmed_not_dropped():
    resolved = resolve_timeline(fixtures.long_tail_timeline())
    got = [(c.id, *frame_window(c)) for c in resolved.tracks[0].clips]
    assert got == [("P", 0, 10), ("Q", 10, 1005)]
    q = resolved.tracks[0].clips[1]
    assert q.source_start == f(5)


def test_zero_duration_clip_is_dropped():
    timeline = build_timeline(
        fps=F24,
        tracks=[("T", [Clip(id="Z", start=f(3), duration=Fraction(0))])],
    )
    validate_timeline(timeline)  # zero duration is not negative: allowed
    assert resolve_timeline(timeline).tracks[0].clips == ()


# -------------------------------------------------------------- detection

def test_touching_intervals_are_not_conflicts():
    timeline = build_timeline(
        fps=F24,
        tracks=[
            (
                "T",
                [
                    Clip(id="A", start=f(0), duration=f(10)),
                    Clip(id="B", start=f(10), duration=f(10)),
                ],
            )
        ],
    )
    assert find_conflicts(timeline) == []


def test_different_tracks_run_in_parallel():
    timeline = build_timeline(
        fps=F24,
        tracks=[
            ("VO", [Clip(id="A", start=f(0), duration=f(50))]),
            ("SFX", [Clip(id="B", start=f(0), duration=f(50))]),
        ],
    )
    assert find_conflicts(timeline) == []


def test_conflict_count_is_exact_for_clique():
    clips = [
        Clip(id=f"K{i}", start=f(0), duration=f(10)) for i in range(6)
    ]
    timeline = build_timeline(fps=F24, tracks=[("T", clips)])
    assert len(find_conflicts(timeline)) == 15  # C(6, 2)


# ------------------------------------------------------------- validation

def test_negative_duration_rejected():
    timeline = build_timeline(
        fps=F24, tracks=[("T", [Clip(id="N", start=f(0), duration=f(-1))])]
    )
    with pytest.raises(ValidationError):
        validate_timeline(timeline)


def test_zero_frame_rate_rejected():
    timeline = build_timeline(
        fps=Fraction(0), tracks=[("T", [Clip(id="N", start=f(0), duration=f(1))])]
    )
    with pytest.raises(ValidationError):
        validate_timeline(timeline)


def test_duplicate_ids_rejected_across_tracks():
    timeline = build_timeline(
        fps=F24,
        tracks=[
            ("A", [Clip(id="DUP", start=f(0), duration=f(1))]),
            ("B", [Clip(id="DUP", start=f(5), duration=f(1))]),
        ],
    )
    with pytest.raises(ValidationError):
        validate_timeline(timeline)


def test_non_frame_aligned_time_rejected():
    timeline = build_timeline(
        fps=F24,
        tracks=[("T", [Clip(id="S", start=Fraction(1, 48), duration=f(1))])],
    )
    with pytest.raises(ValidationError):
        validate_timeline(timeline)


def test_clip_limit_raises_limit_error():
    timeline = fixtures.demo_timeline()
    with pytest.raises(LimitError) as excinfo:
        validate_timeline(timeline, max_clips=2)
    assert excinfo.value.exit_code == 3


# -------------------------------------------------------------- exit codes

def test_cli_corrupt_input_exits_2(tmp_path, capsys):
    bad = tmp_path / "bad.json"
    bad.write_text("{not json", encoding="utf-8")
    assert cli.main(["validate", "--input", str(bad)]) == 2
    assert "error[E_FORMAT]" in capsys.readouterr().err


def test_cli_missing_input_exits_2(tmp_path, capsys):
    assert cli.main(["validate", "--input", str(tmp_path / "nope.json")]) == 2
    assert "error[E_FORMAT]" in capsys.readouterr().err


def test_cli_limit_exits_3(capsys):
    assert cli.main(["demo", "--max-clips", "2"]) == 3
    assert "error[E_LIMIT]" in capsys.readouterr().err


def test_cli_otio_failure_exits_4(capsys):
    code = cli.main(
        ["demo", "--otio-out", "/nonexistent-dir-xyz/demo.otio"]
    )
    assert code == 4
    assert "error[E_OTIO_EXPORT]" in capsys.readouterr().err


def test_cli_resolve_writes_output(tmp_path):
    src = tmp_path / "in.json"
    src.write_text(
        __import__("clipweave.exchange", fromlist=["dumps"]).dumps(
            fixtures.demo_timeline(), 2
        ),
        encoding="utf-8",
    )
    out = tmp_path / "resolved.json"
    code = cli.main(
        ["resolve", "--input", str(src), "--output", str(out),
         "--report-json", "-"]
    )
    assert code == 0
    from clipweave import exchange

    resolved = exchange.load_file(str(out))
    assert [c.id for c in resolved.tracks[0].clips] == ["A", "C", "B"]
