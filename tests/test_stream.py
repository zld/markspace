"""Streaming renderer: chunk determinism, boundary and memory behavior."""
from __future__ import annotations

import json
from pathlib import Path

import numpy as np
import soundfile as sf

from audiomix.cli import main as cli_main
from audiomix.model import parse_manifest
from audiomix.render import BlockRenderer, render_to_file


def _session(tmp_path: Path, tracks: list[dict], *, channels: int = 2,
             rate: int = 48000) -> Path:
    mp = tmp_path / "session.json"
    mp.write_text(json.dumps(
        {"output_rate": rate, "channels": channels, "tracks": tracks}),
        encoding="utf-8")
    return mp


def _render(timeline, block_frames: int) -> np.ndarray:
    return np.vstack([block for _, block, _ in
                      BlockRenderer(timeline, block_frames)])


def test_streaming_matches_offline_sample_exact(tmp_path):
    rng = np.random.default_rng(7)
    sf.write(str(tmp_path / "a.wav"),
             rng.standard_normal((4000, 2)), 44100, subtype="FLOAT")
    sf.write(str(tmp_path / "b.wav"),
             rng.standard_normal((3000, 1)) * 0.5, 48000, subtype="FLOAT")
    mp = _session(tmp_path, [
        {"name": "a", "file": "a.wav", "start": 0.0},
        {"name": "b", "file": "b.wav", "start": 0.013, "gain": 0.7},
    ])
    tl = parse_manifest(mp)
    reference = _render(tl, 10 ** 9)
    for block_frames in (1, 2, 3, 7, 257, 1000, 4096):
        got = _render(tl, block_frames)
        assert got.shape == reference.shape
        assert np.array_equal(got, reference), block_frames


def test_last_block_shorter_than_block_size(tmp_path):
    sf.write(str(tmp_path / "a.wav"), np.ones((100, 2)) * 0.1, 48000,
             subtype="FLOAT")
    mp = _session(tmp_path, [
        {"name": "a", "file": "a.wav", "start": 0}])
    tl = parse_manifest(mp)
    renderer = BlockRenderer(tl, 37)
    sizes = [block.shape[0] for _, block, _ in renderer]
    assert sizes[:-1] == [37, 37] and sizes[-1] == 26
    assert sum(sizes) == 100


def test_track_starts_and_ends_mid_block(tmp_path):
    # 100-frame source starting at frame 50 with block size 37.
    sf.write(str(tmp_path / "a.wav"), np.ones((100, 1)), 48000,
             subtype="FLOAT")
    mp = _session(tmp_path, [
        {"name": "a", "file": "a.wav", "start": 50 / 48000}], channels=1)
    tl = parse_manifest(mp)
    y = _render(tl, 37)
    assert y.shape[0] == 150
    assert np.all(y[:50] == 0.0)
    assert np.all(y[50:] == 1.0)
    # Same result with block size 1 exercises every boundary transition.
    y1 = _render(tl, 1)
    assert np.array_equal(y1, y)


def test_resampler_state_survives_arbitrary_chunking():
    from audiomix.resample import LinearResampler
    rng = np.random.default_rng(11)
    x = rng.standard_normal((5000, 2))
    whole = LinearResampler(44100, 48000)
    expected = np.vstack([whole.process(x), whole.flush()])
    chunked = LinearResampler(44100, 48000)
    parts = []
    for i in range(0, len(x), 13):
        parts.append(chunked.process(x[i:i + 13]))
    parts.append(chunked.flush())
    got = np.vstack(parts)
    assert np.array_equal(got, expected)


def test_memory_does_not_grow_with_timeline_length(tmp_path):
    # A long source rendered in small blocks must keep a bounded queue.
    frames = 48000 * 60
    sf.write(str(tmp_path / "long.wav"),
             np.ones((frames, 1), dtype=np.float32), 48000, subtype="FLOAT")
    mp = _session(tmp_path, [
        {"name": "long", "file": "long.wav", "start": 0}], channels=1)
    tl = parse_manifest(mp)
    renderer = BlockRenderer(tl, 257)
    player = renderer.players[0]
    max_queue = 0
    produced = 0
    for _start, block, _tb in renderer:
        produced += block.shape[0]
        max_queue = max(max_queue, player._queue.shape[0])
    assert produced == frames
    # Read chunk is 8192; a healthy pipeline never buffers more than that
    # plus one block of slack regardless of the 60 s timeline length.
    assert max_queue <= 8192 + 257


def test_cli_stream_and_offline_agree(tmp_path):
    sf.write(str(tmp_path / "a.wav"),
             np.random.default_rng(3).standard_normal((2000, 2)),
             44100, subtype="FLOAT")
    mp = _session(tmp_path, [
        {"name": "a", "file": "a.wav", "start": 0}])
    stream_out = tmp_path / "stream.wav"
    offline_out = tmp_path / "offline.wav"
    rc1 = cli_main(["render", "--manifest", str(mp),
                    "--out", str(stream_out), "--block-frames", "257"])
    rc2 = cli_main(["render", "--manifest", str(mp),
                    "--out", str(offline_out), "--mode", "offline"])
    assert rc1 == 0 and rc2 == 0
    a, _ = sf.read(str(stream_out), always_2d=True)
    b, _ = sf.read(str(offline_out), always_2d=True)
    assert a.shape == b.shape
    assert np.max(np.abs(a.astype(np.float64) - b.astype(np.float64))) <= 1e-6


def test_render_to_file_returns_frame_count(tmp_path):
    sf.write(str(tmp_path / "a.wav"), np.zeros((123, 2)), 48000,
             subtype="FLOAT")
    mp = _session(tmp_path, [
        {"name": "a", "file": "a.wav", "start": 0}])
    out = tmp_path / "out.wav"
    total = render_to_file(parse_manifest(mp), out, 64)
    assert total == 123
    assert sf.info(str(out)).frames == 123
