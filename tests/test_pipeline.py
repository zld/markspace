"""End-to-end pipeline and failure-path tests through the CLI."""
import json
import subprocess
import sys

import numpy as np
import pytest
import soundfile as sf

from audionorm.cli import main
from audionorm.fixtures import generate_all


def run_demo(tmp_path, *extra):
    workdir = tmp_path / "demo"
    code = main(["demo", "--workdir", str(workdir),
                 "--target-rate", "48000", "--target-lufs", "-18",
                 "--jobs", "2", *extra])
    report = json.loads((workdir / "report.json").read_text())
    return code, report, workdir


def by_name(report, name):
    return next(f for f in report["files"] if f["source_path"].endswith(name))


def test_demo_end_to_end(tmp_path, capsys):
    code, report, workdir = run_demo(tmp_path)
    assert code == 0
    assert report["summary"]["failed"] == 0
    assert report["summary"]["succeeded"] == 6
    json.dumps(report, allow_nan=False)  # strict JSON, no NaN/Infinity

    rec = by_name(report, "stereo_tone_44k1.wav")
    for field in ("input_rate", "output_rate", "channels", "frames_in",
                  "frames_out", "rms_before", "rms_after", "lufs_before",
                  "lufs_after", "clipped_before", "clipped_after"):
        assert field in rec, field
    assert rec["input_rate"] == 44100
    assert rec["output_rate"] == 48000
    assert rec["channels"] == 2
    assert rec["frames_out"] == round(rec["frames_in"] * 48000 / 44100)
    assert rec["frames_out"] / rec["frames_in"] == pytest.approx(1.0884, abs=1e-3)
    assert rec["lufs_after"] == pytest.approx(-18.0, abs=1.0)
    assert rec["clipped_after"] == 0

    data, rate = sf.read(workdir / "output" / "stereo_tone_44k1.wav", always_2d=True)
    assert rate == 48000 and data.shape == (rec["frames_out"], 2)


def test_loudness_converges_for_all_non_silent(tmp_path):
    _, report, _ = run_demo(tmp_path)
    for rec in report["files"]:
        if rec["status"] == "ok" and not rec["silent"] and not rec["gain_limited"]:
            assert rec["lufs_after"] == pytest.approx(-18.0, abs=1.0)


def test_hot_file_gain_limited_no_clipping(tmp_path):
    _, report, _ = run_demo(tmp_path)
    rec = by_name(report, "hot_float.wav")
    assert rec["clipped_before"] > 0
    assert rec["clipped_after"] == 0
    assert rec["gain_limited"] is True


def test_silence_represented_as_null(tmp_path):
    _, report, _ = run_demo(tmp_path)
    rec = by_name(report, "silence.wav")
    assert rec["silent"] is True
    assert rec["lufs_before"] is None and rec["lufs_after"] is None
    assert rec["rms_before"] is None and rec["gain_db"] == 0.0


def test_single_frame_file_processed(tmp_path):
    _, report, _ = run_demo(tmp_path)
    rec = by_name(report, "single_frame.wav")
    assert rec["status"] == "ok" and rec["frames_in"] == 1


def test_corrupt_wav_fails_batch_continues(tmp_path):
    inp = tmp_path / "in"
    generate_all(inp)
    (inp / "broken.wav").write_bytes(b"this is not a wav file")
    out = tmp_path / "out"
    code = main(["run", "--input-dir", str(inp), "--output-dir", str(out),
                 "--target-rate", "48000", "--target-lufs", "-18"])
    assert code == 1
    report = json.loads((out / "report.json").read_text())
    bad = by_name(report, "broken.wav")
    assert bad["status"] == "failed" and bad["error"] == "E_DECODE"
    assert report["summary"]["succeeded"] == 6  # others unaffected


def test_nan_file_rejected(tmp_path):
    inp = tmp_path / "in"
    inp.mkdir()
    sf.write(str(inp / "nan.wav"), np.full((1000, 2), np.nan), 48000,
             subtype="FLOAT")
    out = tmp_path / "out"
    code = main(["run", "--input-dir", str(inp), "--output-dir", str(out)])
    assert code == 1
    rec = json.loads((out / "report.json").read_text())["files"][0]
    assert rec["error"] == "E_INVALID_DATA"
    assert not (out / "nan.wav").exists()  # no half-written output


def test_output_inside_input_is_config_error(tmp_path):
    inp = tmp_path / "in"
    inp.mkdir()
    code = main(["run", "--input-dir", str(inp), "--output-dir", str(inp / "out")])
    assert code == 2
    code = main(["run", "--input-dir", str(inp), "--output-dir", str(inp)])
    assert code == 2


def test_overwrite_skip_then_replace(tmp_path):
    inp = tmp_path / "in"
    generate_all(inp)
    out = tmp_path / "out"
    assert main(["run", "--input-dir", str(inp), "--output-dir", str(out)]) == 0
    code = main(["run", "--input-dir", str(inp), "--output-dir", str(out),
                 "--overwrite", "skip"])
    assert code == 0
    report = json.loads((out / "report.json").read_text())
    assert report["summary"]["skipped"] == 6
    code = main(["run", "--input-dir", str(inp), "--output-dir", str(out),
                 "--overwrite", "fail"])
    assert code == 1
    code = main(["run", "--input-dir", str(inp), "--output-dir", str(out),
                 "--overwrite", "replace"])
    assert code == 0


def test_report_is_strict_json_via_json_tool(tmp_path):
    _, _, workdir = run_demo(tmp_path)
    proc = subprocess.run(
        [sys.executable, "-m", "json.tool", str(workdir / "report.json")],
        capture_output=True)
    assert proc.returncode == 0


def test_stable_sorted_report(tmp_path):
    _, report, _ = run_demo(tmp_path)
    paths = [f["source_path"] for f in report["files"]]
    assert paths == sorted(paths)
