"""End-to-end pipeline, CLI contract and output consistency tests."""

from __future__ import annotations

import json
import subprocess
import sys
from fractions import Fraction
from pathlib import Path

import pytest

from frameaudit.cli import main
from frameaudit.errors import InputError, LimitExceededError, OutputWriteError
from frameaudit.pipeline import run_audit


def test_pipeline_audits_fixture_with_all_metrics(fixture_video, fixture_manifest):
    stats = run_audit(
        fixture_video,
        fps=Fraction(30000, 1001),
        jump_threshold=2.0,
        window_size=16,
    )
    assert stats.decoded_frames == fixture_manifest["frame_count"] == 120
    assert stats.frame_type_counts["B"] > 0
    assert stats.frame_type_counts["I"] > 0
    # Every decoded frame is accounted for, including OTHER if present.
    assert sum(stats.frame_type_counts.values()) == stats.decoded_frames
    # Three container-level faults are repaired; jump exceeds the threshold.
    assert stats.repaired_timestamps == 3
    assert stats.max_jump_seconds > 2.0
    assert stats.reorder_overflows == 0
    assert len(stats.frames) == 120
    timestamps = [f.repaired_pts_seconds for f in stats.frames]
    assert all(
        timestamps[i] < timestamps[i + 1] for i in range(len(timestamps) - 1)
    )


def test_terminal_and_json_share_identical_stats(fixture_video, tmp_path):
    stats = run_audit(fixture_video, fps=Fraction(30000, 1001))
    json_path = tmp_path / "report.json"
    from frameaudit.outputs import build_report, render_summary, write_json_report

    write_json_report(stats, json_path)
    report = json.loads(json_path.read_text(encoding="utf-8"))
    summary = render_summary(stats)

    # The same stats object feeds both outputs; key figures must agree.
    assert report["decoded_frames"] == stats.decoded_frames
    assert f"decoded_frames={stats.decoded_frames}" in summary
    assert report["repaired_timestamps"] == stats.repaired_timestamps
    assert (
        f"repaired_timestamps={stats.repaired_timestamps}" in summary
    )
    assert report["reorder_overflows"] == stats.reorder_overflows
    assert report["frame_types"] == stats.frame_type_counts
    assert len(report["frames"]) == stats.decoded_frames
    # JSON carries per-frame repair records with the required fields.
    entry = report["frames"][0]
    for field in (
        "order_index",
        "decode_index",
        "frame_type",
        "original_pts",
        "repaired_pts_seconds",
        "timestamp_repaired",
        "repair_reasons",
        "reorder_overflow",
    ):
        assert field in entry


def test_cli_success_exit_code_and_json(fixture_video, tmp_path):
    json_path = tmp_path / "out.json"
    code = main(
        [
            str(fixture_video),
            "--fps",
            "30000/1001",
            "--jump-threshold",
            "2.0",
            "--json-out",
            str(json_path),
        ]
    )
    assert code == 0
    assert json_path.exists()
    report = json.loads(json_path.read_text(encoding="utf-8"))
    assert report["decoded_frames"] == 120


def test_cli_missing_file_returns_exit_code_2(tmp_path):
    code = main([str(tmp_path / "absent.mkv")])
    assert code == 2


def test_cli_audio_only_returns_exit_code_2(tmp_path):
    import av
    import numpy as np

    audio = tmp_path / "audio.aac"
    container = av.open(str(audio), "w", format="adts")
    stream = container.add_stream("aac", rate=48000)
    stream.layout = "stereo"
    rng = np.random.default_rng(0)
    for i in range(4):
        data = (
            rng.standard_normal((1024, 2)) * 8000
        ).astype(np.int16).reshape(1, -1)
        frame = av.AudioFrame.from_ndarray(
            data, format="s16", layout="stereo"
        )
        frame.sample_rate = 48000
        frame.pts = i * 1024
        for packet in stream.encode(frame):
            container.mux(packet)
    for packet in stream.encode():
        container.mux(packet)
    container.close()

    assert main([str(audio)]) == 2


def test_cli_invalid_arguments_return_exit_code_2(fixture_video):
    assert main([str(fixture_video), "--jump-threshold", "0"]) == 2
    assert main([str(fixture_video), "--fps", "nonsense"]) == 2
    assert main(
        [
            str(fixture_video),
            "--fps",
            "30000/1001",
            "--frame-interval",
            "0.04",
        ]
    ) == 2
    assert main([str(fixture_video), "--reorder-window", "0"]) == 2


def test_cli_json_write_failure_returns_exit_code_1(fixture_video):
    code = main(
        [
            str(fixture_video),
            "--json-out",
            "/nonexistent-directory-xyz/report.json",
        ]
    )
    assert code == 1


def test_cli_frame_cap_returns_exit_code_1(fixture_video):
    code = main([str(fixture_video), "--max-frames", "5"])
    assert code == 1


def test_pipeline_raises_on_missing_input(tmp_path):
    with pytest.raises(InputError):
        run_audit(tmp_path / "nope.mkv")


def test_pipeline_frame_limit_enforced(fixture_video):
    with pytest.raises(LimitExceededError):
        run_audit(fixture_video, max_frames=5)


def test_json_atomic_write_replaces_complete_file(fixture_video, tmp_path):
    stats = run_audit(fixture_video, fps=Fraction(30000, 1001))
    target = tmp_path / "atomic.json"
    target.write_text("stale", encoding="utf-8")
    from frameaudit.outputs import write_json_report

    write_json_report(stats, target)
    # No leftover temp files and the content is the complete valid JSON.
    leftovers = list(tmp_path.glob(".*.tmp"))
    assert leftovers == []
    report = json.loads(target.read_text(encoding="utf-8"))
    assert report["decoded_frames"] == 120


def test_write_failure_does_not_report_success(tmp_path):
    from frameaudit.model import AuditStats

    stats = AuditStats(
        input_path="x",
        container_format="mpegts",
        video_codec="h264",
        time_base="1/90000",
        average_rate="30000/1001",
        decoded_frames=1,
        frame_type_counts={"I": 1, "P": 0, "B": 0, "OTHER": 0},
        repaired_timestamps=0,
        max_jump_seconds=0.0,
        reorder_overflows=0,
        frames=[],
    )
    with pytest.raises(OutputWriteError):
        from frameaudit.outputs import write_json_report

        write_json_report(
            stats, tmp_path / "no-such-dir" / "r.json"
        )


def test_module_invocation_matches_console_entry(fixture_video):
    result = subprocess.run(
        [
            sys.executable,
            "-m",
            "frameaudit.cli",
            str(fixture_video),
            "--fps",
            "30000/1001",
        ],
        capture_output=True,
        text=True,
    )
    assert result.returncode == 0
    assert "decoded_frames=120" in result.stdout
    assert "frame_types=" in result.stdout
    assert "repaired_timestamps=" in result.stdout
    assert "max_jump_seconds=" in result.stdout
    assert "reorder_overflows=" in result.stdout


def test_per_frame_overflow_flags_match_aggregate(fixture_video):
    wide = run_audit(fixture_video, fps=Fraction(30000, 1001), window_size=16)
    assert wide.reorder_overflows == 0
    assert all(not f.reorder_overflow for f in wide.frames)

    shallow = run_audit(
        fixture_video, fps=Fraction(30000, 1001), window_size=1
    )
    flagged = [f for f in shallow.frames if f.reorder_overflow]
    assert shallow.reorder_overflows > 0
    assert len(flagged) == shallow.reorder_overflows
    # No frame is lost when overflows occur.
    assert len(shallow.frames) == shallow.decoded_frames
