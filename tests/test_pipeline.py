"""End-to-end and edge-case tests for the mediaaudit pipeline."""
from __future__ import annotations

import json
import sqlite3
import subprocess
import sys
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))

from scripts import make_samples as ms  # noqa: E402


def run_cli(*args):
    return subprocess.run([sys.executable, "-m", "mediaaudit", *args],
                          cwd=ROOT, capture_output=True, text=True)


def make_set(tmp_path, drift=False):
    out = tmp_path / "media"
    ms.build_set(out, inject_drift=drift)
    return out


def audit(media, tmp_path, *extra):
    report = tmp_path / "report.json"
    proc = run_cli("audit", "--manifest", str(media / "manifest.json"),
                   "--db", str(tmp_path / "audit.sqlite"),
                   "--json", str(report), *extra)
    data = json.loads(report.read_text()) if report.exists() else None
    return proc, data


def rewrite_manifest(media, files):
    mpath = media / "manifest.json"
    manifest = json.loads(mpath.read_text())
    manifest["files"] = files
    mpath.write_text(json.dumps(manifest))


def kinds(report):
    return sorted(i["kind"] for i in report["mismatches"])


def test_clean_audit_passes(tmp_path):
    media = make_set(tmp_path)
    proc, report = audit(media, tmp_path)
    assert proc.returncode == 0, proc.stderr
    assert report["status"] == "pass"
    assert report["files"] == 4 and report["tracks"] == 8
    assert report["max_duration_delta_ms"] == 0
    assert report["mismatches"] == []
    for key in ("track_count", "duration_delta_ms", "codec_summary"):
        assert key in report
        assert key in proc.stdout


def test_drift_audit_flags_two_mismatches(tmp_path):
    media = make_set(tmp_path, drift=True)
    proc, report = audit(media, tmp_path, "--workers", "4")
    assert proc.returncode == 1
    assert report["files"] == 4 and report["tracks"] == 8
    assert report["max_duration_delta_ms"] == 17
    assert len(report["mismatches"]) == 2
    assert kinds(report) == ["duration_delta", "sample_rate_mismatch"]
    assert "mismatches: 2" in proc.stdout
    assert "max_duration_delta_ms: 17" in proc.stdout


def test_workers_keep_manifest_order(tmp_path):
    media = make_set(tmp_path)
    _, report = audit(media, tmp_path, "--workers", "4")
    assert [f["path"] for f in report["per_file"]] == ms.FILES


def test_pack_clean_builds_deterministic_archive(tmp_path):
    media = make_set(tmp_path)
    zips = []
    for name in ("a.zip", "b.zip"):
        out = tmp_path / name
        proc = run_cli("pack", "--manifest", str(media / "manifest.json"),
                       "--db", str(tmp_path / f"{name}.sqlite"),
                       "--out", str(out))
        assert proc.returncode == 0, proc.stderr
        zips.append(out.read_bytes())
    assert zips[0] == zips[1]
    with zipfile.ZipFile(tmp_path / "a.zip") as zf:
        names = zf.namelist()
        assert names == sorted(ms.FILES) + ["manifest.json"]
        payload = json.loads(zf.read("manifest.json"))
    assert payload["show_id"] == ms.SHOW_ID
    assert [f["path"] for f in payload["files"]] == ms.FILES


def test_pack_drift_leaves_no_archive(tmp_path):
    media = make_set(tmp_path, drift=True)
    out = tmp_path / "capture.zip"
    proc = run_cli("pack", "--manifest", str(media / "manifest.json"),
                   "--db", str(tmp_path / "pack.sqlite"), "--out", str(out))
    assert proc.returncode == 1
    assert not out.exists()
    assert not list(tmp_path.glob("*.tmp*"))


def test_truncated_mp4_recorded_and_run_continues(tmp_path):
    media = make_set(tmp_path)
    target = media / "master.mp4"
    data = target.read_bytes()
    target.write_bytes(data[: len(data) // 2])
    proc, report = audit(media, tmp_path)
    assert proc.returncode == 1
    assert report["tracks"] == 6
    assert "extract_failed" in kinds(report)
    issue = next(i for i in report["mismatches"]
                 if i["kind"] == "extract_failed")
    assert issue["path"] == "master.mp4"


def test_zero_timescale_rejected(tmp_path):
    media = make_set(tmp_path)
    (media / "master.mp4").write_bytes(ms.write_mp4(video_timescale=0))
    proc, report = audit(media, tmp_path)
    assert proc.returncode == 1
    assert "extract_failed" in kinds(report)


def test_unknown_codec_flagged(tmp_path):
    media = make_set(tmp_path)
    (media / "master.mp4").write_bytes(ms.write_mp4(video_fmt=b"zzzz"))
    proc, report = audit(media, tmp_path)
    assert proc.returncode == 1
    assert "unknown_codec" in kinds(report)


def test_duplicate_track_id_flagged(tmp_path):
    media = make_set(tmp_path)
    (media / "diagnostic.mkv").write_bytes(
        ms.write_mkv(track_numbers=(1, 1)))
    proc, report = audit(media, tmp_path)
    assert proc.returncode == 1
    assert "duplicate_track_id" in kinds(report)


def test_missing_required_track_flagged(tmp_path):
    media = make_set(tmp_path)
    (media / "master.mp4").write_bytes(ms.write_mp4(include_audio=False))
    proc, report = audit(media, tmp_path)
    assert proc.returncode == 1
    assert "missing_track" in kinds(report)


def test_truncated_mkv_recorded(tmp_path):
    media = make_set(tmp_path)
    target = media / "diagnostic.mkv"
    data = target.read_bytes()
    target.write_bytes(data[: int(len(data) * 0.6)])
    proc, report = audit(media, tmp_path)
    assert proc.returncode == 1
    assert "extract_failed" in kinds(report)


def test_symlink_rejected(tmp_path):
    media = make_set(tmp_path)
    (media / "link.mp4").symlink_to(media / "master.mp4")
    rewrite_manifest(media, ["link.mp4"] + ms.FILES)
    proc, report = audit(media, tmp_path)
    assert proc.returncode == 1
    assert "rejected_path" in kinds(report)


def test_path_escape_rejected(tmp_path):
    media = make_set(tmp_path)
    rewrite_manifest(media, ["../escape.mp4"] + ms.FILES)
    proc, report = audit(media, tmp_path)
    assert proc.returncode == 1
    assert "rejected_path" in kinds(report)


def test_oversize_rejected(tmp_path):
    media = make_set(tmp_path)
    proc, report = audit(media, tmp_path, "--max-bytes", "100")
    assert proc.returncode == 1
    assert report["tracks"] == 0
    assert kinds(report) == ["rejected_path"] * 4


def test_run_persisted_in_sqlite(tmp_path):
    media = make_set(tmp_path, drift=True)
    db = tmp_path / "audit.sqlite"
    audit(media, tmp_path)
    with sqlite3.connect(db) as conn:
        run = conn.execute(
            "SELECT kind, status, files, tracks, mismatches FROM runs"
        ).fetchone()
        assert run == ("audit", "fail", 4, 8, 2)
        assert conn.execute("SELECT COUNT(*) FROM files").fetchone()[0] == 4
        assert conn.execute("SELECT COUNT(*) FROM tracks").fetchone()[0] == 8
        assert conn.execute("SELECT COUNT(*) FROM issues").fetchone()[0] == 2


def test_missing_manifest_is_fatal(tmp_path):
    proc = run_cli("audit", "--manifest", str(tmp_path / "nope.json"),
                   "--db", str(tmp_path / "x.sqlite"))
    assert proc.returncode == 2
