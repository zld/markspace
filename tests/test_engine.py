"""Mix graph, offline/streaming equivalence and boundary behaviour."""

from __future__ import annotations

from pathlib import Path

import numpy as np
import pytest

from mixforge.engine import (
    compare,
    process,
    render_offline,
    render_streaming,
    validate_block_frames,
)
from mixforge.errors import ConsistencyError, ManifestError, RenderError
from mixforge.graph import MixGraph
from mixforge.manifest import load_manifest
from mixforge.metrics import StatsAccumulator, clip_positions

from conftest import tone, write_manifest, write_wav


def _session(tmp_path: Path, tracks: list[dict], **extra):
    return load_manifest(write_manifest(tmp_path / "session.json", tracks, **extra))


def test_graph_mixes_tracks_onto_buses(tmp_path: Path) -> None:
    write_wav(tmp_path / "a.wav", tone(4800, 48000, level=0.1), 48000)
    write_wav(tmp_path / "b.wav", tone(4800, 48000, level=0.1), 48000)
    session = _session(tmp_path, [
        {"id": "a", "path": "a.wav", "channels": "mono"},
        {"id": "b", "path": "b.wav", "channels": "mono", "gain_db": -6.0},
    ])
    graph = MixGraph(session, 48000)
    result = graph.render_block(0, 4800)
    assert result.master.shape == (4800, 2)
    # Mono sources are duplicated to both channels by the explicit matrix.
    assert np.allclose(result.master[:, 0], result.master[:, 1])
    assert set(result.tracks) == {"a", "b"}
    assert np.max(np.abs(result.master)) == pytest.approx(
        0.1 + 0.1 * 10 ** (-6 / 20), rel=1e-6)


def test_bus_gain_applies_after_track_sums(tmp_path: Path) -> None:
    write_wav(tmp_path / "a.wav", tone(480, 48000, level=0.2), 48000)
    session = _session(tmp_path, [
        {"id": "a", "path": "a.wav", "channels": "mono", "bus": "sfx"},
    ], buses={"sfx": {"gain_db": -6.0}})
    graph = MixGraph(session, 48000)
    result = graph.render_block(0, 480)
    assert np.max(np.abs(result.master)) == pytest.approx(0.2 * 10 ** (-6 / 20),
                                                          rel=1e-6)


def test_automation_is_position_based_not_block_based(tmp_path: Path) -> None:
    write_wav(tmp_path / "a.wav", np.ones((960, 1)) * 0.5, 48000)
    session = _session(tmp_path, [{
        "id": "a", "path": "a.wav", "channels": "mono",
        "automation": [{"ms": 0, "value": 0.0}, {"ms": 20, "value": 1.0}],
    }])
    graph = MixGraph(session, 48000)
    block = graph.render_block(480, 480).tracks["a"]
    # Frame 720 is 15 ms: gain 0.75, independent of where the block starts.
    assert block[240, 0] == pytest.approx(0.5 * 0.75, rel=1e-9)
    assert graph.render_block(0, 960).tracks["a"][720, 0] == pytest.approx(
        0.5 * 0.75, rel=1e-9)


@pytest.mark.parametrize("block", [1, 2, 3, 64, 257, 1000, 4800])
def test_offline_and_stream_are_identical(tmp_path: Path, block: int) -> None:
    rng = np.random.default_rng(3)
    write_wav(tmp_path / "a.wav", rng.standard_normal((4410, 2)) * 0.1, 44100)
    write_wav(tmp_path / "b.wav", tone(4800, 48000, 300.0, level=0.1), 48000)
    session = _session(tmp_path, [
        {"id": "a", "path": "a.wav", "offset_ms": 10,
         "automation": [{"ms": 0, "value": 0.2}, {"ms": 100, "value": 1.0}]},
        {"id": "b", "path": "b.wav", "channels": "mono"},
    ], duration_ms=200)
    graph = MixGraph(session, 48000)
    offline_master, offline_tracks = render_offline(graph)
    stream = render_streaming(graph, block)
    assert compare(offline_master, stream.master).max_error == 0.0
    for track_id, samples in offline_tracks.items():
        assert compare(samples, stream.tracks[track_id]).max_error == 0.0
    assert stream.block_lengths[-1] <= block
    assert sum(stream.block_lengths) == graph.total_frames


def test_last_block_may_be_short(tmp_path: Path) -> None:
    write_wav(tmp_path / "a.wav", tone(1000, 48000), 48000)
    session = _session(tmp_path, [{"id": "a", "path": "a.wav", "channels": "mono"}])
    graph = MixGraph(session, 48000)
    stream = render_streaming(graph, 300)
    assert stream.block_lengths == [300, 300, 300, 100]


def test_empty_audio_file_is_reported(tmp_path: Path) -> None:
    write_wav(tmp_path / "empty.wav", np.zeros((0, 1)), 48000)
    session = _session(tmp_path, [{"id": "empty", "path": "empty.wav"}])
    with pytest.raises(RenderError, match="no frames") as excinfo:
        MixGraph(session, 48000)
    assert excinfo.value.track_id == "empty"


def test_single_frame_audio_renders(tmp_path: Path) -> None:
    write_wav(tmp_path / "one.wav", np.array([[0.5]]), 48000)
    session = _session(tmp_path, [{"id": "one", "path": "one.wav"}],
                       duration_ms=10)
    graph = MixGraph(session, 48000)
    master = graph.render_block(0, graph.total_frames).master
    assert master.shape == (480, 2)
    assert np.max(np.abs(master)) == pytest.approx(0.5)


def test_duration_overflow_is_rejected(tmp_path: Path) -> None:
    write_wav(tmp_path / "a.wav", tone(4800, 48000), 48000)
    session = _session(tmp_path, [{"id": "a", "path": "a.wav", "channels": "mono"}],
                       duration_ms=50)
    with pytest.raises(ManifestError, match="beyond the target duration") as excinfo:
        MixGraph(session, 48000)
    assert excinfo.value.track_id == "a"


def test_source_channel_mismatch_is_reported(tmp_path: Path) -> None:
    write_wav(tmp_path / "a.wav", tone(480, 48000, channels=2), 48000)
    session = _session(tmp_path, [
        {"id": "a", "path": "a.wav", "channels": "mono"}])
    with pytest.raises(RenderError, match="1 channel") as excinfo:
        MixGraph(session, 48000)
    assert excinfo.value.track_id == "a"


def test_corrupt_media_reports_track(tmp_path: Path) -> None:
    (tmp_path / "bad.wav").write_bytes(b"definitely not audio")
    session = _session(tmp_path, [{"id": "bad", "path": "bad.wav"}])
    with pytest.raises(RenderError) as excinfo:
        MixGraph(session, 48000)
    assert excinfo.value.track_id == "bad"
    assert excinfo.value.exit_code == 3


def test_clip_count_and_positions(tmp_path: Path) -> None:
    loud = np.zeros((100, 1))
    loud[10:20] = 1.5
    loud[20:25] = -1.0
    acc = StatsAccumulator()
    acc.add(loud)
    assert acc.clip_count == 15
    positions = clip_positions(loud, limit=3)
    assert positions[0] == {"frame": 10, "channel": None or 0}


def test_compare_reports_first_difference() -> None:
    a = np.zeros((10, 2))
    b = np.zeros((10, 2))
    b[4, 1] = 1e-3
    result = compare(a, b)
    assert result.max_error == pytest.approx(1e-3)
    assert (result.first_diff_frame, result.first_diff_channel) == (4, 1)
    assert not result.consistent


def test_shape_mismatch_raises_consistency_error() -> None:
    with pytest.raises(ConsistencyError):
        compare(np.zeros((4, 2)), np.zeros((5, 2)))


@pytest.mark.parametrize("value", [0, -1, 1.5, True])
def test_invalid_block_frames(value) -> None:
    with pytest.raises(ManifestError):
        validate_block_frames(value)


def test_process_reports_comparison_and_stats(tmp_path: Path) -> None:
    write_wav(tmp_path / "a.wav", tone(4410, 44100, level=0.2), 44100)
    session = _session(tmp_path, [{"id": "a", "path": "a.wav", "channels": "mono"}],
                       duration_ms=100)
    result = process(session, 48000, 257)
    assert result["master"].shape == (4800, 2)
    assert result["comparison"].max_error < 1e-6
    assert 0 < result["stats"]["tracks"]["a"]["rms"] < 1.0
    assert result["stats"]["master"]["clip_count"] == 0
