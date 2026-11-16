"""Happy-path pipeline, verification and reporting tests."""

from __future__ import annotations

import json
import os
from pathlib import Path

import numpy as np
import pytest
import soundfile as sf

from audioprep.cli import main
from audioprep.pipeline import run_batch
from audioprep.report import verify_report

from tests._pipeline_helpers import batch_args, make_inputs, write_wav


def test_batch_happy_path_and_report(tmp_path: Path):
    in_dir = tmp_path / "in"
    out_dir = tmp_path / "out"
    report = tmp_path / "report.json"
    make_inputs(in_dir)

    assert run_batch(batch_args(in_dir, out_dir, report)) == 0
    payload = json.loads(report.read_text())
    assert payload["files_ok"] == 2
    assert payload["files_failed"] == 0
    assert [r["relative_path"] for r in payload["records"]] == [
        "a_mono.wav",
        os.path.join("nested", "b_stereo.wav"),
    ]
    for rec in payload["records"]:
        assert rec["output_sample_rate"] == 48000
        assert rec["output_channels"] == 2
        assert rec["clipped_samples_after"] == 0
        assert rec["lufs_i_after"] == pytest.approx(-18.0, abs=0.5)
        assert np.isfinite(rec["rms_dbfs_before"])
        assert np.isfinite(rec["rms_dbfs_after"])
        assert (out_dir / rec["relative_path"]).is_file()


def test_verify_recomputes_and_matches(tmp_path: Path):
    in_dir, out_dir = tmp_path / "in", tmp_path / "out"
    report = tmp_path / "report.json"
    make_inputs(in_dir)
    assert run_batch(batch_args(in_dir, out_dir, report)) == 0
    assert verify_report(report, out_dir) == 0


def test_verify_detects_tampered_output(tmp_path: Path):
    in_dir, out_dir = tmp_path / "in", tmp_path / "out"
    report = tmp_path / "report.json"
    make_inputs(in_dir)
    run_batch(batch_args(in_dir, out_dir, report))
    target = out_dir / "a_mono.wav"
    data, sr = sf.read(str(target), always_2d=True)
    sf.write(str(target), data * 0.5, sr, subtype="PCM_24")
    assert verify_report(report, out_dir) == 3


def test_workers_2_keeps_path_sorted_report(tmp_path: Path):
    in_dir, out_dir = tmp_path / "in", tmp_path / "out"
    report = tmp_path / "report.json"
    make_inputs(in_dir)
    assert run_batch(batch_args(in_dir, out_dir, report, workers=2)) == 0
    payload = json.loads(report.read_text())
    names = [r["relative_path"] for r in payload["records"]]
    assert names == sorted(names)


def test_flac_input_is_supported(tmp_path: Path):
    in_dir, out_dir = tmp_path / "in", tmp_path / "out"
    report = tmp_path / "report.json"
    in_dir.mkdir(parents=True)
    sr = 48000
    t = np.arange(sr) / sr
    sf.write(
        str(in_dir / "lossless.flac"),
        (0.1 * np.sin(2 * np.pi * 440 * t))[:, None],
        sr,
        format="FLAC",
    )
    assert run_batch(batch_args(in_dir, out_dir, report)) == 0
    payload = json.loads(report.read_text())
    assert payload["files_ok"] == 1
    assert (out_dir / "lossless.wav").is_file()
    assert sf.info(str(out_dir / "lossless.wav")).subtype == "PCM_24"


def test_mono_and_keep_channel_modes(tmp_path: Path):
    in_dir = tmp_path / "in"
    sr = 48000
    t = np.arange(sr) / sr
    stereo = np.stack(
        [np.sin(2 * np.pi * 440 * t), np.sin(2 * np.pi * 880 * t)], axis=1
    )
    write_wav(in_dir / "s.wav", stereo, sr)

    out_mono = tmp_path / "mono"
    run_batch(batch_args(in_dir, out_mono, tmp_path / "m.json", channels="mono"))
    assert sf.info(str(out_mono / "s.wav")).channels == 1

    out_keep = tmp_path / "keep"
    run_batch(batch_args(in_dir, out_keep, tmp_path / "k.json", channels="keep"))
    assert sf.info(str(out_keep / "s.wav")).channels == 2


def test_block_size_does_not_change_results(tmp_path: Path):
    in_dir = tmp_path / "in"
    make_inputs(in_dir)
    reports = {}
    for block in (512, 4096):
        out = tmp_path / f"out{block}"
        rep = tmp_path / f"r{block}.json"
        assert run_batch(batch_args(in_dir, out, rep, block_frames=block)) == 0
        reports[block] = json.loads(rep.read_text())
    small = {r["relative_path"]: r for r in reports[512]["records"]}
    large = {r["relative_path"]: r for r in reports[4096]["records"]}
    assert small.keys() == large.keys()
    for name in small:
        for field in (
            "lufs_i_after",
            "rms_dbfs_after",
            "output_frames",
            "clipped_samples_after",
        ):
            assert small[name][field] == pytest.approx(large[name][field])


def test_cli_main_exit_codes(tmp_path: Path):
    in_dir, out_dir = tmp_path / "in", tmp_path / "out"
    report = tmp_path / "report.json"
    make_inputs(in_dir)
    argv = [
        "batch",
        "--input",
        str(in_dir),
        "--output",
        str(out_dir),
        "--report",
        str(report),
        "--sample-rate",
        "48000",
        "--channels",
        "stereo",
        "--target-lufs",
        "-18",
        "--peak-ceiling",
        "-1",
        "--block-frames",
        "4096",
        "--workers",
        "1",
    ]
    assert main(argv) == 0
    assert main(["verify", "--report", str(report), "--output", str(out_dir)]) == 0


def test_report_has_no_non_finite_tokens(tmp_path: Path):
    in_dir, out_dir = tmp_path / "in", tmp_path / "out"
    report = tmp_path / "report.json"
    make_inputs(in_dir)
    run_batch(batch_args(in_dir, out_dir, report))
    text = report.read_text()
    for token in ("NaN", "Infinity", "-Infinity"):
        assert token not in text
