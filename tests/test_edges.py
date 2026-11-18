"""Edge cases: non-finite data, corrupt inputs, clipping, empty sources."""
from __future__ import annotations

import json
from pathlib import Path

import numpy as np
import pytest
import soundfile as sf

from audiomix.cli import main as cli_main
from audiomix.model import RenderError, parse_manifest
from audiomix.render import BlockRenderer, render_to_file
from audiomix.report import StatsAccumulator


def _session(tmp_path: Path, tracks: list[dict], **kw) -> Path:
    payload = {"channels": kw.get("channels", 2), "tracks": tracks}
    payload.update({k: v for k, v in kw.items() if k != "channels"})
    mp = tmp_path / "session.json"
    mp.write_text(json.dumps(payload), encoding="utf-8")
    return mp


def test_nan_is_located_to_track_and_aborts(tmp_path):
    good = np.zeros((128, 2), dtype=np.float64)
    bad = np.ones((128, 2), dtype=np.float64) * 0.2
    bad[64, 0] = np.nan
    sf.write(str(tmp_path / "good.wav"), good, 48000, subtype="FLOAT")
    sf.write(str(tmp_path / "bad.wav"), bad, 48000, subtype="FLOAT")
    mp = _session(tmp_path, [
        {"name": "good", "file": "good.wav", "start": 0},
        {"name": "bad", "file": "bad.wav", "start": 0}])
    tl = parse_manifest(mp)
    with pytest.raises(RenderError) as exc:
        np.vstack([b for _, b, _ in BlockRenderer(tl, 16)])
    assert exc.value.track == "bad"
    assert "NaN" in str(exc.value)


def test_inf_aborts_and_nothing_is_written(tmp_path):
    x = np.ones((64, 1), dtype=np.float64)
    x[10, 0] = np.inf
    sf.write(str(tmp_path / "inf.wav"), x, 48000, subtype="FLOAT")
    mp = _session(tmp_path, [
        {"name": "inf", "file": "inf.wav", "start": 0}], channels=1)
    out = tmp_path / "out.wav"
    with pytest.raises(RenderError):
        render_to_file(parse_manifest(mp), out, 8)
    assert not out.exists()
    assert not (tmp_path / "out.wav.part").exists()


def test_corrupt_input_returns_exit_code_3(tmp_path):
    (tmp_path / "broken.wav").write_bytes(b"RIFFnotreallyawav" * 8)
    mp = _session(tmp_path, [
        {"name": "x", "file": "broken.wav", "start": 0}])
    rc = cli_main(["render", "--manifest", str(mp),
                   "--out", str(tmp_path / "o.wav")])
    assert rc == 3


def test_unwritable_output_returns_3_and_cleans_part(tmp_path):
    sf.write(str(tmp_path / "a.wav"), np.zeros((16, 2)), 48000,
             subtype="FLOAT")
    mp = _session(tmp_path, [
        {"name": "a", "file": "a.wav", "start": 0}])
    blocked = tmp_path / "nodir"
    blocked.mkdir()
    blocked.chmod(0o555)  # read-only directory
    rc = cli_main(["render", "--manifest", str(mp),
                   "--out", str(blocked / "o.wav")])
    assert rc == 3
    assert not (blocked / "o.wav").exists()
    blocked.chmod(0o755)


def test_manifest_error_returns_exit_code_2(tmp_path):
    sf.write(str(tmp_path / "a.wav"), np.zeros((8, 1)), 48000,
             subtype="FLOAT")
    mp = _session(tmp_path, [
        {"name": "a", "file": "a.wav", "start": -1.0}])
    rc = cli_main(["render", "--manifest", str(mp),
                   "--out", str(tmp_path / "o.wav")])
    assert rc == 2


def test_empty_and_single_frame_sources(tmp_path):
    sf.write(str(tmp_path / "empty.wav"), np.zeros((0, 2)), 48000,
             subtype="FLOAT")
    sf.write(str(tmp_path / "one.wav"), np.ones((1, 2)) * 0.25, 48000,
             subtype="FLOAT")
    mp = _session(tmp_path, [
        {"name": "empty", "file": "empty.wav", "start": 0},
        {"name": "one", "file": "one.wav", "start": 0.1}])
    tl = parse_manifest(mp)
    y_block = np.vstack([b for _, b, _ in BlockRenderer(tl, 5)])
    y_one = np.vstack([b for _, b, _ in BlockRenderer(tl, 1)])
    assert np.array_equal(y_block, y_one)
    # Single frame at 4800 Hz; everything before it is silence.
    assert np.all(y_block[:4800] == 0.0)
    assert np.allclose(y_block[4800], 0.25)


def test_non_integer_frame_duration_does_not_overflow(tmp_path):
    # 32 kHz -> 48 kHz gives a 1.5 ratio with fractional source positions.
    x = np.arange(320, dtype=np.float64)[:, None] * 0.001
    sf.write(str(tmp_path / "frac.wav"), x, 32000, subtype="FLOAT")
    mp = _session(tmp_path, [
        {"name": "frac", "file": "frac.wav", "start": 0}], channels=1)
    tl = parse_manifest(mp)
    y = np.vstack([b for _, b, _ in BlockRenderer(tl, 7)])
    assert np.isfinite(y).all()
    assert y.shape[0] == 480  # ceil(320 * 48000 / 32000)


def test_clipping_count_and_limiter(tmp_path):
    x = np.array([[0.5], [1.5], [-2.0], [0.9]], dtype=np.float64)
    sf.write(str(tmp_path / "hot.wav"), x, 48000, subtype="FLOAT")
    mp = _session(tmp_path, [
        {"name": "hot", "file": "hot.wav", "start": 0}], channels=1)
    tl = parse_manifest(mp)
    raw = np.vstack([b for _, b, _ in BlockRenderer(tl, 2)])
    assert int(np.count_nonzero(np.abs(raw) > 1.0)) == 2

    limited = tmp_path / "lim.wav"
    total = render_to_file(tl, limited, 2, limit=True)
    data, _ = sf.read(str(limited), always_2d=True)
    assert total == 4 and np.max(np.abs(data)) <= 1.0
    assert np.allclose(data.ravel(), [0.5, 1.0, -1.0, 0.9])


def test_stats_zero_when_no_active_frames():
    acc = StatsAccumulator("silent")
    acc.add(np.zeros((10, 2)))
    stats = acc.as_dict()
    assert stats["peak"] == 0.0 and stats["rms"] == 0.0
    assert stats["active_frames"] == 0
    assert stats["clipped_samples"] == 0
    assert stats["no_active"] is True


def test_stats_reject_non_finite_input():
    acc = StatsAccumulator("x")
    with pytest.raises(RenderError):
        acc.add(np.array([[np.nan, 1.0]]))


def test_missing_manifest_file_is_render_error(tmp_path):
    rc = cli_main(["render",
                   "--manifest", str(tmp_path / "nope.json"),
                   "--out", str(tmp_path / "o.wav")])
    assert rc == 3
