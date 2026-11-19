"""CLI exit codes, validation errors and degraded I/O chains."""

from __future__ import annotations

import json
from pathlib import Path

import numpy as np
import pytest
import soundfile as sf

from mixforge.cli import main
from mixforge.errors import AudioIOError, MixForgeError
from mixforge.io import write_json, write_wav

from conftest import base_spec


def write_spec(tmp_path: Path, spec: dict) -> Path:
    path = tmp_path / "project.json"
    write_json(path, spec)
    return path


def run_cli(capsys, *args: str) -> tuple[int, dict]:
    code = main(list(args))
    captured = capsys.readouterr().out
    return code, json.loads(captured)


def test_compare_mode_is_identical_and_writes_wav(fixture_dir, tmp_path, capsys):
    project = write_spec(fixture_dir.parent, base_spec())
    output = tmp_path / "mix.wav"
    code, payload = run_cli(
        capsys,
        "render",
        "--project",
        str(project),
        "--mode",
        "compare",
        "--block-size",
        "512",
        "--output",
        str(output),
    )
    assert code == 0
    assert payload["status"] == "ok"
    assert payload["max_abs_diff"] == 0.0
    assert payload["first_diff_sample"] is None
    assert payload["frames"] == 24_000
    assert payload["track_count"] == 3
    assert output.exists()
    info = sf.info(str(output))
    assert info.frames == 24_000 and info.samplerate == 48_000


@pytest.mark.parametrize(
    "mutate, expected_location",
    [
        (lambda spec: spec.pop("sample_rate"), "sample_rate"),
        (lambda spec: spec.update(duration_frames="5"), "duration_frames"),
        (lambda spec: spec["tracks"][0].update(start_frame=-4), "tracks[0].start"),
        (lambda spec: spec["tracks"].append(dict(spec["tracks"][1])), "tracks[3].id"),
        (
            lambda spec: spec["tracks"][0]["automation"]["points"].append(
                {"time": 0, "gain_db": -2.0}
            ),
            "tracks[0].automation.points[3].time",
        ),
        (lambda spec: spec["tracks"][0].update(path="assets/missing.wav"), "track[ramp].path"),
        (lambda spec: spec["master"]["limiter"].update(ceiling=2.0), "master.ceiling"),
    ],
)
def test_configuration_errors_exit_two(fixture_dir, tmp_path, capsys, mutate, expected_location):
    spec = base_spec()
    mutate(spec)
    project = write_spec(fixture_dir.parent, spec)
    code, payload = run_cli(capsys, "render", "--project", str(project), "--mode", "offline")
    assert code == 2
    assert payload["status"] == "error"
    assert expected_location in str(payload.get("location", "")), payload


def test_missing_project_exits_two(tmp_path, capsys):
    code, payload = run_cli(
        capsys, "render", "--project", str(tmp_path / "nope.json"), "--mode", "offline"
    )
    assert code == 2
    assert payload["error"] in {"io", "config"}


def test_malformed_json_exits_two(tmp_path, capsys):
    path = tmp_path / "broken.json"
    path.write_text("{not json", encoding="utf-8")
    code, payload = run_cli(capsys, "render", "--project", str(path))
    assert code == 2
    assert "invalid JSON" in payload["message"]


def test_non_finite_wav_rejected_at_read(fixture_dir, tmp_path, capsys):
    bad = fixture_dir / "bad.wav"
    write_wav(bad, np.zeros((64, 1)), 48_000)
    # Craft a float WAV containing NaN by writing bytes through soundfile.
    with sf.SoundFile(str(bad), mode="w", samplerate=48_000, channels=1, format="WAV", subtype="FLOAT") as handle:
        handle.write(np.array([[np.nan], [0.0]], dtype=np.float32))
    spec = base_spec()
    spec["tracks"] = [{"id": "ramp", "path": "assets/bad.wav"}]
    project = write_spec(fixture_dir.parent, spec)
    code, payload = run_cli(capsys, "render", "--project", str(project))
    assert code == 2
    assert "NaN" in payload["message"]


def _run_tampered_compare(fixture_dir, tmp_path, capsys, monkeypatch, bad_block, bad_frame):
    project = write_spec(fixture_dir.parent, base_spec())
    from mixforge import stream as stream_module

    original = stream_module.stream_render

    def sabotage(project, *, block_size):
        blocks, summary = original(project, block_size=block_size)

        def tampered():
            for index, block in enumerate(blocks):
                if index == bad_block:
                    block = block.copy()
                    block[bad_frame, 0] += 0.25
                yield block

        return tampered(), summary

    monkeypatch.setattr("mixforge.cli.stream_render", sabotage)
    output = tmp_path / "mix.wav"
    code, payload = run_cli(
        capsys,
        "render",
        "--project",
        str(project),
        "--mode",
        "compare",
        "--block-size",
        "64",
        "--output",
        str(output),
    )
    return code, payload, output


def test_mismatch_reports_first_diff_and_exits_one(fixture_dir, tmp_path, capsys, monkeypatch):
    code, payload, output = _run_tampered_compare(
        fixture_dir, tmp_path, capsys, monkeypatch, bad_block=3, bad_frame=5
    )
    assert code == 1
    assert payload["status"] == "mismatch"
    assert payload["max_abs_diff"] == pytest.approx(0.25)
    assert payload["first_diff_sample"] == (3 * 64 + 5) * 2
    assert not output.exists()


def test_first_diff_sample_is_correct_past_the_first_batch(
    fixture_dir, tmp_path, capsys, monkeypatch
):
    # Compare batches 16 blocks at a time; block 40 is in the third batch.
    bad_block, bad_frame = 40, 5
    code, payload, _ = _run_tampered_compare(
        fixture_dir, tmp_path, capsys, monkeypatch, bad_block=bad_block, bad_frame=bad_frame
    )
    assert code == 1
    assert payload["first_diff_sample"] == (bad_block * 64 + bad_frame) * 2


def test_stream_mode_writes_short_final_block(fixture_dir, tmp_path, capsys):
    project = write_spec(fixture_dir.parent, base_spec(duration_frames=1_000))
    output = tmp_path / "stream.wav"
    code, payload = run_cli(
        capsys,
        "render",
        "--project",
        str(project),
        "--mode",
        "stream",
        "--block-size",
        "300",
        "--output",
        str(output),
    )
    assert code == 0
    assert payload["mode"] == "stream"
    assert sf.info(str(output)).frames == 1_000


def test_wav_source_reports_track_and_block_context(fixture_dir):
    from mixforge.io import WavSource

    with WavSource(fixture_dir / "ramp.wav", track_id="ramp") as source:
        with pytest.raises(AudioIOError) as caught:
            source.read(9_000, 100, block_start=2_048, block_frames=512)
    assert caught.value.context == {
        "requested_start": 9_000,
        "requested_frames": 100,
        "source_frames": 2_400,
        "block_start": 2_048,
        "block_frames": 512,
    }


def test_inspect_reports_plan(fixture_dir, capsys):
    project = write_spec(fixture_dir.parent, base_spec())
    code, payload = run_cli(capsys, "inspect", "--project", str(project), "--block-size", "256")
    assert code == 0
    assert payload["tracks"][0]["automation"]["interpolation"] == "linear"
    assert payload["tracks"][0]["clip_frames"] == 4_800
    assert payload["loop"] is None
    assert payload["automation_samples"]


def test_inspect_rejects_fades_render_would_reject(fixture_dir, capsys):
    spec = base_spec()
    spec["tracks"] = [
        {"id": "ramp", "path": "assets/ramp.wav", "fade": {"in_frames": 8_000, "out_frames": 8_000}}
    ]
    project = write_spec(fixture_dir.parent, spec)
    render_code, render_payload = run_cli(capsys, "render", "--project", str(project))
    inspect_code, inspect_payload = run_cli(capsys, "inspect", "--project", str(project))
    assert render_code == inspect_code == 2
    assert "fade" in str(render_payload["location"])
    assert "fade" in str(inspect_payload["location"])


def test_inspect_rejects_missing_source(fixture_dir, capsys):
    spec = base_spec()
    spec["tracks"][0]["path"] = "assets/missing.wav"
    project = write_spec(fixture_dir.parent, spec)
    code, payload = run_cli(capsys, "inspect", "--project", str(project))
    assert code == 2
    assert "track[ramp]" in payload["location"]


@pytest.mark.parametrize("gain", [7_000, 1e9])
def test_extreme_gain_is_a_config_error_not_a_traceback(fixture_dir, capsys, gain):
    spec = base_spec()
    spec["tracks"][0]["gain_db"] = gain
    project = write_spec(fixture_dir.parent, spec)
    code, payload = run_cli(capsys, "render", "--project", str(project))
    assert code == 2
    assert "tracks[0].gain_db" in payload["location"]


def test_extreme_automation_gain_is_a_config_error(fixture_dir, capsys):
    spec = base_spec()
    spec["tracks"][0]["automation"]["points"][0]["gain_db"] = 7_000
    project = write_spec(fixture_dir.parent, spec)
    code, payload = run_cli(capsys, "inspect", "--project", str(project))
    assert code == 2
    assert "automation.points[0].gain_db" in payload["location"]


@pytest.mark.parametrize(
    "mutate, location",
    [
        (lambda spec: spec["tracks"][0].update(start_frame=2**63), "tracks[0].start"),
        (lambda spec: spec["tracks"][0].update(start_frame=2**53), "tracks[0].start"),
        (
            lambda spec: spec["tracks"][0]["automation"]["points"].__setitem__(
                0, {"time": 2**63, "gain_db": 0.0}
            ),
            "automation.points[0].time",
        ),
        (lambda spec: spec.update(duration_frames=2**63), "duration_frames"),
    ],
)
def test_huge_frame_values_are_config_errors(fixture_dir, capsys, mutate, location):
    spec = base_spec()
    mutate(spec)
    project = write_spec(fixture_dir.parent, spec)
    for command in ("render", "inspect"):
        code, payload = run_cli(capsys, command, "--project", str(project))
        assert code == 2, (command, payload)
        assert location in payload["location"], payload


def test_automation_summary_is_bounded_for_tiny_blocks(fixture_dir, capsys):
    # 24k frames at block size 1 would otherwise emit 72k boundary rows.
    project = write_spec(fixture_dir.parent, base_spec())
    code, payload = run_cli(
        capsys, "render", "--project", str(project), "--mode", "compare", "--block-size", "1"
    )
    assert code == 0
    assert payload["block_boundary_stride"] > 1
    rows = payload["automation_samples"]
    assert len(rows) < 20_000
    sampled = [row for row in rows if "block_boundary_sampled" in row["roles"]]
    assert sampled
    # Keyframes are never thinned out.
    keyframes = {row["frame"] for row in rows if "keyframe" in row["roles"]}
    assert keyframes == {0, 5_000, 13_000}
