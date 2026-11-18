"""Batch tests: single-file/batch consistency, failures, idempotency, exits."""
import json
import sqlite3

import pytest

from audioslice import audio, cli
from audioslice.batch import BatchConfig, InputError, process_file, run_batch
from audioslice.detector import DetectorParams
from audioslice.segments import SegmentParams

CONFIG = BatchConfig(detector=DetectorParams(), segments=SegmentParams())


def make_files(root):
    root.mkdir(parents=True, exist_ok=True)
    audio.synth_file(root / "01_take.wav", 16_000, 3.0, [
        audio.SynthEvent(0.4, 0.6, fade_in=0.2, fade_out=0.2),
        audio.SynthEvent(1.8, 0.5, freq=330.0),
    ], seed=11)
    audio.synth_file(root / "02_take.wav", 44_100, 2.0, [
        audio.SynthEvent(0.3, 0.8, fade_in=0.3, fade_out=0.3),
    ], channels=2, seed=22)


def bounds(result):
    return [(s["start_sample"], s["end_sample"]) for s in result["segments"]]


def test_batch_matches_single_file(tmp_path):
    src = tmp_path / "in"
    make_files(src)
    summary = run_batch(src, tmp_path / "r.sqlite", CONFIG)
    assert summary["files"] == 2
    assert summary["failed"] == 0
    assert summary["segments"] == 3
    for res in summary["results"]:
        single = process_file(res["path"], CONFIG)
        assert bounds(single) == bounds(res)
        assert single["silence_total_seconds"] == pytest.approx(
            res["silence_total_seconds"])


def test_failed_file_recorded_and_batch_continues(tmp_path):
    src = tmp_path / "in"
    make_files(src)
    (src / "03_broken.wav").write_bytes(b"not really a wave file")
    summary = run_batch(src, tmp_path / "r.sqlite", CONFIG)
    assert summary["files"] == 3
    assert summary["succeeded"] == 2
    assert summary["failed"] == 1
    assert "03_broken.wav" in summary["errors"][0]["path"]
    with sqlite3.connect(tmp_path / "r.sqlite") as conn:
        rows = conn.execute(
            "SELECT path, status, error FROM files ORDER BY path").fetchall()
    assert [r[1] for r in rows] == ["ok", "ok", "error"]
    assert rows[2][2]  # error message preserved


def test_rerun_is_idempotent(tmp_path):
    src = tmp_path / "in"
    make_files(src)
    db = tmp_path / "r.sqlite"
    first = run_batch(src, db, CONFIG)
    second = run_batch(src, db, CONFIG)
    assert first["segments"] == second["segments"]
    with sqlite3.connect(db) as conn:
        assert conn.execute("SELECT COUNT(*) FROM jobs").fetchone()[0] == 1
        assert conn.execute("SELECT COUNT(*) FROM files").fetchone()[0] == 2
        assert conn.execute("SELECT COUNT(*) FROM segments").fetchone()[0] == 3


def test_paths_must_stay_inside_input_root(tmp_path):
    src = tmp_path / "in"
    make_files(src)
    outside = tmp_path / "outside.wav"
    audio.synth_file(outside, 16_000, 1.0, [audio.SynthEvent(0.2, 0.3)])
    (src / "00_link.wav").symlink_to(outside)
    with pytest.raises(InputError, match="outside the input root"):
        run_batch(src, tmp_path / "r.sqlite", CONFIG)


def test_missing_directory_is_input_error(tmp_path):
    with pytest.raises(InputError):
        run_batch(tmp_path / "nope", tmp_path / "r.sqlite", CONFIG)


def test_cli_exit_codes(tmp_path, capsys):
    src = tmp_path / "in"
    make_files(src)
    db = tmp_path / "r.sqlite"
    assert cli.main(["batch", "--input", str(src), "--db", str(db)]) == 0
    out = capsys.readouterr().out
    assert "files=2" in out and "segments=3" in out
    assert "silence_total_seconds=" in out

    assert cli.main(["batch", "--input", str(tmp_path / "nope"),
                     "--db", str(db)]) == 3
    assert cli.main(["file", "--input", str(tmp_path / "nope.wav")]) == 3

    (src / "09_bad.wav").write_bytes(b"junk")
    assert cli.main(["batch", "--input", str(src), "--db", str(db)]) == 4

    with pytest.raises(SystemExit) as excinfo:  # invalid parameter
        cli.main(["batch", "--input", str(src), "--db", str(db),
                  "--onset-factor", "1.0"])
    assert excinfo.value.code == 2


def test_cli_json_shape(tmp_path, capsys):
    src = tmp_path / "in"
    make_files(src)
    assert cli.main(["batch", "--input", str(src),
                     "--db", str(tmp_path / "r.sqlite"), "--json"]) == 0
    payload = json.loads(capsys.readouterr().out)
    assert payload["files"] == 2 and payload["segments"] == 3
    seg = payload["results"][0]["segments"][0]
    assert isinstance(seg["start_sample"], int) and seg["start_sample"] >= 0
    assert isinstance(seg["end_sample"], int)
    assert isinstance(payload["results"][0]["silence_total_seconds"], float)
