"""Pipeline tests: path parity, boundaries, validation, exit codes."""

from __future__ import annotations

import json
from pathlib import Path

import numpy as np
import pytest
import soundfile as sf

from mixforge import cli
from mixforge.automation import GainCurve
from mixforge.engine import render_to_buffer, render_to_file
from mixforge.errors import ManifestError, MediaError
from mixforge.graph import MixGraph
from mixforge.manifest import AutomationPoint, load_manifest
from mixforge.metrics import ClipMeter
from mixforge.resampler import StreamResampler, output_frames


def _tone(rate, freq, amp, seconds=0.3, channels=1):
    n = int(round(rate * seconds))
    t = np.arange(n) / rate
    sig = amp * np.sin(2 * np.pi * freq * t)
    return np.column_stack([sig] * channels) if channels > 1 else sig


def _make_project(tmp_path: Path, tracks=((32000, 1), (44100, 1), (48000, 2))):
    entries = []
    for i, (rate, ch) in enumerate(tracks):
        name = f"t{i}"
        sf.write(str(tmp_path / f"{name}.wav"), _tone(rate, 200 + 90 * i, 0.3, channels=ch),
                 rate, subtype="FLOAT")
        entries.append({"name": name, "path": f"{name}.wav", "pan": 0.2 * (i - 1)})
    (tmp_path / "m.json").write_text(json.dumps({"tracks": entries}))
    return load_manifest(tmp_path / "m.json")


# --- resampler -------------------------------------------------------------

def test_output_frame_rounding():
    assert output_frames(38400, 32000, 48000) == 57600
    assert output_frames(52920, 44100, 48000) == 57600
    assert output_frames(100, 44100, 48000) == round(100 * 48000 / 44100)


@pytest.mark.parametrize("in_rate", [32000, 44100, 48000])
def test_resampler_block_size_independent(in_rate):
    src = _tone(in_rate, 440.0, 0.5, seconds=0.1)
    outs = []
    for chunk in (1, 7, 1024, len(src)):
        r = StreamResampler(in_rate, 48000, 1, len(src))
        got = []
        for i in range(0, len(src), chunk):
            r.feed(src[i : i + chunk])
            got.append(r.read(1 << 20))
        r.finish()
        got.append(r.read(1 << 20))
        outs.append(np.concatenate(got))
    assert all(len(o) == output_frames(len(src), in_rate, 48000) for o in outs)
    for o in outs[1:]:
        np.testing.assert_array_equal(outs[0], o)


def test_resampler_rejects_overfeed():
    r = StreamResampler(48000, 48000, 1, 4)
    r.feed(np.zeros(4))
    with pytest.raises(ValueError):
        r.feed(np.zeros(1))


# --- automation ------------------------------------------------------------

def test_automation_modes_and_clamping():
    pts = [AutomationPoint(0, -6.0), AutomationPoint(10, 0.0), AutomationPoint(20, -6.0)]
    hold = GainCurve(pts, "hold")
    assert hold.gain_db_at(np.array([5]))[0] == -6.0
    lin = GainCurve(pts, "linear")
    assert lin.gain_db_at(np.array([5]))[0] == pytest.approx(-3.0)
    cub = GainCurve(pts, "cubic")
    mid = cub.gain_db_at(np.arange(21))
    assert np.all(np.diff(mid[:10]) > 0) and np.all(np.diff(mid[10:]) < 0)  # monotone legs
    for curve in (hold, lin, cub):  # outside the range the endpoints hold
        assert curve.gain_db_at(np.array([-5]))[0] == -6.0
        assert curve.gain_db_at(np.array([99]))[0] == -6.0
    assert cub.sample_points()[0]["linear"] == pytest.approx(10 ** (-6 / 20))


def test_automation_rejects_unsorted():
    with pytest.raises(ManifestError):
        GainCurve([AutomationPoint(10, 0.0), AutomationPoint(5, 0.0)], "linear")


# --- manifest --------------------------------------------------------------

def test_manifest_validation(tmp_path):
    def write(payload):
        (tmp_path / "bad.json").write_text(json.dumps(payload))
        return tmp_path / "bad.json"

    with pytest.raises(ManifestError, match="empty project"):
        load_manifest(write({"tracks": []}))
    dup = {"tracks": [{"name": "a", "path": "x.wav"}, {"name": "a", "path": "y.wav"}]}
    with pytest.raises(ManifestError, match="duplicate"):
        load_manifest(write(dup))
    neg = {"tracks": [{"name": "a", "path": "x.wav", "start_frame": -1}]}
    with pytest.raises(ManifestError, match="start_frame"):
        load_manifest(write(neg))
    bad_auto = {"tracks": [{"name": "a", "path": "x.wav",
                            "automation": [{"frame": 5, "db": 0}, {"frame": 5, "db": 1}]}]}
    with pytest.raises(ManifestError, match="increasing"):
        load_manifest(write(bad_auto))
    with pytest.raises(ManifestError):
        load_manifest(tmp_path / "missing.json")


# --- pipeline parity -------------------------------------------------------

@pytest.mark.parametrize("block", [1, 257, 1024, 8192])
def test_offline_stream_parity(tmp_path, block):
    project = _make_project(tmp_path)
    with MixGraph(project, 48000) as g:
        ref, _ = render_to_buffer(g, 8192)
    with MixGraph(project, 48000) as g:
        got, _ = render_to_buffer(g, block)
    assert got.shape == ref.shape
    assert np.abs(got - ref).max() <= 1e-6


def test_block_remainder_and_determinism(tmp_path):
    project = _make_project(tmp_path)
    with MixGraph(project, 48000) as g:
        assert g.frames == 14400  # 0.3 s @ 48 kHz, 14400 % 257 != 0
        a, _ = render_to_buffer(g, 257)
    with MixGraph(project, 48000) as g:
        b, _ = render_to_buffer(g, 257)
    assert len(a) == 14400
    np.testing.assert_array_equal(a, b)


def test_start_offset_and_empty_frames(tmp_path):
    project = _make_project(tmp_path, tracks=((48000, 1),))
    spec = project.tracks[0]
    offset = type(spec)(**{**spec.__dict__, "start_frame": 4800})
    project = type(project)(tracks=(offset,), target_rate=48000)
    with MixGraph(project, 48000) as g:
        assert g.frames == 4800 + 14400
        mix, contribs = g.render_block(0, 4800)
        assert np.abs(mix).max() == 0.0


# --- metrics / errors ------------------------------------------------------

def test_clip_meter_counts_per_channel_sample():
    m = ClipMeter()
    m.update(np.array([[1.5, -0.5], [-1.01, 2.0], [1.0, 0.9]]))
    assert m.count == 3  # |x| > 1 strictly, per channel sample


def test_output_failure_is_atomic(tmp_path):
    project = _make_project(tmp_path, tracks=((48000, 1),))
    missing_dir = tmp_path / "nope" / "out.wav"
    with MixGraph(project, 48000) as g, pytest.raises(MediaError, match="cannot write"):
        render_to_file(g, missing_dir, 1024)
    assert not missing_dir.exists()
    assert list(tmp_path.glob("*.tmp-*")) == []


def test_corrupt_audio_has_track_context(tmp_path):
    (tmp_path / "bad.wav").write_bytes(b"not audio")
    (tmp_path / "m.json").write_text(json.dumps(
        {"tracks": [{"name": "broken", "path": "bad.wav"}]}))
    project = load_manifest(tmp_path / "m.json")
    with pytest.raises(MediaError, match="broken"):
        MixGraph(project, 48000)


# --- CLI end to end --------------------------------------------------------

def test_demo_cli(tmp_path):
    out, report = tmp_path / "mix.wav", tmp_path / "report.json"
    rc = cli.main(["demo", "--out", str(out), "--report", str(report),
                   "--target-rate", "48000", "--block-frames", "257",
                   "--seed", "23", "--stress"])
    assert rc == 0
    data = json.loads(report.read_text())
    assert data["tracks"] == 3 and data["rate"] == 48000 and data["frames"] == 57600
    assert data["offline_stream_max_abs"] <= 1e-6
    assert 0.35 <= max(m["peak"] for m in data["track_metrics"]) <= 0.45
    assert len(data["automation_samples"]) == 3
    assert 8_000 <= data["clip_samples"] <= 10_500


def test_cli_exit_codes(tmp_path):
    out = tmp_path / "o.wav"
    assert cli.main(["demo", "--out", str(out), "--block-frames", "0"]) == 2
    assert cli.main(["render", "--manifest", str(tmp_path / "none.json"),
                     "--out", str(out)]) == 2
    (tmp_path / "bad.wav").write_bytes(b"junk")
    (tmp_path / "m.json").write_text(json.dumps(
        {"tracks": [{"name": "x", "path": "bad.wav"}]}))
    assert cli.main(["render", "--manifest", str(tmp_path / "m.json"),
                     "--out", str(out)]) == 3
    assert cli.main(["demo", "--out", str(out), "--seed", "23", "--stress",
                     "--max-clip-samples", "10"]) == 4
