"""Pipeline tests: rebuild and validation must agree with each other."""
from __future__ import annotations

import json
import shutil

import pytest

from mediapack import cli, fixture, validate
from mediapack.rebuild import compute_fragments, rebuild_file

from build_fixtures import build_all


@pytest.fixture(scope="module")
def built(tmp_path_factory):
    return build_all(tmp_path_factory.mktemp("fixtures"))


def test_valid_fixture_passes(built):
    report = validate.validate_file(built["valid"])
    assert report.ok, report.diagnostics
    assert report.sample_count == 5
    assert report.timescale == 1000
    # the shipped sidx describes an older layout: recoverable, not an error
    assert report.index_offset_check == "STALE"
    assert any(d.code == "index-stale" for d in report.diagnostics)


def test_fragment_planning(built):
    report = validate.validate_file(built["valid"])
    frags = compute_fragments(report.table.samples, 2000)
    assert [f.end_sample for f in frags] == [2, 4, 5]
    assert [f.duration for f in frags] == [2000, 2000, 1000]
    assert [f.delta for f in frags] == [0, 0, -1000]


def test_rebuild_produces_valid_index(built, tmp_path):
    src = tmp_path / "work.mp4"
    shutil.copy(built["valid"], src)
    result = rebuild_file(str(src), 2.0)
    assert result.ok, result.message
    assert result.reference_count == 3
    assert [f.end_sample for f in result.fragments] == [2, 4, 5]
    final = validate.validate_file(src)
    assert final.ok, final.diagnostics
    assert final.index_offset_check == "PASS"
    assert final.sample_count == 5


def test_offset_drift_rejected_and_untouched(built, tmp_path):
    src = tmp_path / "drift.mp4"
    shutil.copy(built["offset-drift"], src)
    before = src.read_bytes()
    report = validate.validate_file(src)
    assert not report.ok
    assert any(d.code == "sample-digest" for d in report.diagnostics)
    result = rebuild_file(str(src), 2.0)
    assert not result.ok
    assert src.read_bytes() == before  # no successful index was written


def test_truncated_media_rejected(built):
    report = validate.validate_file(built["truncated"])
    assert not report.ok
    assert any(d.code == "structure" for d in report.diagnostics)


def test_stsc_count_mismatch_rejected(built):
    report = validate.validate_file(built["stsc-mismatch"])
    assert not report.ok
    assert any(d.code == "structure" for d in report.diagnostics)


def test_tampered_sample_rejected(built):
    report = validate.validate_file(built["tampered"])
    assert not report.ok
    assert any(d.code == "sample-digest" for d in report.diagnostics)


def test_missing_sidx_is_recoverable(built, tmp_path):
    report = validate.validate_file(built["no-sidx"])
    assert report.ok, report.diagnostics
    assert report.index_offset_check == "ABSENT"
    src = tmp_path / "nosidx.mp4"
    shutil.copy(built["no-sidx"], src)
    result = rebuild_file(str(src), 2.0)
    assert result.ok, result.message
    assert validate.validate_file(src).index_offset_check == "PASS"


def test_co64_offsets_roundtrip(built, tmp_path):
    report = validate.validate_file(built["co64"])
    assert report.ok, report.diagnostics
    assert report.table.offset_width == 8
    src = tmp_path / "co64.mp4"
    shutil.copy(built["co64"], src)
    result = rebuild_file(str(src), 2.0)
    assert result.ok, result.message
    final = validate.validate_file(src)
    assert final.index_offset_check == "PASS"
    assert final.table.offset_width == 8


def test_oversize_input_rejected(tmp_path):
    big = tmp_path / "big.mp4"
    with open(big, "wb") as fh:
        fh.truncate(2 * 1024**3 + 1)  # sparse file, no real data
    report = validate.validate_file(big)
    assert not report.ok
    assert any(d.code == "file-too-large" for d in report.diagnostics)


def test_sidx_codec_roundtrip(tmp_path):
    from mediapack.sidx import encode_sidx, decode_sidx
    refs = [
        fixture.SidxReference(184, 2000, True, 0),
        fixture.SidxReference(216, 2000, True, 0),
    ]
    blob = encode_sidx(1000, 0, 40, refs)
    sidx = decode_sidx(blob[8:], 0, len(blob))
    assert sidx.references == refs
    assert sidx.first_offset == 40
    assert sidx.timescale == 1000


def test_cli_demo(tmp_path, capsys):
    rc = cli.main(
        ["demo", "--workdir", str(tmp_path / "demo"), "--target-seconds", "2"]
    )
    assert rc == 0
    out = json.loads(capsys.readouterr().out)
    assert out["validation"] == "PASS"
    assert out["sample_count"] == 5
    assert out["index_offset_check"] == "PASS"
    assert out["rebuilt_boundaries_samples"] == [2, 4, 5]
    assert out["boundary_durations_seconds"] == [2, 2, 1]
    assert out["reference_count"] == 3


def test_cli_demo_corrupt(tmp_path, capsys):
    rc = cli.main(
        [
            "demo",
            "--workdir",
            str(tmp_path / "bad"),
            "--target-seconds",
            "2",
            "--stbl-offset-delta",
            "4",
        ]
    )
    assert rc == 2
    out = json.loads(capsys.readouterr().out)
    assert out["validation"] == "FAIL"


def test_cli_check_exit_codes(built, tmp_path, capsys):
    assert cli.main(["check", str(built["valid"])]) == 0
    assert cli.main(["check", str(built["offset-drift"])]) == 2
    assert cli.main(["check", str(tmp_path / "nope.mp4")]) == 3
    capsys.readouterr()


def test_cli_rebuild_command(built, tmp_path, capsys):
    src = tmp_path / "in.mp4"
    shutil.copy(built["valid"], src)
    rc = cli.main(["rebuild", str(src), "--target-seconds", "2"])
    assert rc == 0
    out = json.loads(capsys.readouterr().out)
    assert out["rebuilt"] is True
    assert out["index_offset_check"] == "PASS"
