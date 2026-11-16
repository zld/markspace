"""Error-recovery, safety-scan and edge-case pipeline tests."""

from __future__ import annotations

import json
from pathlib import Path

import numpy as np
import soundfile as sf

from audioprep.pipeline import run_batch

from tests._pipeline_helpers import batch_args, make_inputs, write_wav


def test_unsupported_extension_is_usage_error(tmp_path: Path):
    in_dir = tmp_path / "in"
    in_dir.mkdir()
    (in_dir / "notes.txt").write_text("not audio")
    code = run_batch(batch_args(in_dir, tmp_path / "out", tmp_path / "r.json"))
    assert code == 2


def test_missing_input_directory_returns_usage_error(tmp_path: Path):
    code = run_batch(
        batch_args(tmp_path / "nope", tmp_path / "out", tmp_path / "r.json")
    )
    assert code == 2


def test_invalid_params_return_usage_error(tmp_path: Path):
    in_dir = tmp_path / "in"
    make_inputs(in_dir)
    bad = batch_args(
        in_dir, tmp_path / "out", tmp_path / "r.json", peak_ceiling=1.5
    )
    assert run_batch(bad) == 2
    bad = batch_args(
        in_dir, tmp_path / "out2", tmp_path / "r2.json", block_frames=0
    )
    assert run_batch(bad) == 2


def test_zero_frame_file_is_recorded_and_batch_continues(tmp_path: Path):
    in_dir, out_dir = tmp_path / "in", tmp_path / "out"
    report = tmp_path / "report.json"
    make_inputs(in_dir)
    write_wav(in_dir / "empty.wav", np.zeros((0, 1), dtype=np.float64), 48000)

    assert run_batch(batch_args(in_dir, out_dir, report)) == 1
    payload = json.loads(report.read_text())
    assert payload["files_ok"] == 2
    assert payload["files_failed"] == 1
    err = payload["errors"][0]
    assert err["relative_path"] == "empty.wav"
    assert "zero-frame" in err["error"]
    assert not (out_dir / "empty.wav").exists()
    assert not list(out_dir.rglob("*.tmp*"))


def test_symlink_escape_is_rejected(tmp_path: Path):
    in_dir = tmp_path / "in"
    in_dir.mkdir()
    outside = tmp_path / "outside.wav"
    sr = 48000
    write_wav(outside, np.zeros((sr, 1)), sr)
    (in_dir / "link.wav").symlink_to(outside)
    code = run_batch(batch_args(in_dir, tmp_path / "out", tmp_path / "r.json"))
    assert code == 2


def test_symlink_inside_root_is_accepted(tmp_path: Path):
    in_dir = tmp_path / "in"
    in_dir.mkdir()
    sr = 48000
    write_wav(in_dir / "real.wav", 0.1 * np.ones((sr, 1)), sr)
    (in_dir / "alias.wav").symlink_to(in_dir / "real.wav")
    code = run_batch(batch_args(in_dir, tmp_path / "out", tmp_path / "r.json"))
    assert code == 0


def test_duplicate_output_target_is_rejected(tmp_path: Path):
    in_dir = tmp_path / "in"
    in_dir.mkdir()
    sr = 48000
    data = np.zeros((1000, 1))
    write_wav(in_dir / "song.wav", data, sr)
    sf.write(str(in_dir / "song.flac"), data, sr, format="FLAC")
    code = run_batch(batch_args(in_dir, tmp_path / "out", tmp_path / "r.json"))
    assert code == 2


def test_corrupt_file_fails_but_batch_continues(tmp_path: Path):
    in_dir, out_dir = tmp_path / "in", tmp_path / "out"
    report = tmp_path / "report.json"
    make_inputs(in_dir)
    (in_dir / "broken.wav").write_bytes(b"RIFF\x00\x00\x00\x00WAVEgarbage")
    assert run_batch(batch_args(in_dir, out_dir, report)) == 1
    payload = json.loads(report.read_text())
    assert payload["files_ok"] == 2
    assert payload["files_failed"] == 1
    assert payload["errors"][0]["relative_path"] == "broken.wav"


def test_non_finite_samples_are_rejected_per_file(tmp_path: Path):
    in_dir, out_dir = tmp_path / "in", tmp_path / "out"
    report = tmp_path / "report.json"
    make_inputs(in_dir)
    sr = 48000
    bad = np.full((sr, 1), 0.2, dtype=np.float64)
    bad[10] = np.inf
    sf.write(str(in_dir / "inf.wav"), bad, sr, subtype="FLOAT", format="WAV")
    assert run_batch(batch_args(in_dir, out_dir, report)) == 1
    payload = json.loads(report.read_text())
    failed = {e["relative_path"]: e["error"] for e in payload["errors"]}
    assert "inf.wav" in failed
    assert "non-finite" in failed["inf.wav"]


def test_unsupported_channel_count_is_per_file_failure(tmp_path: Path):
    in_dir, out_dir = tmp_path / "in", tmp_path / "out"
    report = tmp_path / "report.json"
    make_inputs(in_dir)
    sr = 48000
    three = np.zeros((sr, 3), dtype=np.float64)
    sf.write(str(in_dir / "surround.wav"), three, sr, subtype="PCM_24", format="WAV")
    assert run_batch(batch_args(in_dir, out_dir, report)) == 1
    payload = json.loads(report.read_text())
    failed = {e["relative_path"]: e["error"] for e in payload["errors"]}
    assert "surround.wav" in failed
    assert "channel" in failed["surround.wav"].lower()
    assert payload["files_ok"] == 2
