"""End-to-end and unit coverage for the mixforge processing chain."""

from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path

import numpy as np
import pytest
import soundfile as sf

from mixforge.automation import GainCurve
from mixforge.cli import main
from mixforge.errors import EXIT_CLIP, EXIT_INVALID, EXIT_IO, AssetError, ManifestError
from mixforge.metrics import LevelStats
from mixforge.project import load_project
from mixforge.render import render_offline, render_stream
from mixforge.mixer import MixGraph
from mixforge.resample import output_frame_count, resample

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "make_fixture.py"


def make_wav(path: Path, data: np.ndarray, rate: int) -> None:
    sf.write(str(path), data, rate, subtype="FLOAT", format="WAV")


def make_project(tmp_path: Path, tracks: list[dict], **overrides) -> Path:
    project = {
        "project_rate": 48_000,
        "frames": 4_800,
        "channels": 2,
        "clip_policy": "preserve",
        "automation_interval": 0.1,
        "output": "mix.wav",
        "tracks": tracks,
    }
    project.update(overrides)
    path = tmp_path / "project.json"
    path.write_text(json.dumps(project), encoding="utf-8")
    return path


def mono_track(name: str, file_name: str, **overrides) -> dict:
    track = {
        "name": name, "file": file_name, "start_frame": 0,
        "gain_db": 0.0, "layout": {"mode": "mono_copy"},
    }
    track.update(overrides)
    return track


def run_cli(project_path: Path, report_path: Path, mode: str = "offline",
            block: int = 1024) -> int:
    return main(["render", "--project", str(project_path), "--mode", mode,
                 "--block-frames", str(block), "--report", str(report_path)])


# ---------------------------------------------------------------- validation

def test_duplicate_track_names_rejected(tmp_path):
    make_wav(tmp_path / "a.wav", np.zeros((100, 1)), 48_000)
    path = make_project(tmp_path, [
        mono_track("dup", "a.wav"), mono_track("dup", "a.wav")])
    with pytest.raises(ManifestError, match="duplicate track name"):
        load_project(path)


def test_negative_start_and_non_finite_rejected(tmp_path):
    make_wav(tmp_path / "a.wav", np.zeros((100, 1)), 48_000)
    path = make_project(tmp_path, [
        mono_track("t", "a.wav", start_frame=-1)])
    with pytest.raises(ManifestError, match="start_frame"):
        load_project(path)

    path = make_project(tmp_path, [
        mono_track("t", "a.wav", gain_db=float("nan"))])
    with pytest.raises(ManifestError, match="gain_db"):
        load_project(path)


def test_unreadable_file_is_asset_error(tmp_path):
    path = make_project(tmp_path, [mono_track("t", "missing.wav")])
    with pytest.raises(AssetError) as exc:
        MixGraph(load_project(path))
    assert exc.value.exit_code == EXIT_IO


def test_missing_manifest_exit_code_3(tmp_path):
    code = run_cli(tmp_path / "nope.json", tmp_path / "r.json")
    assert code == EXIT_IO


def test_unsorted_and_duplicate_automation_times(tmp_path):
    make_wav(tmp_path / "a.wav", np.zeros((100, 1)), 48_000)
    points = [
        {"time": 0.1, "gain_db": 0.0},
        {"time": 0.1, "gain_db": -1.0},
    ]
    path = make_project(tmp_path, [
        mono_track("t", "a.wav", automation=points)])
    with pytest.raises(ManifestError, match="unique times"):
        load_project(path)


def test_limit_exceeded_reported(tmp_path):
    make_wav(tmp_path / "a.wav", np.zeros((100, 1)), 48_000)
    path = make_project(tmp_path, [mono_track("t", "a.wav")], frames=-1)
    with pytest.raises(ManifestError, match="frames"):
        load_project(path)


# ------------------------------------------------------------- resample/curve

def test_resample_frame_count_and_identity():
    signal = np.random.default_rng(0).normal(size=(1000, 2))
    assert output_frame_count(1000, 44_100, 48_000) == 1088
    out = resample(signal, 48_000, 48_000)
    assert out.shape == signal.shape
    np.testing.assert_array_equal(out, signal)
    up = resample(signal[:1], 48_000, 96_000)  # single sample survives
    assert up.shape == (2, 2) and np.isfinite(up).all()
    empty = resample(np.zeros((0, 2)), 48_000, 24_000)
    assert empty.shape == (0, 2)


def test_hold_interpolation_and_mutes():
    spec = type("S", (), {})()
    spec.gain_db = 0.0
    from mixforge.project import AutomationPoint, MuteSegment
    spec.automation = [AutomationPoint(0.0, -6.0, "linear"),
                       AutomationPoint(0.5, 0.0, "hold")]
    spec.mutes = [MuteSegment(0.25, 0.5)]
    curve = GainCurve(spec, 100)
    times = np.array([0.0, 0.25, 0.75])
    # Hold segment keeps the 0.5s value for every later time.
    assert curve.automation_db(times)[2] == pytest.approx(0.0)
    mult = curve.multiplier(0, 100)
    assert mult[30] == 0.0  # inside the mute interval
    assert mult[60] > 0.0


def test_stats_cover_silence_and_tail_blocks():
    stats = LevelStats(1.0)
    stats.add(np.zeros((100, 2)))
    stats.add(np.full((7, 2), 0.5))
    assert stats.clip_count == 0
    stats.add(np.full((3, 1), 1.5))
    assert stats.clip_count == 3
    report = stats.to_dict()
    assert set(report) == {"peak_dbfs", "rms_dbfs", "clip_count"}


# ------------------------------------------------------------------- pipeline

def test_offline_and_stream_match(tmp_path):
    rate = 48_000
    make_wav(tmp_path / "a.wav",
             0.5 * np.sin(2 * np.pi * 440 * np.arange(rate) / rate).reshape(-1, 1),
             rate)
    make_wav(tmp_path / "b.wav",
             0.3 * np.ones((2_400, 1)), rate)
    path = make_project(tmp_path, [
        mono_track("a", "a.wav"),
        mono_track("b", "b.wav", start_frame=1_200)],
        automation_interval=0.02)

    project = load_project(path)
    graph_a = MixGraph(project)
    offline = render_offline(graph_a, 1024)
    graph_b = MixGraph(load_project(path))
    streamed = render_stream(graph_b, 1024)
    np.testing.assert_allclose(offline, streamed, atol=1e-12)

    report_off = tmp_path / "off.json"
    assert run_cli(path, report_off, "offline", 1024) == 0
    report_str = tmp_path / "str.json"
    assert run_cli(path, report_str, "stream", 1024) == 0
    off = json.loads(report_off.read_text())
    str_report = json.loads(report_str.read_text())
    assert off["frames"] == str_report["frames"]
    for key in ("bus", "tracks"):
        assert off[key] == str_report[key]
    assert len(off["tracks"][0]["automation_samples"]) >= 5


def test_polarity_cancellation_on_bus(tmp_path):
    rate = 48_000
    tone = 0.4 * np.sin(2 * np.pi * 330 * np.arange(2_400) / rate).reshape(-1, 1)
    make_wav(tmp_path / "pos.wav", tone, rate)
    make_wav(tmp_path / "neg.wav", -tone, rate)
    path = make_project(tmp_path, [
        mono_track("pos", "pos.wav"), mono_track("neg", "neg.wav")])
    project = load_project(path)
    graph = MixGraph(project)
    out = render_offline(graph, 512)
    assert np.max(np.abs(out[:2_400])) < 1e-12
    assert graph.bus_stats.peak == pytest.approx(0.0, abs=1e-12)


def test_clipping_policies(tmp_path):
    make_wav(tmp_path / "hot.wav", np.full((1_024, 1), 1.5), 48_000)
    path = make_project(tmp_path, [mono_track("hot", "hot.wav")],
                        clip_policy="error")
    graph = MixGraph(load_project(path))
    with pytest.raises(Exception) as exc:
        render_offline(graph, 256)
    assert exc.value.exit_code == EXIT_CLIP
    assert not (tmp_path / "mix.wav").exists()  # no artifact on error policy

    path = make_project(tmp_path, [mono_track("hot", "hot.wav")],
                        clip_policy="hard_limit")
    graph = MixGraph(load_project(path))
    out = render_offline(graph, 256)
    assert np.max(np.abs(out)) <= 1.0
    assert graph.bus_stats.clip_count > 0  # counted before limiting


def test_layout_mismatch_never_drops_channels(tmp_path):
    make_wav(tmp_path / "stereo.wav", np.ones((100, 2)), 48_000)
    path = make_project(tmp_path, [
        mono_track("t", "stereo.wav")])
    with pytest.raises(ManifestError, match="mono_copy") as exc:
        MixGraph(load_project(path))
    assert exc.value.exit_code == EXIT_INVALID


def test_stream_interruption_cleans_up(tmp_path):
    make_wav(tmp_path / "a.wav", np.ones((4_096, 1)), 48_000)
    path = make_project(tmp_path, [mono_track("a", "a.wav")])
    graph = MixGraph(load_project(path))
    with pytest.raises(Exception) as exc:
        render_stream(graph, 512, fault_after_blocks=2)
    assert exc.value.exit_code == EXIT_IO
    assert not (tmp_path / "mix.wav").exists()
    leftovers = [p for p in tmp_path.iterdir() if p.name.startswith(".mix.wav")]
    assert leftovers == []


def test_validation_failure_leaves_no_report(tmp_path):
    make_wav(tmp_path / "a.wav", np.ones((10, 1)), 48_000)
    path = make_project(tmp_path, [
        mono_track("a", "a.wav"), mono_track("a", "a.wav")])
    report = tmp_path / "r.json"
    assert run_cli(path, report) == EXIT_INVALID
    assert not report.exists()


# ------------------------------------------------------------------- acceptance

def test_shipped_fixture_acceptance(tmp_path):
    fixture = tmp_path / "fixture"
    subprocess.run([sys.executable, str(SCRIPT), "--out", str(fixture)],
                   check=True)
    project = fixture / "project.json"
    off_report = tmp_path / "offline.json"
    str_report = tmp_path / "stream.json"
    assert run_cli(project, off_report, "offline", 1024) == 0
    assert run_cli(project, str_report, "stream", 1024) == 0
    off, streamed = (json.loads(p.read_text())
                     for p in (off_report, str_report))
    assert off["frames"] == streamed["frames"] == 48_000
    assert off["track_count"] == 7
    assert all(len(t["automation_samples"]) >= 5 for t in off["tracks"])
    assert off["bus"]["clip_count"] > 0  # deliberate over-level fixture
    for a, b in zip(off["tracks"], streamed["tracks"], strict=True):
        for key in ("peak_dbfs", "rms_dbfs", "clip_count"):
            assert abs(a[key] - b[key]) <= 1e-5
    assert abs(off["bus"]["peak_dbfs"] - streamed["bus"]["peak_dbfs"]) <= 1e-5

    off_wav, _ = sf.read(str(fixture / "mix.wav"), always_2d=True)
    assert off_wav.shape == (48_000, 2)
    assert np.isfinite(off_wav).all()
