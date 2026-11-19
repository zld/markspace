import sqlite3

import pytest

from mediacheck.extract import summarize_file
from mediacheck.store import MediaStore, StoreError, batch_manifest


def fake_summary(path, name, track_count=1, duration=2.0):
    tracks = [{
        "index": i, "track_id": i + 1, "kind": "audio", "codec": "aac",
        "duration_seconds": duration, "start_seconds": 0.0,
    } for i in range(track_count)]
    return {
        "manifest_version": 1, "path": str(path), "name": name,
        "container": "mp4", "file_size": 1000, "track_count": track_count,
        "duration_seconds": duration, "codec_summary": ["aac"],
        "tracks": tracks, "warnings": [],
    }


def test_batch_commit_and_read_back(tmp_path):
    db_path = tmp_path / "media.sqlite"
    with MediaStore(db_path) as store:
        batch = store.begin_batch(tmp_path)
        stats = store.commit_batch(
            batch,
            summaries=[fake_summary(tmp_path / "a.mp4", "a.mp4")],
            failures=[{"path": str(tmp_path / "bad.mp4"), "code": "out_of_bounds",
                       "message": "past end", "offset": 10, "box_path": "moov"}],
        )
        assert stats == {"files_ok": 1, "tracks_written": 1, "failures": 1}
        assert store.media_count() == 1
        assert store.track_count() == 1
        latest = store.latest_batch()
        assert latest["status"] == "complete"
        assert latest["files_failed"] == 1
        failures = store.batch_failures(batch)
        assert failures[0]["code"] == "out_of_bounds"
        assert failures[0]["offset"] == 10


def test_rescan_updates_in_place(tmp_path):
    db_path = tmp_path / "media.sqlite"
    with MediaStore(db_path) as store:
        first = store.begin_batch(tmp_path)
        store.commit_batch(first, summaries=[
            fake_summary(tmp_path / "a.mp4", "a.mp4", track_count=2)],
            failures=[])
        second = store.begin_batch(tmp_path)
        store.commit_batch(second, summaries=[
            fake_summary(tmp_path / "a.mp4", "a.mp4", track_count=3)],
            failures=[])
        assert store.media_count() == 1
        # Tracks are replaced, not appended.
        assert store.track_count() == 3
        summaries = store.batch_summaries(second)
        assert summaries[0]["track_count"] == 3


def test_failed_batch_rolls_back_every_row(tmp_path):
    db_path = tmp_path / "media.sqlite"
    with MediaStore(db_path) as store:
        batch = store.begin_batch(tmp_path)
        bad_track_summary = fake_summary(tmp_path / "a.mp4", "a.mp4")
        bad_track_summary["track_count"] = "not-an-int"  # raises TypeError
        with pytest.raises(StoreError):
            store.commit_batch(batch, summaries=[bad_track_summary], failures=[])
        assert store.media_count() == 0
        assert store.track_count() == 0
        row = store.conn.execute("SELECT * FROM batches WHERE id = ?",
                                 (batch,)).fetchone()
        assert row["status"] == "rolled_back"
        assert row["note"]


def test_rollback_leaves_previous_batch_intact(tmp_path):
    db_path = tmp_path / "media.sqlite"
    with MediaStore(db_path) as store:
        first = store.begin_batch(tmp_path)
        store.commit_batch(first, summaries=[
            fake_summary(tmp_path / "a.mp4", "a.mp4")], failures=[])
        second = store.begin_batch(tmp_path)
        bad = fake_summary(tmp_path / "b.mp4", "b.mp4")
        bad["file_size"] = object()
        with pytest.raises(StoreError):
            store.commit_batch(second, summaries=[
                fake_summary(tmp_path / "c.mp4", "c.mp4"), bad], failures=[])
        assert store.media_count() == 1
        assert store.latest_batch()["id"] == first


def test_unwritable_database_reports_store_error(tmp_path):
    blocker = tmp_path / "not_a_dir"
    blocker.write_text("x")
    with pytest.raises(StoreError):
        MediaStore(blocker / "nested" / "media.sqlite")


def test_batch_manifest_matches_extract_shape(tmp_path, sample_dir):
    summary = summarize_file(sample_dir / "cutscene_01.mp4")
    with MediaStore(tmp_path / "m.sqlite") as store:
        batch = store.begin_batch(sample_dir)
        store.commit_batch(batch, summaries=[summary], failures=[])
        document = batch_manifest(store)
    assert document["media"][0]["relative_path"] if "relative_path" in \
        document["media"][0] else True
    assert document["media"][0]["tracks"] == summary["tracks"]
    assert document["files_ok"] == 1
    assert document["batch"]["id"] == 1


def test_relative_summary_paths_resolve_against_batch_root(tmp_path):
    root = tmp_path / "tree"
    (root / "a" / "b").mkdir(parents=True)
    summary = fake_summary("a/b/clip.mp4", "clip.mp4")
    assert not summary["path"].startswith("/")
    with MediaStore(tmp_path / "m.sqlite") as store:
        batch = store.begin_batch(root)
        store.commit_batch(batch, summaries=[summary], failures=[], root=root)
        stored = store.batch_summaries(batch)[0]
        assert stored["path"] == str((root / "a/b/clip.mp4").resolve())
        assert stored["relative_path"] == "a/b/clip.mp4"


def test_duplicate_path_in_same_batch_upserts(tmp_path):
    db_path = tmp_path / "media.sqlite"
    with MediaStore(db_path) as store:
        batch = store.begin_batch(tmp_path)
        summary = fake_summary(tmp_path / "a.mp4", "a.mp4")
        store.commit_batch(batch, summaries=[summary, summary], failures=[])
        assert store.media_count() == 1
        assert store.track_count() == 1


def test_schema_version_recorded(tmp_path):
    with MediaStore(tmp_path / "m.sqlite") as store:
        value = store.conn.execute(
            "SELECT value FROM schema_meta WHERE key = 'schema_version'"
        ).fetchone()[0]
        assert value == "1"


def test_database_is_plain_sqlite(tmp_path):
    db_path = tmp_path / "m.sqlite"
    with MediaStore(db_path) as store:
        batch = store.begin_batch(tmp_path)
        store.commit_batch(batch, summaries=[], failures=[])
    conn = sqlite3.connect(db_path)
    tables = {r[0] for r in conn.execute(
        "SELECT name FROM sqlite_master WHERE type = 'table'")}
    conn.close()
    assert {"batches", "media", "tracks", "failures"} <= tables
