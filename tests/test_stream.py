"""Streaming rendering: block invariance, padding, CLI, and failure paths."""

from __future__ import annotations

import json
from pathlib import Path

import numpy as np
import pytest
import soundfile as sf

from audiomix.cli import main
from audiomix.mixer import MixGraph, parse_policy
from audiomix.offline import render_offline
from audiomix.stream import iter_block_ranges, iter_stream_blocks, render_stream

from conftest import build_session, ramp, write_wav

TOLERANCE = 1e-6


def _mixed_session(session_dir: Path, frames: int = 4000) -> Path:
    """Stereo project mixing three rates, a loop, pan, and a dB curve."""
    write_wav(session_dir / "dialog.wav", ramp(frames, 1, scale=0.4), 44100)
    write_wav(session_dir / "music.wav", ramp(frames, 2, scale=0.2), 48000)
    write_wav(session_dir / "amb.wav", ramp(frames // 2, 1, scale=0.05), 22050)
    manifest = {
        "channels": 2,
        "sample_rate": 48000,
        "frames": 3000,
        "tracks": [
            {"name": "dialog", "path": "dialog.wav", "start_frame": 37,
             "gain_db": -3.0, "pan": -0.4,
             "automation": [{"frame": 0, "value": -24.0, "unit": "db"},
                            {"frame": 1500, "value": 3.0, "unit": "db"},
                            {"frame": 2800, "value": -12.0, "unit": "db"}]},
            {"name": "music", "path": "music.wav", "start_frame": 5,
             "gain_db": -6.0, "loop": {"start_frame": 200, "end_frame": 1000},
             "automation": [{"frame": 0, "value": 0.2},
                            {"frame": 900, "value": 1.0},
                            {"frame": 2800, "value": 0.5}]},
            {"name": "amb", "path": "amb.wav", "gain_db": -12.0},
        ],
    }
    return build_session(session_dir, manifest).source_path


@pytest.mark.parametrize("block_frames", [1, 7, 64, 257, 999, 3000, 5000])
def test_stream_matches_offline_sample_for_sample(session_dir: Path,
                                                  block_frames: int):
    manifest = _mixed_session(session_dir)
    session = build_session(session_dir, json.loads(manifest.read_text()))
    policy = parse_policy("report")
    graph = MixGraph(session, 48000, policy)
    offline = render_offline(graph, policy)
    streamed = render_stream(graph, block_frames)
    assert streamed.frames == offline.frames
    assert np.max(np.abs(offline.samples - streamed.samples)) <= TOLERANCE


@pytest.mark.parametrize("block_frames", [13, 64, 257, 1024])
def test_stream_padding_is_excluded_from_output_and_stats(
    session_dir: Path, block_frames: int
):
    manifest = _mixed_session(session_dir)
    session = build_session(session_dir, json.loads(manifest.read_text()))
    policy = parse_policy("report")
    graph = MixGraph(session, 48000, policy)
    streamed = render_stream(graph, block_frames)
    assert streamed.frames == 3000
    assert streamed.stats.master.valid_frames == 3000
    assert np.asarray(streamed.samples).shape == (3000, 2)
    for stats in streamed.stats.tracks:
        assert stats.valid_frames == 3000


def test_block_ranges_cover_session_with_full_render_blocks():
    ranges = iter_block_ranges(3000, 257)
    assert ranges[0] == (0, 257, 257)
    assert ranges[-1] == (2827, 3000, 3084)  # render past end, keep only 173
    assert sum(end - start for start, end, _ in ranges) == 3000


def test_block_ranges_zero_total_is_empty():
    assert iter_block_ranges(0, 64) == []


def test_loop_and_curve_state_continue_across_boundaries(session_dir: Path):
    data = np.arange(200, dtype=np.float64)[:, None] / 200.0
    write_wav(session_dir / "s.wav", data, 48000)
    session = build_session(session_dir, {
        "channels": 1, "frames": 500,
        "tracks": [{"name": "s", "path": "s.wav",
                    "loop": {"start_frame": 0, "end_frame": 37},
                    "automation": [{"frame": 0, "value": 0.0},
                                   {"frame": 500, "value": 1.0}]}]})
    graph = MixGraph(session, 48000, parse_policy("report"))
    reference = data[np.arange(500) % 37, 0]
    # replicate the graph's curve sampling exactly via the public curve
    curve = session.tracks[0].automation
    expected = reference * curve.gain_at(np.arange(500))
    blocks = np.concatenate(list(iter_stream_blocks(graph, 31)))
    assert np.allclose(blocks[:, 0], expected, atol=1e-12)


def test_stream_clip_limit_matches_offline(session_dir: Path):
    write_wav(session_dir / "hot.wav", np.full((300, 1), 2.5), 48000)
    session = build_session(session_dir, {
        "channels": 1, "tracks": [{"name": "hot", "path": "hot.wav"}]})
    policy = parse_policy("limit")
    graph = MixGraph(session, 48000, policy)
    offline = render_offline(graph, policy)
    streamed = render_stream(graph, 47)
    assert np.array_equal(offline.samples, streamed.samples)
    assert streamed.stats.master.clip_count == 0
    assert streamed.stats.tracks[0].clip_count == 300


# -- CLI behaviour ------------------------------------------------------------

def test_cli_render_success_writes_wav_and_report(session_dir: Path, capsys):
    manifest = _mixed_session(session_dir)
    wav = session_dir / "mix.wav"
    report = session_dir / "report.json"
    code = main(["render", "--manifest", str(manifest), "--output", str(wav),
                 "--report", str(report), "--rate", "48000",
                 "--block-frames", "257", "--mode", "stream"])
    assert code == 0
    assert wav.is_file() and report.is_file()
    data = json.loads(report.read_text())
    assert set(data["tracks"]) == {"dialog", "music", "amb"}
    for entry in data["tracks"].values():
        assert {"peak", "rms", "valid_frames", "clip_count"} <= set(entry)
    assert data["output_frames"] == 3000
    automation = data["automation"]["dialog"]
    frames = [p["frame"] for p in automation]
    assert frames == sorted(frames)
    # start, every in-range keyframe, and the final frame are all present
    assert {0, 1500, 2800, 2999} <= set(frames)
    out = capsys.readouterr().out
    assert "tracks: 3" in out and "output frames: 3000" in out
    stdout_wav, _ = sf.read(str(wav), always_2d=True)
    assert stdout_wav.shape == (3000, 2)


def test_cli_render_offline_and_stream_reports_agree(session_dir: Path):
    manifest = _mixed_session(session_dir)
    paths = {}
    for mode in ("offline", "stream"):
        wav = session_dir / f"{mode}.wav"
        report = session_dir / f"{mode}.json"
        args = ["render", "--manifest", str(manifest), "--output", str(wav),
                "--report", str(report), "--rate", "48000", "--mode", mode, "--quiet"]
        if mode == "stream":
            args += ["--block-frames", "257"]
        assert main(args) == 0
        paths[mode] = (wav, report)
    first, _ = sf.read(str(paths["offline"][0]), always_2d=True)
    second, _ = sf.read(str(paths["stream"][0]), always_2d=True)
    assert np.max(np.abs(first - second)) <= TOLERANCE
    a = json.loads(paths["offline"][1].read_text())
    b = json.loads(paths["stream"][1].read_text())
    assert a["tracks"].keys() == b["tracks"].keys()
    for name in a["tracks"]:
        for key in ("peak", "rms", "valid_frames", "clip_count"):
            assert a["tracks"][name][key] == pytest.approx(
                b["tracks"][name][key], abs=TOLERANCE
            )


def test_cli_missing_manifest_exits_2(session_dir: Path, capsys):
    code = main(["render", "--manifest", str(session_dir / "absent.json"),
                 "--output", str(session_dir / "x.wav")])
    assert code == 2
    assert "manifest not found" in capsys.readouterr().err


def test_cli_missing_track_does_not_leave_report(session_dir: Path, capsys):
    manifest = session_dir / "session.json"
    manifest.write_text(json.dumps({"channels": 1, "tracks": [
        {"name": "ghost", "path": "ghost.wav"}]}))
    report = session_dir / "report.json"
    wav = session_dir / "mix.wav"
    code = main(["render", "--manifest", str(manifest), "--output", str(wav),
                 "--report", str(report)])
    assert code == 2
    assert not report.exists() and not wav.exists()
    err = capsys.readouterr().err
    assert "ghost" in err and "not found" in err


def test_cli_corrupt_audio_cleans_temps(session_dir: Path, capsys):
    (session_dir / "bad.wav").write_bytes(b"RIFF")
    manifest = session_dir / "session.json"
    manifest.write_text(json.dumps({"channels": 1, "tracks": [
        {"name": "bad", "path": "bad.wav"}]}))
    report = session_dir / "report.json"
    code = main(["render", "--manifest", str(manifest), "--report", str(report)])
    assert code == 2
    assert not report.exists()
    assert list(session_dir.glob(".*.tmp")) == []
    assert "bad" in capsys.readouterr().err


def test_cli_refuses_to_overwrite_input(session_dir: Path):
    write_wav(session_dir / "s.wav", ramp(100, 1), 48000)
    manifest = session_dir / "session.json"
    manifest.write_text(json.dumps({"channels": 1, "tracks": [
        {"name": "s", "path": "s.wav"}]}))
    before = (session_dir / "s.wav").read_bytes()
    code = main(["render", "--manifest", str(manifest),
                 "--output", str(session_dir / "s.wav")])
    assert code == 2
    assert (session_dir / "s.wav").read_bytes() == before


def test_cli_internal_error_exits_3(session_dir: Path, monkeypatch, capsys):
    manifest = _mixed_session(session_dir)
    import audiomix.cli as cli

    def boom(*args, **kwargs):
        raise RuntimeError("unexpected")

    monkeypatch.setattr(cli, "MixGraph", boom)
    code = main(["render", "--manifest", str(manifest),
                 "--output", str(session_dir / "mix.wav")])
    assert code == 3
    assert "internal error" in capsys.readouterr().err


def test_cli_generate_sample_is_deterministic(tmp_path: Path):
    first, second = tmp_path / "a", tmp_path / "b"
    assert main(["generate-sample", "--dir", str(first)]) == 0
    assert main(["generate-sample", "--dir", str(second)]) == 0
    for name in ("dialog.wav", "music.wav", "ambience.wav", "session.json"):
        assert (first / name).read_bytes() == (second / name).read_bytes()
    assert main(["render", "--manifest", str(first / "session.json"),
                 "--output", str(first / "mix.wav"),
                 "--rate", "48000", "--block-frames", "257",
                 "--mode", "stream", "--quiet"]) == 0
