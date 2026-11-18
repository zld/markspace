"""End-to-end tests for the mediascan pipeline.

Fixtures are generated in ``tmp_path`` by importing ``tools/make_samples.py``;
nothing here depends on files committed to the repository.
"""

from __future__ import annotations

import json
import os
import sqlite3
import sys
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))
sys.path.insert(0, str(ROOT / "tools"))

import make_samples  # noqa: E402
from mediascan import cli, compare, extract, sniff, wav  # noqa: E402
from mediascan.errors import ScanFailure  # noqa: E402


@pytest.fixture()
def samples(tmp_path: Path) -> Path:
    out = tmp_path / "samples"
    out.mkdir()
    for name, data in make_samples.build_samples().items():
        (out / name).write_bytes(data)
    (out / "reference.json").write_text(
        json.dumps(make_samples.build_baseline()), encoding="utf-8"
    )
    return out


def _check(samples: Path, tmp_path: Path, *extra: str) -> tuple[int, dict, str]:
    json_path = tmp_path / "report.json"
    db = tmp_path / "index.sqlite"
    argv = [
        "check", str(samples),
        "--baseline", str(samples / "reference.json"),
        "--db", str(db),
        "--json", str(json_path),
        *extra,
    ]
    import io
    from contextlib import redirect_stdout

    buf = io.StringIO()
    with redirect_stdout(buf):
        code = cli.main(argv)
    payload = json.loads(json_path.read_text(encoding="utf-8"))
    return code, payload, buf.getvalue()


def test_check_matches_acceptance_summary(samples: Path, tmp_path: Path) -> None:
    code, payload, out = _check(samples, tmp_path, "--duration-tolerance-ms", "25")
    summary = payload["summary"]
    assert summary["files_scanned"] == 4
    assert summary["tracks_total"] == 7
    assert summary["mismatches"] == 3
    assert summary["failures"] == 1
    assert summary["max_duration_delta_ms"] == 120
    line = out.splitlines()[0]
    assert line == (
        "files_scanned=4 tracks_total=7 mismatches=3 failures=1 "
        "max_duration_delta_ms=120"
    )
    assert code == 1
    assert {"summary", "files", "mismatches"} <= payload.keys()


def test_mismatches_carry_codes_and_fields(samples: Path, tmp_path: Path) -> None:
    _, payload, _ = _check(samples, tmp_path)
    codes = {m["error_code"] for m in payload["mismatches"]}
    assert codes == {"ERR_TIMEBASE_DRIFT", "ERR_CODEC_PARAM", "ERR_DURATION_DRIFT"}
    by_code = {m["error_code"]: m for m in payload["mismatches"]}
    drift = by_code["ERR_DURATION_DRIFT"]
    assert drift["duration_delta_ms"] == 120
    assert drift["duration_delta_relative"] == pytest.approx(6.0)
    assert by_code["ERR_CODEC_PARAM"]["codec_summary"]["channels"] == 1
    assert all(m["file"] == "scene_b.mp4" for m in payload["mismatches"])


def test_broken_box_is_located_with_offset(samples: Path, tmp_path: Path) -> None:
    _, payload, out = _check(samples, tmp_path)
    failure = payload["failures"][0]
    assert failure["path"] == "scene_broken.mp4"
    assert failure["error_code"] == "ERR_BOX_LENGTH"
    assert failure["error_offset"] == 140
    assert "scene_broken.mp4" in out


def test_unknown_fields_not_guessed(samples: Path, tmp_path: Path) -> None:
    _, payload, _ = _check(samples, tmp_path)
    scene_a = next(f for f in payload["files"] if f["path"] == "scene_a.mp4")
    subtitle = next(t for t in scene_a["tracks"] if t["type"] == "subtitle")
    assert subtitle["codec"] == "tx3g"
    audio = next(t for t in scene_a["tracks"] if t["type"] == "audio")
    assert audio["bit_depth"] is None
    assert "bit_depth" in audio["unknown_fields"]


def test_container_truth_beats_extension(tmp_path: Path) -> None:
    wav_path = tmp_path / "actually_wav.mp4"
    wav_path.write_bytes(make_samples.wav_bytes(8000, 1, 8, 80))
    result = sniff.sniff_path(str(wav_path))
    assert result.kind == "wav"
    assert result.extension_matches is False
    record = extract.extract_file(str(wav_path))
    assert record.container["container"] == "wav"
    assert record.duration_ms == 10

    fake = tmp_path / "fake.wav"
    fake.write_bytes(b"not a container at all")
    with pytest.raises(ScanFailure) as err:
        sniff.sniff_path(str(fake))
    assert err.value.code == "ERR_UNKNOWN_FORMAT"
    assert err.value.offset == 4


def test_truncated_and_oversized_limits(tmp_path: Path) -> None:
    truncated = tmp_path / "clip.wav"
    truncated.write_bytes(b"RIFF\x10\x00\x00\x00WAVEfmt ")
    # The signature is recognisable, so sniffing succeeds; extraction is what
    # reports the boundary violation.
    assert sniff.sniff_path(str(truncated)).kind == "wav"
    with pytest.raises(ScanFailure) as err:
        extract.extract_file(str(truncated))
    assert err.value.code == "ERR_TRUNCATED"
    assert err.value.offset == 4

    from mediascan.limits import Limits

    big = tmp_path / "big.wav"
    big.write_bytes(b"\x00" * 64)
    with pytest.raises(ScanFailure) as err:
        sniff.sniff_path(str(big), Limits(max_file_size=16))
    assert err.value.code == "ERR_FILE_TOO_LARGE"


def test_index_upserts_without_duplicates(samples: Path, tmp_path: Path) -> None:
    db = tmp_path / "index.sqlite"
    json_path = tmp_path / "r.json"
    argv = [
        "scan", str(samples), "--db", str(db), "--json", str(json_path),
    ]
    assert cli.main(argv) == 1  # broken sample fails, others index fine
    with sqlite3.connect(db) as conn:
        first = conn.execute("SELECT COUNT(*) FROM scans").fetchone()[0]
        ok = conn.execute(
            "SELECT COUNT(*) FROM scans WHERE status='ok'"
        ).fetchone()[0]
    assert first == 5
    assert ok == 4
    assert cli.main(argv) == 1
    with sqlite3.connect(db) as conn:
        second = conn.execute("SELECT COUNT(*) FROM scans").fetchone()[0]
        tracks = conn.execute("SELECT COUNT(*) FROM tracks").fetchone()[0]
    assert second == first  # update, never duplicate
    assert tracks == 7


def test_store_failure_is_isolated(tmp_path: Path) -> None:
    from mediascan.store import Store

    good = tmp_path / "good.wav"
    good.write_bytes(make_samples.wav_bytes(8000, 1, 8, 80))
    record = extract.extract_file(str(good))
    with Store(str(tmp_path / "index.sqlite")) as st:
        st.record_scan("good.wav", record)
        st.record_failure("bad.mp4", "bad.mp4", "ERR_BOX_LENGTH")
        assert st.fetch("good.wav")["sha256"] == record.sha256
        assert st.fetch("bad.mp4")["status"] == "failed"
        # Re-scanning the same key replaces tracks in place.
        st.record_scan("good.wav", record)
        assert st.fetch("good.wav")["track_count"] == 1


def test_zero_duration_and_duplicate_tracks() -> None:
    from mediascan.extract import Record, Track

    record = Record(
        path="a.mp4",
        size_bytes=1,
        sha256="x",
        container={},
        duration_ms=0,
        tracks=[
            Track(1, "audio", codec="aac", sample_rate=48000, channels=2,
                  duration_ms=0, timescale=48000),
            Track(2, "audio", codec="aac", sample_rate=44100, channels=2,
                  duration_ms=0, timescale=44100),
        ],
    )
    baseline = compare.Baseline(
        {
            "a.mp4": compare.ExpectedFile(
                "a.mp4",
                0,
                [
                    compare.ExpectedTrack(
                        "audio",
                        {"codec": "aac", "sample_rate": 48000, "channels": 2},
                        0,
                        48000,
                    ),
                    compare.ExpectedTrack(
                        "audio",
                        {"codec": "aac", "sample_rate": 44100, "channels": 2},
                        0,
                        44100,
                    ),
                ],
            )
        }
    )
    result = compare.compare_record(record, baseline, 0)
    assert result.mismatches == []
    assert result.max_duration_delta_ms == 0

    record.tracks[1] = Track(2, "video", codec="avc1", width=640, height=360)
    result = compare.compare_record(record, baseline, 0)
    codes = [m.error_code for m in result.mismatches]
    assert codes == ["ERR_TRACK_TYPE"]


def test_track_missing_and_extra() -> None:
    from mediascan.extract import Record, Track

    baseline = compare.Baseline(
        {
            "b.mp4": compare.ExpectedFile(
                "b.mp4",
                1000,
                [
                    compare.ExpectedTrack("audio", {"codec": "aac"}, 1000, 48000),
                    compare.ExpectedTrack("audio", {"codec": "aac"}, 1000, 48000),
                ],
            )
        }
    )
    record = Record(
        path="b.mp4",
        size_bytes=1,
        sha256="x",
        container={},
        duration_ms=1000,
        tracks=[
            Track(1, "audio", codec="aac", sample_rate=48000, channels=1,
                  duration_ms=1000, timescale=48000),
            Track(2, "audio", codec="aac", sample_rate=48000, channels=1,
                  duration_ms=1000, timescale=48000),
            Track(3, "subtitle", codec="tx3g", duration_ms=1000),
        ],
    )
    result = compare.compare_record(record, baseline, 0)
    counts = result.categories
    assert counts["track_extra"] == 1
    assert result.mismatches[0].error_code == "ERR_TRACK_EXTRA"


def test_relative_tolerance_and_baseline_errors(tmp_path: Path, samples: Path) -> None:
    _, payload, _ = _check(samples, tmp_path, "--relative-tolerance-pct", "10")
    assert not [m for m in payload["mismatches"] if m["error_code"] == "ERR_DURATION_DRIFT"]
    code = cli.main(
        ["check", str(samples), "--baseline", str(tmp_path / "missing.json")]
    )
    assert code == 2
    bad = tmp_path / "bad.json"
    bad.write_text("{not json", encoding="utf-8")
    assert cli.main(["check", str(samples), "--baseline", str(bad)]) == 2


def test_baseline_missing_entry_counts_as_mismatch(samples: Path, tmp_path: Path) -> None:
    baseline = make_samples.build_baseline()
    del baseline["files"]["voiceover.wav"]
    path = tmp_path / "partial.json"
    path.write_text(json.dumps(baseline), encoding="utf-8")
    report_path = tmp_path / "out.json"
    code = cli.main(
        ["check", str(samples), "--baseline", str(path), "--json", str(report_path)]
    )
    payload = json.loads(report_path.read_text(encoding="utf-8"))
    assert code == 1
    assert any(
        m["error_code"] == "ERR_BASELINE_MISSING" and m["file"] == "voiceover.wav"
        for m in payload["mismatches"]
    )


def test_json_replace_failure_keeps_terminal_summary(
    samples: Path, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    blocker = tmp_path / "blocked"
    blocker.mkdir()
    json_path = blocker  # a directory can never be replaced by a file
    code = cli.main(
        [
            "check", str(samples),
            "--baseline", str(samples / "reference.json"),
            "--json", str(json_path),
        ]
    )
    captured = capsys.readouterr()
    assert code == 1
    assert "files_scanned=4" in captured.out
    assert "report not written" in captured.err
    assert not os.path.exists(str(json_path) + ".tmp")


def test_symlinks_are_ignored(samples: Path, tmp_path: Path) -> None:
    link = tmp_path / "linked.wav"
    os.symlink(samples / "voiceover.wav", link)
    root = tmp_path / "root"
    root.mkdir()
    os.symlink(samples / "voiceover.wav", root / "sneaky.wav")
    json_path = tmp_path / "sym.json"
    cli.main(
        ["scan", str(root), "--json", str(json_path)]
    )
    payload = json.loads(json_path.read_text(encoding="utf-8"))
    assert payload["summary"]["files_scanned"] == 0
    assert payload["skipped"][0]["reason"] == "symlink"


def test_pack_uses_same_extractor(samples: Path, tmp_path: Path) -> None:
    json_path = tmp_path / "pack.json"
    code = cli.main(["pack", str(samples), "--json", str(json_path)])
    payload = json.loads(json_path.read_text(encoding="utf-8"))
    assert code == 1  # broken file fails packaging
    ok_paths = {f["path"] for f in payload["files"]}
    assert {"scene_a.mp4", "voiceover.wav", "room_tone.aiff"} <= ok_paths
    assert all(len(f["sha256"]) == 64 for f in payload["files"])
    failed = {f["path"] for f in payload["failures"]}
    assert "scene_broken.mp4" in failed
    assert payload["summary"]["tracks_total"] == 7

    strict_path = tmp_path / "strict.json"
    assert cli.main(
        ["pack", str(samples), "--json", str(strict_path), "--require-known"]
    ) == 1
    strict = json.loads(strict_path.read_text(encoding="utf-8"))
    unknown = {f["path"] for f in strict["failures"]
               if f["error_code"] == "ERR_PARAM_UNKNOWN"}
    assert "scene_a.mp4" in unknown


def test_aiff_sample_rate_roundtrip() -> None:
    for rate in (8000, 44100, 48000, 96000):
        raw = wav.encode_extended(rate)
        assert round(wav._decode_extended(raw)) == rate
