"""Core mix-graph, automation, resampling and manifest validation tests."""
from __future__ import annotations

import json
from pathlib import Path

import numpy as np
import pytest
import soundfile as sf

from audiomix.automation import AutomationCurve
from audiomix.mixer import MixGraph
from audiomix.model import (AutomationPoint, AutomationSpec, ManifestError,
                            parse_manifest)
from audiomix.render import BlockRenderer
from audiomix.resample import (LinearResampler, map_channels,
                               resampled_frame_count)


def _write_wav(path: Path, data: np.ndarray, rate: int) -> None:
    sf.write(str(path), data, rate, subtype="FLOAT")


def _manifest(path: Path, payload: dict) -> Path:
    mp = path / "session.json"
    mp.write_text(json.dumps(payload), encoding="utf-8")
    return mp


def test_manifest_rejects_negative_start(tmp_path):
    wav = tmp_path / "a.wav"
    _write_wav(wav, np.zeros((10, 1)), 48000)
    mp = _manifest(tmp_path, {"tracks": [
        {"name": "a", "file": "a.wav", "start": -0.1}]})
    with pytest.raises(ManifestError) as exc:
        parse_manifest(mp)
    assert exc.value.track == "a"
    assert exc.value.field_name == "start"


def test_manifest_rejects_invalid_gain_and_duplicate_name(tmp_path):
    wav = tmp_path / "a.wav"
    _write_wav(wav, np.zeros((10, 1)), 48000)
    mp = _manifest(tmp_path, {"tracks": [
        {"name": "a", "file": "a.wav", "start": 0, "gain": -1.0}]})
    with pytest.raises(ManifestError) as exc:
        parse_manifest(mp)
    assert exc.value.field_name == "gain"

    mp2 = _manifest(tmp_path, {"tracks": [
        {"name": "a", "file": "a.wav", "start": 0},
        {"name": "a", "file": "a.wav", "start": 0}]})
    with pytest.raises(ManifestError) as exc:
        parse_manifest(mp2)
    assert exc.value.track == "a" and exc.value.field_name == "name"


def test_manifest_rejects_non_increasing_automation(tmp_path):
    wav = tmp_path / "a.wav"
    _write_wav(wav, np.zeros((10, 1)), 48000)
    mp = _manifest(tmp_path, {"tracks": [{
        "name": "a", "file": "a.wav", "start": 0,
        "automation": {"mode": "linear", "points": [
            {"time": 1.0, "value": 1.0},
            {"time": 1.0, "value": 0.5}]}}]})
    with pytest.raises(ManifestError) as exc:
        parse_manifest(mp)
    assert exc.value.track == "a"
    assert "automation.points" in exc.value.field_name


def test_resampler_matches_count_and_same_rate_identity():
    x = np.arange(20, dtype=np.float64)[:, None]
    r = LinearResampler(48000, 24000)
    y = np.vstack([r.process(x[:7]), r.process(x[7:]), r.flush()])
    assert y.shape[0] == resampled_frame_count(20, 48000, 24000)
    rid = LinearResampler(48000, 48000)
    z = np.vstack([rid.process(x), rid.flush()])
    assert np.array_equal(z, x)


def test_channel_map_bounds_and_mono_widening():
    stereo = np.arange(20, dtype=np.float64).reshape(10, 2)
    mapped = map_channels(stereo, (1, 0), 2)
    assert np.array_equal(mapped[:, 0], stereo[:, 1])
    with pytest.raises(Exception):
        map_channels(stereo, (5, 0), 2)
    mono = np.ones((4, 1))
    wide = map_channels(mono, None, 2)
    assert wide.shape == (4, 2) and np.all(wide == 1.0)


def test_automation_linear_and_db_rules():
    lin = AutomationCurve(AutomationSpec("linear", (
        AutomationPoint(0.0, 0.0, "linear"),
        AutomationPoint(1.0, 2.0, "linear"))))
    assert lin.sample(-1.0) == 0.0          # left extension
    assert lin.sample(5.0) == 2.0           # right extension
    assert lin.sample(0.5) == 1.0

    db = AutomationCurve(AutomationSpec("db", (
        AutomationPoint(0.0, None, "db"),
        AutomationPoint(1.0, 0.0, "db"))))
    assert db.sample(0.0) == 0.0            # -inf dB -> zero gain
    assert db.sample(0.5) == 0.0            # mixed segment interior zero
    assert db.sample(1.0) == 1.0            # breakpoint exact


def test_mixer_node_order_mute_gain_automation(tmp_path):
    rate = 48000
    src = np.ones((rate, 2), dtype=np.float64) * 0.5
    _write_wav(tmp_path / "a.wav", src, rate)
    mp = _manifest(tmp_path, {"channels": 2, "tracks": [{
        "name": "a", "file": "a.wav", "start": 0, "gain": 0.5,
        "automation": {"mode": "linear", "points": [
            {"time": 0.0, "value": 0.0},
            {"time": 1.0, "value": 1.0}]}}]})
    tl = parse_manifest(mp)
    blocks = np.vstack([b for _, b, _ in BlockRenderer(tl, 512)])
    # gain 0.5 * source 0.5 * automation ramp from 0 -> 1 (frame grid).
    assert blocks[0, 0] == 0.0
    assert abs(blocks[-1, 0] - 0.25) < 1e-4
    # Monotonic increase along the ramp.
    mid = blocks[: rate // 2, 0]
    assert np.all(np.diff(mid) >= 0.0)

    muted_payload = json.loads(Path(mp).read_text())
    muted_payload["tracks"][0]["muted"] = True
    muted_payload["tracks"][0].pop("automation")
    mp2 = _manifest(tmp_path, muted_payload)
    blocks_m = np.vstack([b for _, b, _ in
                          BlockRenderer(parse_manifest(mp2), 512)])
    assert np.all(blocks_m == 0.0)


def test_bus_gain_and_summation(tmp_path):
    rate = 48000
    _write_wav(tmp_path / "a.wav", np.ones((100, 1)), rate)
    _write_wav(tmp_path / "b.wav", np.ones((100, 1)), rate)
    mp = _manifest(tmp_path, {"channels": 1, "bus_gain": 0.5, "tracks": [
        {"name": "a", "file": "a.wav", "start": 0},
        {"name": "b", "file": "b.wav", "start": 0}]})
    blocks = np.vstack([b for _, b, _ in
                        BlockRenderer(parse_manifest(mp), 37)])
    # (1 + 1) * 0.5 == 1.0
    assert np.allclose(blocks, 1.0)


def test_graph_is_shared_instance(tmp_path):
    rate = 48000
    _write_wav(tmp_path / "a.wav", np.zeros((10, 1)), rate)
    mp = _manifest(tmp_path, {"tracks": [
        {"name": "a", "file": "a.wav", "start": 0}]})
    tl = parse_manifest(mp)
    r1, r2 = BlockRenderer(tl, 1), BlockRenderer(tl, 1000)
    assert isinstance(r1.graph, MixGraph) and isinstance(r2.graph, MixGraph)
    assert type(r1.graph.process_track_block) is \
        type(r2.graph.process_track_block)
