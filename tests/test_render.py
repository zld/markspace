"""Offline rendering: model validation, resampling, automation, policy."""

from __future__ import annotations

import json
import math
from pathlib import Path

import numpy as np
import pytest

from audiomix.automation import parse_automation
from audiomix.errors import AutomationError, ManifestError, TrackError
from audiomix.mixer import MixGraph, parse_policy
from audiomix.offline import render_offline
from audiomix.resample import output_frames, resample_linear

from conftest import build_session, ramp, write_wav


def _single_track(manifest: dict, session_dir: Path):
    return build_session(session_dir, manifest)


# -- manifest and track validation ------------------------------------------

def test_missing_track_file_names_track(session_dir: Path):
    manifest = {"channels": 1, "frames": 10,
                "tracks": [{"name": "missing", "path": "nope.wav"}]}
    with pytest.raises(TrackError) as info:
        _single_track(manifest, session_dir)
    assert info.value.track == "missing"
    assert "not found" in info.value.reason


def test_corrupt_wav_names_track(session_dir: Path):
    bad = session_dir / "bad.wav"
    bad.write_bytes(b"not a wav at all")
    manifest = {"channels": 1, "frames": 10,
                "tracks": [{"name": "broken", "path": "bad.wav"}]}
    session = _single_track(manifest, session_dir)
    with pytest.raises(TrackError) as info:
        MixGraph(session, 48000, parse_policy("report"))
    assert info.value.track == "broken"
    assert "read" in info.value.reason or "format" in info.value.reason


def test_channel_map_out_of_range_reports_track(session_dir: Path):
    write_wav(session_dir / "s.wav", ramp(10, 1), 48000)
    manifest = {"channels": 2, "tracks": [
        {"name": "map", "path": "s.wav", "channel_map": [0, 3]}]}
    with pytest.raises(TrackError) as info:
        _single_track(manifest, session_dir)
    assert info.value.track == "map"
    assert "outside" in info.value.reason


def test_path_escaping_manifest_dir_is_rejected(session_dir: Path):
    outside = session_dir.parent / "outside.wav"
    write_wav(outside, ramp(10, 1), 48000)
    manifest = {"channels": 1, "tracks": [
        {"name": "escape", "path": "../outside.wav"}]}
    with pytest.raises(TrackError) as info:
        _single_track(manifest, session_dir)
    assert "escapes" in info.value.reason


def test_empty_audio_file_reports_track(session_dir: Path):
    write_wav(session_dir / "empty.wav", np.zeros((0, 1)), 48000)
    manifest = {"channels": 1, "frames": 10,
                "tracks": [{"name": "empty", "path": "empty.wav"}]}
    session = _single_track(manifest, session_dir)
    with pytest.raises(TrackError) as info:
        MixGraph(session, 48000, parse_policy("report"))
    assert info.value.track == "empty"
    assert "no frames" in info.value.reason


def test_nonfinite_audio_reports_track(session_dir: Path):
    write_wav(session_dir / "nan.wav", np.array([[0.1], [np.nan], [0.2]]), 48000)
    manifest = {"channels": 1, "tracks": [{"name": "nan", "path": "nan.wav"}]}
    session = _single_track(manifest, session_dir)
    with pytest.raises(TrackError) as info:
        MixGraph(session, 48000, parse_policy("report"))
    assert info.value.track == "nan"
    assert "NaN" in info.value.reason


def test_manifest_rejects_empty_tracks():
    with pytest.raises(ManifestError):
        from audiomix.model import parse_manifest
        parse_manifest({"channels": 2, "tracks": []}, Path("/tmp/x/session.json"))


def test_manifest_rejects_absolute_path(tmp_path: Path):
    target = tmp_path / "abs.wav"
    write_wav(target, ramp(10, 1), 48000)
    with pytest.raises(TrackError) as info:
        build_session(tmp_path, {"channels": 1, "tracks": [
            {"name": "abs", "path": str(target)}]})
    assert "relative" in info.value.reason


# -- resampling ---------------------------------------------------------------

@pytest.mark.parametrize(
    "frames,src,dst,expected",
    [
        (0, 44100, 48000, 0),
        (1, 44100, 48000, 2),
        (44100, 44100, 48000, 48000),
        (100, 48000, 44100, 92),
        (5, 48000, 96000, 10),
    ],
)
def test_output_frames_ceil_rule(frames, src, dst, expected):
    assert output_frames(frames, src, dst) == expected


def test_resample_integer_ratio_is_exact():
    source = np.arange(200, dtype=np.float64)[:, None]
    doubled = resample_linear(source, 48000, 96000)[:, 0]
    assert doubled.shape == (400,)
    assert np.allclose(doubled[:399], np.arange(399, dtype=float) / 2.0)
    assert doubled[-1] == 199.0  # holds the last source sample


def test_resample_rejects_empty_source_with_signal_not_crash():
    result = resample_linear(np.zeros((0, 2)), 44100, 48000)
    assert result.shape == (0, 2)


def test_resample_one_frame_source_is_constant():
    result = resample_linear(np.array([[0.25]]), 8000, 48000)
    assert result.shape == (6, 1)
    assert np.all(result == 0.25)


def test_resample_rejects_nonfinite():
    from audiomix.errors import ResampleError
    with pytest.raises(ResampleError):
        resample_linear(np.array([[np.inf]]), 48000, 48000)


# -- automation ---------------------------------------------------------------

def test_db_interpolation_and_clamping():
    curve = parse_automation(
        [{"frame": 0, "value": -6.0, "unit": "db"},
         {"frame": 100, "value": 0.0, "unit": "db"}], "t")
    gains = curve.gain_at(np.array([-10, 0, 50, 100, 500]))
    assert gains[0] == pytest.approx(10 ** (-6 / 20))
    assert gains[2] == pytest.approx(10 ** (-3 / 20))
    assert gains[3] == gains[4] == pytest.approx(1.0)
    assert np.all(np.diff(gains) >= 0)


def test_linear_gain_sampling_is_linear():
    curve = parse_automation(
        [{"frame": 0, "value": 0.0}, {"frame": 10, "value": 1.0}], "t")
    assert curve.gain_at(np.array([0, 5, 10])).tolist() == [0.0, 0.5, 1.0]


@pytest.mark.parametrize("points", [
    [{"frame": 0, "value": 0.0}, {"frame": 0, "value": 1.0}],
    [{"frame": 0, "value": float("nan")}],
    [{"frame": 0, "value": -0.5}],
    [{"frame": -1, "value": 0.5}],
    [{"frame": 0, "value": 0.0, "unit": "db"},
     {"frame": 10, "value": 1.0, "unit": "linear"}],
    [],
])
def test_invalid_automation_is_rejected(points):
    with pytest.raises(AutomationError):
        parse_automation(points, "t")


def test_report_automation_includes_endpoints():
    curve = parse_automation(
        [{"frame": 10, "value": 0.5}, {"frame": 20, "value": 0.7}], "t")
    frames = [p["frame"] for p in curve.sampled_points(0, 30)]
    assert frames == [0, 10, 20, 30]


# -- offline rendering --------------------------------------------------------

def test_offline_gain_pan_and_length(session_dir: Path):
    write_wav(session_dir / "s.wav", ramp(100, 1, scale=0.5), 48000)
    session = build_session(session_dir, {
        "channels": 2, "frames": 200,
        "tracks": [{"name": "s", "path": "s.wav", "gain_db": -6.0, "pan": -1.0}]})
    result = render_offline(MixGraph(session, 48000, parse_policy("report")),
                            parse_policy("report"))
    assert result.samples.shape == (200, 2)
    assert result.frames == 200
    # pan -1.0 routes everything to the left channel at unity; -6 dB halves it
    assert np.allclose(result.samples[:, 1], 0.0)
    source = ramp(100, 1, scale=0.5)[:, 0]
    assert result.samples[50, 0] == pytest.approx(source[50] * 10 ** (-6 / 20), abs=1e-6)
    assert np.all(result.samples[100:] == 0.0)


def test_offline_resamples_and_pads_silence(session_dir: Path):
    write_wav(session_dir / "s.wav", np.ones((100, 1)) * 0.5, 24000)
    session = build_session(session_dir, {
        "channels": 1, "frames": 300,
        "tracks": [{"name": "s", "path": "s.wav", "start_frame": 10}]})
    graph = MixGraph(session, 48000, parse_policy("report"))
    result = render_offline(graph, parse_policy("report"))
    assert result.frames == 300
    # 100 source frames at 24 kHz -> 200 output frames starting at frame 10
    assert np.allclose(result.samples[10:210, 0], 0.5)
    assert np.all(result.samples[210:] == 0.0)


def test_loop_repeats_until_session_end(session_dir: Path):
    data = np.arange(100, dtype=np.float64)[:, None] / 100.0
    write_wav(session_dir / "s.wav", data, 48000)
    session = build_session(session_dir, {
        "channels": 1, "frames": 300,
        "tracks": [{"name": "s", "path": "s.wav",
                    "loop": {"start_frame": 10, "end_frame": 20}}]})
    result = render_offline(MixGraph(session, 48000, parse_policy("report")),
                            parse_policy("report"))
    values = result.samples[:, 0]
    assert np.allclose(values[10:20], data[10:20, 0])
    assert np.allclose(values[20:30], data[10:20, 0])  # loop restart
    assert np.allclose(values[290:300], data[10:20, 0])


def test_report_policy_counts_clipping(session_dir: Path):
    write_wav(session_dir / "s.wav", np.ones((10, 1)) * 1.5, 48000)
    session = build_session(session_dir, {
        "channels": 1, "tracks": [{"name": "s", "path": "s.wav"}]})
    result = render_offline(MixGraph(session, 48000, parse_policy("report")),
                            parse_policy("report"))
    assert result.stats.tracks[0].clip_count == 10
    assert result.stats.master.clip_count == 10
    assert result.stats.master.peak == pytest.approx(1.5)


def test_limit_policy_clamps_only_master(session_dir: Path):
    write_wav(session_dir / "s.wav", np.ones((10, 1)) * 1.5, 48000)
    session = build_session(session_dir, {
        "channels": 1, "tracks": [{"name": "s", "path": "s.wav"}]})
    result = render_offline(MixGraph(session, 48000, parse_policy("limit")),
                            parse_policy("limit"))
    assert result.stats.master.clip_count == 0
    assert result.stats.master.peak == pytest.approx(1.0)
    assert result.stats.tracks[0].clip_count == 10


def test_silent_track_reports_zero_rms(session_dir: Path):
    write_wav(session_dir / "s.wav", np.zeros((50, 1)), 48000)
    session = build_session(session_dir, {
        "channels": 1, "frames": 50, "tracks": [{"name": "s", "path": "s.wav"}]})
    result = render_offline(MixGraph(session, 48000, parse_policy("report")),
                            parse_policy("report"))
    assert result.stats.master.rms == 0.0
    assert result.stats.master.peak == 0.0
    assert result.stats.master.valid_frames == 50
