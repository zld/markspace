"""CLI contract: exit codes, summary line, report and failure isolation."""

from __future__ import annotations

import json
from pathlib import Path

import numpy as np
import pytest
import soundfile as sf

from mixforge.cli import main

from conftest import tone, write_manifest, write_wav


def _make(tmp_path: Path) -> Path:
    audio = tmp_path / "audio"
    write_wav(audio / "voice.wav", tone(44100, 44100, 300.0, level=0.2), 44100)
    write_wav(audio / "bed.wav", tone(24000, 48000, 500.0, channels=2,
                                     level=0.15), 48000)
    return write_manifest(audio / "session.json", [
        {"id": "voice", "path": "voice.wav", "channels": "mono",
         "automation": [{"ms": 0, "value": -6, "unit": "db"},
                        {"ms": 500, "value": 0, "unit": "db"}]},
        {"id": "bed", "path": "bed.wav", "offset_ms": 100, "gain_db": -3.0,
         "bus": "music"},
    ], buses={"music": {"gain_db": -2.0}})


def _run(capsys, *args) -> tuple[int, str, str]:
    code = main(list(args))
    out, err = capsys.readouterr()
    return code, out, err


def test_render_success(tmp_path: Path, capsys) -> None:
    manifest = _make(tmp_path)
    output = tmp_path / "mix.wav"
    report = tmp_path / "report.json"
    code, out, err = _run(capsys, "render", "--manifest", str(manifest),
                          "--rate", "48000", "--block-frames", "257",
                          "--output", str(output), "--report", str(report))
    assert code == 0
    assert err == ""
    assert "tracks=2 rate=48000 frames=" in out
    for line in out.splitlines():
        if line.startswith("track="):
            fields = dict(part.split("=", 1) for part in line.split())
            assert 0.01 <= float(fields["peak"]) <= 1.0
            assert 0.01 <= float(fields["rms"]) <= 1.0
            assert int(fields["automation_samples"]) >= 4
            assert int(fields["clip_count"]) >= 0
    assert "offline_stream_max_error=" in out
    info = sf.info(str(output))
    assert info.samplerate == 48000
    assert info.subtype == "FLOAT"
    payload = json.loads(report.read_text())
    assert payload["offline_stream_max_error"] < 1e-6
    assert len(payload["tracks"]) == 2
    assert payload["last_block_frames"] <= 257


def test_offline_only_has_no_blocks(tmp_path: Path, capsys) -> None:
    manifest = _make(tmp_path)
    code, out, _err = _run(capsys, "render", "--manifest", str(manifest),
                           "--output", str(tmp_path / "mix.wav"))
    assert code == 0
    assert "offline_stream_max_error=0.000e+00" in out
    assert "blocks=" not in out


def test_missing_manifest_exit_2(tmp_path: Path, capsys) -> None:
    code, _out, err = _run(capsys, "render", "--manifest",
                           str(tmp_path / "nope.json"))
    assert code == 2
    assert "cannot read manifest" in err


def test_bad_block_frames_exit_2(tmp_path: Path, capsys) -> None:
    manifest = _make(tmp_path)
    code, _out, err = _run(capsys, "render", "--manifest", str(manifest),
                           "--block-frames", "0")
    assert code == 2
    assert "block-frames" in err


def test_bad_rate_exit_2(tmp_path: Path, capsys) -> None:
    manifest = _make(tmp_path)
    code, _out, err = _run(capsys, "render", "--manifest", str(manifest),
                           "--rate", "-1")
    assert code == 2
    assert "--rate" in err


def test_corrupt_media_exit_3_and_no_output(tmp_path: Path, capsys) -> None:
    manifest = _make(tmp_path)
    (tmp_path / "audio" / "bed.wav").write_bytes(b"broken")
    output = tmp_path / "mix.wav"
    report = tmp_path / "report.json"
    code, out, err = _run(capsys, "render", "--manifest", str(manifest),
                          "--output", str(output), "--report", str(report))
    assert code == 3
    assert "[bed" in err
    assert not output.exists()
    assert out == ""
    payload = json.loads(report.read_text())
    assert payload["status"] == "error"
    assert payload["error"]["track_id"] == "bed"


def test_refuses_to_overwrite_input(tmp_path: Path, capsys) -> None:
    manifest = _make(tmp_path)
    source = tmp_path / "audio" / "voice.wav"
    before = source.read_bytes()
    code, _out, err = _run(capsys, "render", "--manifest", str(manifest),
                           "--output", str(source))
    assert code == 2
    assert "overwrite" in err
    assert source.read_bytes() == before


def test_unwritable_output_exit_3(tmp_path: Path, capsys) -> None:
    manifest = _make(tmp_path)
    blocker = tmp_path / "not_a_dir"
    blocker.write_text("x")
    code, _out, err = _run(capsys, "render", "--manifest", str(manifest),
                           "--output", str(blocker / "mix.wav"))
    assert code == 3
    assert "output" in err


def test_no_command_is_usage_error(capsys) -> None:
    code, _out, err = _run(capsys, "render")
    assert code == 2
    assert "manifest" in err


def test_consistency_failure_exit_4(tmp_path: Path, capsys, monkeypatch) -> None:
    from mixforge import engine

    manifest = _make(tmp_path)
    original = engine.compare
    monkeypatch.setattr(engine, "compare", lambda a, b: original(
        a, b + np.full_like(b, 1e-3)))
    code, out, err = _run(capsys, "render", "--manifest", str(manifest),
                          "--block-frames", "128")
    assert code == 4
    assert out == ""
    assert "first_diff" in err or "differ by" in err
