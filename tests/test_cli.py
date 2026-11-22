"""Tests for the command-line interface: summaries, exit codes and outputs."""

from __future__ import annotations

import json

from timeline_core.cli import main

FPS = 24


def test_cli_demo_json_matches_acceptance_fields(capsys):
    code = main(["demo", "--fps", "24", "--format", "v1", "--json"])
    assert code == 0
    summary = json.loads(capsys.readouterr().out)
    assert summary["conflict_count"] == 2
    assert summary["resolved_intervals"] == [
        {"start": 0, "end": 72, "id": "A"},
        {"start": 72, "end": 168, "id": "B"},
        {"start": 168, "end": 216, "id": "C"},
    ]
    assert summary["duration_deviation_frames"] == 0
    assert summary["rendered_frames"] == 216


def test_cli_v2_and_reversed_order_identical(capsys):
    main(["demo", "--fps", "24", "--format", "v2", "--json"])
    forward = json.loads(capsys.readouterr().out)
    main(["demo", "--fps", "24", "--format", "v2", "--reverse", "--json"])
    reversed_out = json.loads(capsys.readouterr().out)
    for field in (
        "conflict_count",
        "resolved_intervals",
        "duration_deviation_frames",
        "rendered_frames",
    ):
        assert forward[field] == reversed_out[field]


def test_cli_bad_input_returns_exit_2(tmp_path, capsys):
    bad = tmp_path / "bad.json"
    bad.write_text(json.dumps({"version": 42, "fps": FPS, "clips": []}))
    code = main(["run", str(bad), "--format", "v1"])
    assert code == 2
    assert "input error" in capsys.readouterr().err


def test_cli_missing_file_returns_exit_2(capsys):
    code = main(["run", "/nonexistent/timeline.json", "--format", "v2"])
    assert code == 2


def test_cli_writes_render_manifest_only_on_success(tmp_path):
    out = tmp_path / "manifest.json"
    code = main([
        "demo", "--fps", "24", "--format", "v1",
        "--render-out", str(out),
    ])
    assert code == 0 and out.exists()
    payload = json.loads(out.read_text())
    assert payload["rendered_frames"] == 216

    bad = tmp_path / "bad.json"
    bad.write_text('{"version": 42}')
    failed_out = tmp_path / "should_not_exist.json"
    code = main([
        "run", str(bad), "--format", "v1",
        "--render-out", str(failed_out),
    ])
    assert code == 2 and not failed_out.exists()
