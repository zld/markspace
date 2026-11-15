"""End-to-end pipeline tests: detection, DB rerun, export, corrupt inputs."""
from __future__ import annotations

import json
import os
import sqlite3
import sys

import pytest

sys.path.insert(0, os.path.dirname(__file__))  # for make_fixtures

import make_fixtures  # noqa: E402
from soundindex import cli  # noqa: E402

SCAN_ARGS = ["--frame-ms", "20", "--hop-ms", "10", "--noise-margin-db", "8",
             "--min-silence-ms", "120", "--min-clip-ms", "80",
             "--fade-guard-ms", "30"]


@pytest.fixture(scope="session")
def fixture_dir(tmp_path_factory):
    out = tmp_path_factory.mktemp("fixtures")
    make_fixtures.build_fixtures(str(out))
    make_fixtures.build_bad_fixtures(str(out), str(out / "voice_with_noise.wav"))
    return str(out)


@pytest.fixture()
def db(tmp_path):
    return str(tmp_path / "index.sqlite")


def scan(fixture_dir, db, *extra):
    return cli.main(["scan", "--input", fixture_dir, "--db", db,
                     *SCAN_ARGS, *extra])


def test_scan_fixtures_ok(fixture_dir, db, capsys):
    rc = scan(fixture_dir, db, "--export", db + ".jsonl")
    out = capsys.readouterr().out
    assert rc == 0
    assert "files_ok=4" in out
    assert "files_failed=0" in out
    summary = dict(tok.split("=") for tok in out.splitlines()[-1].split())
    assert 3 <= int(summary["clips_total"]) <= 6
    assert float(summary["silence_total_ms"]) >= 0.0


def test_silence_file_has_no_clips(fixture_dir, db):
    scan(fixture_dir, db)
    con = sqlite3.connect(db)
    row = con.execute(
        "SELECT id, n_samples, sample_rate, silence_total_ms FROM files"
        " WHERE path LIKE '%silence.wav'").fetchone()
    clips = con.execute("SELECT COUNT(*) FROM clips WHERE file_id=?",
                        (row[0],)).fetchone()[0]
    assert clips == 0
    duration_ms = row[1] * 1000.0 / row[2]
    assert abs(row[3] - duration_ms) < 1.0


def test_clip_intervals_valid(fixture_dir, db):
    scan(fixture_dir, db)
    con = sqlite3.connect(db)
    for (fid, n) in con.execute("SELECT id, n_samples FROM files WHERE status='ok'"):
        clips = con.execute(
            "SELECT start_sample, end_sample FROM clips"
            " WHERE file_id=? ORDER BY idx", (fid,)).fetchall()
        prev_end = 0
        for s, e in clips:
            assert isinstance(s, int) and isinstance(e, int)
            assert 0 <= s < e <= n
            assert s >= prev_end  # no overlap
            prev_end = e
        sil = con.execute(
            "SELECT start_sample, end_sample FROM silences"
            " WHERE file_id=? ORDER BY idx", (fid,)).fetchall()
        covered = sorted(list(clips) + list(sil))
        assert covered[0][0] == 0 and covered[-1][1] == n  # full coverage


def test_rerun_reuses_results(fixture_dir, db, capsys):
    assert scan(fixture_dir, db) == 0
    capsys.readouterr()
    assert scan(fixture_dir, db) == 0
    out = capsys.readouterr().out
    assert "reused=4" in out
    con = sqlite3.connect(db)
    assert con.execute("SELECT COUNT(*) FROM files").fetchone()[0] == 4


def test_force_rerun_replaces_without_duplicates(fixture_dir, db):
    scan(fixture_dir, db)
    con = sqlite3.connect(db)
    clips_before = con.execute("SELECT COUNT(*) FROM clips").fetchone()[0]
    assert scan(fixture_dir, db, "--force") == 0
    con = sqlite3.connect(db)
    assert con.execute("SELECT COUNT(*) FROM files").fetchone()[0] == 4
    assert con.execute("SELECT COUNT(*) FROM clips").fetchone()[0] == clips_before


def test_export_sorted_with_metadata(fixture_dir, db):
    out_jsonl = db + ".jsonl"
    assert scan(fixture_dir, db, "--export", out_jsonl) == 0
    lines = [json.loads(l) for l in open(out_jsonl, encoding="utf-8")]
    paths = [l["path"] for l in lines]
    assert paths == sorted(paths)  # stable grouping by path
    by_type = {}
    for l in lines:
        if "index" in l:
            by_type.setdefault((l["path"], l["type"]), []).append(l["index"])
    for idxs in by_type.values():
        assert idxs == sorted(idxs)  # indices ascending within each type
    files = [l for l in lines if l["type"] == "file"]
    assert len(files) == 4
    for f in files:
        assert f["sample_rate"] in (16000, 44100)
        assert f["channels"] >= 1
        assert f["params"]["min_clip_ms"] == 80.0
        assert f["silence_total_ms"] >= 0.0
    clips = [l for l in lines if l["type"] == "clip"]
    assert 3 <= len(clips) <= 6
    for c in clips:
        assert isinstance(c["start_sample"], int) and c["start_sample"] >= 0
        assert c["end_sample"] > c["start_sample"]


def test_bad_inputs_fail_per_file(fixture_dir, db, capsys):
    bad_dir = os.path.join(fixture_dir, "bad")
    good = os.path.join(fixture_dir, "voice_with_noise.wav")
    # mix one good file with the corrupted ones via a combined dir
    import shutil
    combo = os.path.join(os.path.dirname(db), "combo")
    os.makedirs(combo)
    shutil.copy(good, combo)
    for name in os.listdir(bad_dir):
        shutil.copy(os.path.join(bad_dir, name), combo)
    rc = scan(combo, db)
    out = capsys.readouterr().out
    assert rc == 1
    assert "files_ok=1" in out
    assert "files_failed=4" in out
    con = sqlite3.connect(db)
    rows = con.execute(
        "SELECT status, error FROM files WHERE status='failed'").fetchall()
    assert len(rows) == 4
    assert all(r[1] for r in rows)  # every failure carries a diagnostic


@pytest.mark.parametrize("bad_args", [
    ["--frame-ms", "-5"],
    ["--hop-ms", "0"],
    ["--frame-ms", "10", "--hop-ms", "20"],  # hop > frame is contradictory
    ["--noise-margin-db", "-1"],
    ["--min-clip-ms", "0"],
    ["--fade-guard-ms", "-30"],
])
def test_param_errors_exit_2(fixture_dir, db, bad_args):
    rc = cli.main(["scan", "--input", fixture_dir, "--db", db, *bad_args])
    assert rc == 2


def test_corrupt_database_exit_2(fixture_dir, tmp_path, capsys):
    bad_db = str(tmp_path / "corrupt.sqlite")
    with open(bad_db, "wb") as fh:
        fh.write(b"this is not a sqlite database" * 10)
    rc = scan(fixture_dir, bad_db)
    assert rc == 2
    assert "error:" in capsys.readouterr().err


def test_export_refuses_to_overwrite_input(fixture_dir, db):
    scan(fixture_dir, db)
    target = os.path.join(fixture_dir, "silence.wav")
    assert cli.main(["export", "--db", db, "--out", target]) == 2


def test_export_unwritable_path_exit_2(fixture_dir, db):
    scan(fixture_dir, db)
    missing = os.path.join(db + "-no-such-dir", "out.jsonl")
    assert cli.main(["export", "--db", db, "--out", missing]) == 2


def test_query_reports_status(fixture_dir, db, capsys):
    scan(fixture_dir, db)
    capsys.readouterr()
    assert cli.main(["query", "--db", db]) == 0
    out = capsys.readouterr().out
    assert "status=ok" in out
    assert "files=4" in out
