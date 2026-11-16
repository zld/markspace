"""SQLite store and shared packaging entry point tests."""

from __future__ import annotations

import os
import sys

import pytest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "tools"))

import make_samples as samples  # noqa: E402

from media_audit.package import PackageError, package_files  # noqa: E402
from media_audit.probe import probe_file  # noqa: E402
from media_audit.store import AuditStore  # noqa: E402


def _write(tmp_path, name: str, data: bytes) -> str:
    path = os.path.join(str(tmp_path), name)
    with open(path, "wb") as handle:
        handle.write(data)
    return path

def test_store_persists_and_reloads(tmp_path):
    db_path = os.path.join(str(tmp_path), "audit.sqlite")
    media_path = _write(tmp_path, "a.mp4", samples.sample_cutscene_a())
    info = probe_file(media_path)
    with AuditStore(db_path) as store:
        batch_id = store.start_batch(str(tmp_path), "manifest.json", 80)
        store.save_probe(batch_id, "a.mp4", info)
        reloaded = store.load_infos(batch_id)
    assert "a.mp4" in reloaded
    assert reloaded["a.mp4"].duration_ms == 10000
    assert len(reloaded["a.mp4"].tracks) == 3


def test_store_rerun_replaces_rows(tmp_path):
    db_path = os.path.join(str(tmp_path), "audit.sqlite")
    media_path = _write(tmp_path, "a.mp4", samples.sample_cutscene_a())
    info = probe_file(media_path)
    with AuditStore(db_path) as store:
        first = store.start_batch(str(tmp_path), "manifest.json", 80)
        store.save_probe(first, "a.mp4", info)
        second = store.start_batch(str(tmp_path), "manifest.json", 80)
        assert second == first  # same batch key is reused
        store.save_probe(second, "a.mp4", info)
        rows = store.conn.execute(
            "SELECT COUNT(*) AS n FROM files"
        ).fetchone()["n"]
    assert rows == 1


def test_store_savepoint_rolls_back_one_file(tmp_path):
    db_path = os.path.join(str(tmp_path), "audit.sqlite")
    media_path = _write(tmp_path, "a.mp4", samples.sample_cutscene_a())
    good = probe_file(media_path)
    with AuditStore(db_path) as store:
        batch_id = store.start_batch(str(tmp_path), "manifest.json", 80)
        store.save_probe(batch_id, "good.mp4", good)

        # A failing savepoint must roll back its own writes without touching
        # rows committed before it.
        with pytest.raises(RuntimeError):
            with store._savepoint("bad_file"):
                store.conn.execute(
                    "INSERT INTO files(batch_id, name, rel_path, container, "
                    "byte_size, brands, probed_at) VALUES(?, ?, ?, ?, ?, ?, ?)",
                    (batch_id, "bad.mp4", "bad.mp4", "mp4", 0, "[]",
                     "2026-01-01T00:00:00+00:00"),
                )
                raise RuntimeError("simulated mid-file failure")
        names = set(store.load_infos(batch_id).keys())
    assert names == {"good.mp4"}


# --- package ----------------------------------------------------------------


def test_package_copies_valid_files_with_summary(tmp_path):
    media_dir = tmp_path / "media"
    media_dir.mkdir()
    _write(str(media_dir), "a.mp4", samples.sample_cutscene_a())
    dest = tmp_path / "out"
    result = package_files(
        [os.path.join(str(media_dir), "a.mp4")],
        str(dest), str(media_dir),
    )
    assert result.input_summary["file_count"] == 1
    assert os.path.exists(os.path.join(str(dest), "package_manifest.json"))


def test_package_rejects_truncated_file_with_offset(tmp_path):
    media_dir = tmp_path / "media"
    media_dir.mkdir()
    bad_path = _write(str(media_dir), "bad.mkv", samples.sample_truncated())
    dest = tmp_path / "out"
    with pytest.raises(PackageError) as exc:
        package_files([bad_path], str(dest), str(media_dir))
    assert exc.value.offset is not None
    assert not os.path.exists(str(dest))  # nothing written before rejection


def test_store_records_and_idempotently_migrates_schema(tmp_path):
    db_path = os.path.join(str(tmp_path), "audit.sqlite")
    with AuditStore(db_path) as store:
        version = store.conn.execute(
            "SELECT value FROM meta WHERE key = 'schema_version'"
        ).fetchone()["value"]
    assert int(version) == 1
    # Reopening an up-to-date database must not duplicate or fail migration.
    with AuditStore(db_path) as store:
        version = store.conn.execute(
            "SELECT value FROM meta WHERE key = 'schema_version'"
        ).fetchone()["value"]
    assert int(version) == 1


def test_store_initialises_empty_database_and_stamps_version(tmp_path):
    import sqlite3

    db_path = os.path.join(str(tmp_path), "audit.sqlite")
    # A completely empty database file is upgraded to the full v1 schema.
    sqlite3.connect(db_path).close()
    with AuditStore(db_path) as store:
        version = store.conn.execute(
            "SELECT value FROM meta WHERE key = 'schema_version'"
        ).fetchone()["value"]
        tables = {
            row["name"]
            for row in store.conn.execute(
                "SELECT name FROM sqlite_master WHERE type = 'table'"
            ).fetchall()
        }
    assert int(version) == 1
    assert {"batches", "files", "tracks", "scan_events", "meta"} <= tables


def test_store_rejects_nonexistent_directory(tmp_path):
    with pytest.raises(Exception):
        AuditStore(os.path.join(str(tmp_path), "nope", "a.sqlite"))

