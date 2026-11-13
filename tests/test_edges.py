"""Edge cases: timezones/DST, corruption categories, drift limits, errors."""

from __future__ import annotations

import contextlib
import errno
import hashlib
import io
import json
import sqlite3
import sys
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path
from unittest import mock
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from forensic_timeline import cli, clock, integrity, reader, samplegen, store, timeline  # noqa: E402
from forensic_timeline.models import (  # noqa: E402
    BlockInfo, Confidence, Event, IntegrityStatus,
)
from forensic_timeline.samplegen import BLOCK_SIZE  # noqa: E402

NY = ZoneInfo("America/New_York")
SH = ZoneInfo("Asia/Shanghai")


def make_event(event_id: str, utc: datetime, seq: int = 0) -> Event:
    return Event(
        event_id=event_id, raw_timestamp=utc.isoformat(), raw_timezone="+00:00",
        naive=False, utc_raw=utc, utc_final=utc, corrected=False,
        confidence=Confidence.LOW, event_type="t", description="d",
        source_block=0, source_offset=0, seq=seq,
    )


class TimezoneEdgeTest(unittest.TestCase):
    def test_naive_timestamp_uses_configured_zone(self) -> None:
        parsed = clock.parse_record_time("2026-06-01T12:00:00", SH, "Asia/Shanghai")
        self.assertIsNone(parsed.anomaly)
        self.assertTrue(parsed.naive)
        self.assertEqual(parsed.utc, datetime(2026, 6, 1, 4, 0, tzinfo=timezone.utc))

    def test_explicit_offset_used_directly(self) -> None:
        parsed = clock.parse_record_time("2026-06-01T12:00:00+08:00", SH, "Asia/Shanghai")
        self.assertIsNone(parsed.anomaly)
        self.assertFalse(parsed.naive)
        self.assertEqual(parsed.utc, datetime(2026, 6, 1, 4, 0, tzinfo=timezone.utc))

    def test_ambiguous_local_time_isolated(self) -> None:
        # 2026-11-01 01:30 occurs twice in America/New_York (DST fall-back).
        parsed = clock.parse_record_time("2026-11-01T01:30:00", NY, "America/New_York")
        self.assertEqual(parsed.anomaly, "ambiguous_local_time")
        self.assertIsNone(parsed.utc)

    def test_nonexistent_local_time_isolated(self) -> None:
        # 2026-03-08 02:30 does not exist in America/New_York (spring-forward).
        parsed = clock.parse_record_time("2026-03-08T02:30:00", NY, "America/New_York")
        self.assertEqual(parsed.anomaly, "nonexistent_local_time")
        self.assertIsNone(parsed.utc)

    def test_unparseable_timestamp_flagged(self) -> None:
        parsed = clock.parse_record_time("not-a-time", SH, "Asia/Shanghai")
        self.assertEqual(parsed.anomaly, "unparseable_time")


class DriftEdgeTest(unittest.TestCase):
    def anchors(self, true: datetime, n: int) -> list[dict]:
        return [
            {"event_id": f"e{i}",
             "true_utc": (true + timedelta(minutes=i)).isoformat()}
            for i in range(n)
        ]

    def utc_map(self, true: datetime, drift: int, n: int) -> dict[str, datetime]:
        return {
            f"e{i}": true + timedelta(minutes=i, seconds=drift)
            for i in range(n)
        }

    def test_drift_within_limit_applied(self) -> None:
        true = datetime(2026, 1, 1, tzinfo=timezone.utc)
        est = clock.estimate_drift(self.utc_map(true, 37, 3), self.anchors(true, 3), 180, 2)
        self.assertTrue(est.applied)
        self.assertEqual(est.drift_seconds, 37)
        self.assertEqual(est.confidence, Confidence.HIGH)

    def test_drift_beyond_limit_not_applied(self) -> None:
        true = datetime(2026, 1, 1, tzinfo=timezone.utc)
        est = clock.estimate_drift(self.utc_map(true, 300, 3), self.anchors(true, 3), 180, 2)
        self.assertFalse(est.applied)
        self.assertEqual(est.drift_seconds, 300)
        self.assertEqual(est.confidence, Confidence.LOW)
        self.assertIn("exceeds limit", est.reason)

    def test_insufficient_anchors_not_applied(self) -> None:
        true = datetime(2026, 1, 1, tzinfo=timezone.utc)
        est = clock.estimate_drift(self.utc_map(true, 37, 1), self.anchors(true, 1), 180, 2)
        self.assertFalse(est.applied)
        self.assertEqual(est.drift_seconds, 0)
        self.assertIn("insufficient anchors", est.reason)


class IntegrityEdgeTest(unittest.TestCase):
    def test_categories_are_distinct(self) -> None:
        blocks = [
            BlockInfo(0, 0, BLOCK_SIZE, hashlib.sha256(b"a" * BLOCK_SIZE).hexdigest()),
            BlockInfo(1, BLOCK_SIZE, BLOCK_SIZE, hashlib.sha256(b"b" * BLOCK_SIZE).hexdigest()),
            BlockInfo(2, 2 * BLOCK_SIZE, BLOCK_SIZE, hashlib.sha256(b"c" * BLOCK_SIZE).hexdigest()),
        ]
        manifest_blocks = [
            {"index": 0, "offset": 0, "sha256": "not-a-hash"},            # bad format
            {"index": 1, "offset": BLOCK_SIZE, "sha256": "0" * 64},       # mismatch
            {"index": 9, "offset": 9 * BLOCK_SIZE, "sha256": "f" * 64},   # missing block
            # image block 2 is not listed -> extra block
        ]
        results = integrity.verify_blocks(blocks, manifest_blocks)
        by_status = {}
        for r in results:
            by_status.setdefault(r.status, []).append(r)
        self.assertEqual([r.block_index for r in by_status[IntegrityStatus.BAD_HASH_FORMAT]], [0])
        self.assertEqual([r.block_index for r in by_status[IntegrityStatus.HASH_MISMATCH]], [1])
        self.assertEqual([r.block_index for r in by_status[IntegrityStatus.MISSING_BLOCK]], [9])
        self.assertEqual([r.block_index for r in by_status[IntegrityStatus.EXTRA_BLOCK]], [2])
        self.assertNotIn(IntegrityStatus.OK, by_status)

    def test_ok_blocks_reported_ok(self) -> None:
        digest = hashlib.sha256(b"x" * BLOCK_SIZE).hexdigest()
        blocks = [BlockInfo(0, 0, BLOCK_SIZE, digest)]
        results = integrity.verify_blocks(blocks, [{"index": 0, "offset": 0, "sha256": digest}])
        self.assertEqual([r.status for r in results], [IntegrityStatus.OK])


class ReaderEdgeTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.dir = Path(self._tmp.name)
        self.kw = dict(block_size=BLOCK_SIZE, max_blocks=100,
                       max_image_bytes=1 << 20, max_records=1000)

    def write_image(self, data: bytes) -> Path:
        path = self.dir / "img.bin"
        path.write_bytes(data)
        return path

    def test_truncated_tail_flagged_not_fatal(self) -> None:
        block = b'{"id":"e1","ts":"2026-01-01T00:00:00Z"}\n'
        block = block + b"\x00" * (BLOCK_SIZE - len(block))
        path = self.write_image(block + b"partial-tail")
        result = reader.read_image(path, **self.kw)
        self.assertEqual(len(result.blocks), 1)
        self.assertEqual(len(result.records), 1)
        kinds = [a.kind for a in result.anomalies]
        self.assertIn("truncated_image", kinds)

    def test_undecodable_record_counted_others_kept(self) -> None:
        good = b'{"id":"e1","ts":"2026-01-01T00:00:00Z"}\n'
        bad = b"\xff\xfe not utf-8\n"
        block = good + bad
        block = block + b"\x00" * (BLOCK_SIZE - len(block))
        result = reader.read_image(self.write_image(block), **self.kw)
        self.assertEqual(len(result.records), 1)
        kinds = [a.kind for a in result.anomalies]
        self.assertIn("undecodable_record", kinds)

    def test_malformed_json_record_rejected_others_kept(self) -> None:
        good = b'{"id":"e1","ts":"2026-01-01T00:00:00Z"}\n'
        bad = b'{"id":"e2","ts":\n'
        block = good + bad
        block = block + b"\x00" * (BLOCK_SIZE - len(block))
        result = reader.read_image(self.write_image(block), **self.kw)
        self.assertEqual(len(result.records), 1)
        kinds = [a.kind for a in result.anomalies]
        self.assertIn("rejected_record", kinds)

    def test_record_missing_fields_rejected(self) -> None:
        block = b'{"id": 42, "ts": null}\n'
        block = block + b"\x00" * (BLOCK_SIZE - len(block))
        result = reader.read_image(self.write_image(block), **self.kw)
        self.assertEqual(len(result.records), 0)
        self.assertEqual(result.anomalies[0].kind, "rejected_record")

    def test_non_regular_file_refused(self) -> None:
        with self.assertRaises(reader.ImageReadError):
            reader.read_image(self.dir, **self.kw)  # a directory, not a file

    def test_block_count_limit_enforced(self) -> None:
        path = self.write_image(b"\x00" * BLOCK_SIZE * 3)
        with self.assertRaises(reader.ImageReadError):
            reader.read_image(path, **{**self.kw, "max_blocks": 2})

    def test_manifest_without_sample_tag_refused(self) -> None:
        path = self.dir / "manifest.json"
        path.write_text(json.dumps({"block_size": BLOCK_SIZE}), encoding="utf-8")
        with self.assertRaises(reader.ImageReadError) as ctx:
            reader.load_manifest(path)
        self.assertIn("refusing external evidence", str(ctx.exception))


class TimelineEdgeTest(unittest.TestCase):
    def test_duplicates_kept_and_flagged(self) -> None:
        t = datetime(2026, 1, 1, tzinfo=timezone.utc)
        events = [
            make_event("a", t, seq=0),
            make_event("a", t, seq=1),  # same id
            make_event("b", t, seq=2),  # same content, different id
        ]
        ordered = timeline.sort_and_flag(events)
        self.assertEqual(len(ordered), 3)
        self.assertIsNone(ordered[0].duplicate_of)
        self.assertEqual(ordered[1].duplicate_of, "a")
        self.assertEqual(ordered[2].duplicate_of, "a")

    def test_gap_exactly_at_threshold_not_reported(self) -> None:
        start = datetime(2026, 1, 1, tzinfo=timezone.utc)
        events = [make_event("a", start + timedelta(seconds=900), seq=0)]
        gaps = timeline.find_gaps(events, start, start + timedelta(seconds=1800), 900)
        self.assertEqual(gaps, [])

    def test_gap_above_threshold_reported(self) -> None:
        start = datetime(2026, 1, 1, tzinfo=timezone.utc)
        events = [make_event("a", start + timedelta(seconds=901), seq=0)]
        gaps = timeline.find_gaps(events, start, start + timedelta(seconds=1800), 900)
        self.assertEqual(len(gaps), 1)
        self.assertEqual(gaps[0].kind, "head")
        self.assertEqual(gaps[0].duration_seconds, 901)

    def test_head_and_tail_boundaries(self) -> None:
        start = datetime(2026, 1, 1, tzinfo=timezone.utc)
        events = [make_event("a", start + timedelta(seconds=2000), seq=0)]
        gaps = timeline.find_gaps(events, start, start + timedelta(seconds=5000), 900)
        self.assertEqual([g.kind for g in gaps], ["head", "tail"])


class StoreEdgeTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.db = Path(self._tmp.name) / "runs.sqlite"

    def minimal_batch(self) -> store.BatchData:
        return store.BatchData(
            batch_id=store.new_batch_id(), started_utc=store.utc_now_iso(),
            status="completed", config={}, inputs={},
        )

    def test_error_mapping(self) -> None:
        self.assertIsInstance(
            store._map_operational_error(
                sqlite3.OperationalError("database or disk is full")),
            store.DiskFullError)
        self.assertIsInstance(
            store._map_operational_error(
                sqlite3.OperationalError("database is locked")),
            store.DatabaseLockedError)
        self.assertIsInstance(
            store._map_operational_error(sqlite3.OperationalError("boom")),
            store.StoreError)

    def test_failed_batch_rolls_back(self) -> None:
        conn = store.connect(self.db)
        try:
            batch = self.minimal_batch()
            batch.events = [make_event("a", datetime(2026, 1, 1, tzinfo=timezone.utc))]
            # Pre-occupy the events primary key so the batch fails *after*
            # the batches-row insert; the whole batch must roll back.
            conn.execute(
                "INSERT INTO events VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                (batch.batch_id, 0, "x", "", "", 0, "", "", 0, "low", "t", "d", 0, 0, None))
            conn.commit()
            with self.assertRaises(store.StoreError):
                store.save_batch(conn, batch)
            n = conn.execute(
                "SELECT COUNT(*) FROM batches WHERE batch_id=?",
                (batch.batch_id,)).fetchone()[0]
            self.assertEqual(n, 0)  # batches row rolled back
        finally:
            conn.close()


class CliErrorTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.dir = Path(self._tmp.name)

    def run_cli(self, argv: list[str]) -> tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            rc = cli.main(argv)
        return rc, out.getvalue(), err.getvalue()

    def test_missing_timezone_is_config_error(self) -> None:
        rc, _, err = self.run_cli(["--self-check"])
        self.assertEqual(rc, 2)
        self.assertIn("--timezone is required", err)

    def test_bad_timezone_is_config_error(self) -> None:
        rc, _, err = self.run_cli(["--self-check", "--timezone", "Mars/Olympus"])
        self.assertEqual(rc, 2)
        self.assertIn("unknown IANA timezone", err)

    def test_missing_image_is_image_error(self) -> None:
        rc, _, err = self.run_cli([
            "--timezone", "Asia/Shanghai",
            "--image", str(self.dir / "nope.bin"),
            "--manifest", str(self.dir / "nope.json"),
        ])
        self.assertEqual(rc, 3)
        self.assertIn("error=image", err)

    def test_foreign_manifest_refused(self) -> None:
        manifest = self.dir / "m.json"
        manifest.write_text(json.dumps({"format": "something-else"}), encoding="utf-8")
        image = self.dir / "i.bin"
        image.write_bytes(b"\x00" * BLOCK_SIZE)
        rc, _, err = self.run_cli([
            "--timezone", "Asia/Shanghai",
            "--image", str(image), "--manifest", str(manifest),
        ])
        self.assertEqual(rc, 3)
        self.assertIn("refusing external evidence", err)

    def test_locked_database_exit_code(self) -> None:
        db = self.dir / "locked.sqlite"
        holder = sqlite3.connect(db)
        holder.execute("BEGIN EXCLUSIVE")
        holder.execute("CREATE TABLE IF NOT EXISTS hold (x INTEGER)")
        try:
            rc, _, err = self.run_cli([
                "--self-check", "--timezone", "Asia/Shanghai",
                "--db", str(db),
            ])
            self.assertEqual(rc, 4)
            self.assertIn("error=db_locked", err)
            # report still distinguishes completed vs pending steps
            self.assertIn("store=not_run", err)
            self.assertIn("read=ok", err)
        finally:
            holder.rollback()
            holder.close()


class FallbackTimezoneTest(unittest.TestCase):
    """The built-in rule table must reproduce ZoneInfo DST semantics."""

    def test_fallback_matches_zoneinfo_offsets(self) -> None:
        for name, probe_days in (
            ("America/New_York", ((2026, 3, 8), (2026, 11, 1))),
            ("Europe/Berlin", ((2026, 3, 29), (2026, 10, 25))),
        ):
            zi = ZoneInfo(name)
            fb = clock._RuleBasedTz(name, *clock._FALLBACK_RULES[name])
            for (y, m, d) in probe_days:
                for hour in range(24):
                    naive = datetime(y, m, d, hour, 30)
                    for fold in (0, 1):
                        self.assertEqual(
                            naive.replace(tzinfo=zi, fold=fold).utcoffset(),
                            naive.replace(tzinfo=fb, fold=fold).utcoffset(),
                            (name, naive, fold))

    def test_fallback_dst_anomalies_detected(self) -> None:
        fb = clock._RuleBasedTz(
            "America/New_York", *clock._FALLBACK_RULES["America/New_York"])
        ambiguous = clock.parse_record_time(
            "2026-11-01T01:30:00", fb, "America/New_York")
        nonexistent = clock.parse_record_time(
            "2026-03-08T02:30:00", fb, "America/New_York")
        self.assertEqual(ambiguous.anomaly, "ambiguous_local_time")
        self.assertIsNone(ambiguous.utc)
        self.assertEqual(nonexistent.anomaly, "nonexistent_local_time")
        self.assertIsNone(nonexistent.utc)

    def test_fallback_fixed_offset_zones(self) -> None:
        sh = clock._RuleBasedTz("Asia/Shanghai", *clock._FALLBACK_RULES["Asia/Shanghai"])
        parsed = clock.parse_record_time("2026-06-01T12:00:00", sh, "Asia/Shanghai")
        self.assertEqual(parsed.utc, datetime(2026, 6, 1, 4, tzinfo=timezone.utc))

    def test_resolve_timezone_unknown_still_raises(self) -> None:
        with self.assertRaises(ZoneInfoNotFoundError):
            clock.resolve_timezone("Mars/Olympus")

    def test_resolve_uses_fallback_when_database_missing(self) -> None:
        def boom(name):  # simulate bare Windows without the IANA database
            raise ZoneInfoNotFoundError(f"cannot found {name}")

        with mock.patch.object(clock, "ZoneInfo", boom):
            tz = clock.resolve_timezone("Asia/Shanghai")
        self.assertIsInstance(tz, clock._RuleBasedTz)
        parsed = clock.parse_record_time("2026-06-01T12:00:00", tz, "Asia/Shanghai")
        self.assertEqual(parsed.utc, datetime(2026, 6, 1, 4, tzinfo=timezone.utc))


class SamplegenContainmentTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)

    def test_writes_only_inside_destination(self) -> None:
        dest = Path(self._tmp.name) / "sample-dir"
        spec = samplegen.generate_sample(dest)
        written = {spec.image_path, spec.manifest_path}
        for path in written:
            self.assertEqual(path.parent.resolve(), dest.resolve())
            self.assertTrue(str(path.resolve()).startswith(str(dest.resolve())))

    def test_relative_destination_rejected(self) -> None:
        with self.assertRaises(ValueError):
            samplegen.generate_sample("relative/path")

    def test_destination_that_is_a_file_rejected(self) -> None:
        blocker = Path(self._tmp.name) / "not-a-dir"
        blocker.write_text("x", encoding="utf-8")
        with self.assertRaises((ValueError, OSError)):
            samplegen.generate_sample(blocker)


class IntegrityRobustnessTest(unittest.TestCase):
    def test_truncated_image_blocks_reported_missing_not_raised(self) -> None:
        # Manifest still lists four blocks; only two survived in the image.
        digest_a = hashlib.sha256(b"a" * BLOCK_SIZE).hexdigest()
        digest_b = hashlib.sha256(b"b" * BLOCK_SIZE).hexdigest()
        blocks = [
            BlockInfo(0, 0, BLOCK_SIZE, digest_a),
            BlockInfo(1, BLOCK_SIZE, BLOCK_SIZE, digest_b),
        ]
        manifest_blocks = [
            {"index": 0, "offset": 0, "sha256": digest_a},
            {"index": 1, "offset": BLOCK_SIZE, "sha256": digest_b},
            {"index": 2, "offset": 2 * BLOCK_SIZE, "sha256": "0" * 64},
            {"index": 3, "offset": 3 * BLOCK_SIZE, "sha256": "f" * 64},
        ]
        results = integrity.verify_blocks(blocks, manifest_blocks)
        statuses = {r.block_index: r.status for r in results}
        self.assertEqual(statuses[0], IntegrityStatus.OK)
        self.assertEqual(statuses[1], IntegrityStatus.OK)
        self.assertEqual(statuses[2], IntegrityStatus.MISSING_BLOCK)
        self.assertEqual(statuses[3], IntegrityStatus.MISSING_BLOCK)

    def test_unordered_manifest_indices_handled(self) -> None:
        digest = hashlib.sha256(b"z" * BLOCK_SIZE).hexdigest()
        blocks = [BlockInfo(0, 0, BLOCK_SIZE, digest)]
        results = integrity.verify_blocks(
            blocks, [{"index": 0, "offset": 0, "sha256": digest}])
        self.assertEqual(results[0].status, IntegrityStatus.OK)


class DiskFullCliTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.dir = Path(self._tmp.name)

    def test_disk_full_on_json_report_exit_code_5(self) -> None:
        json_path = self.dir / "report.json"
        real_write_text = Path.write_text

        def selective_write(self, data, *args, **kwargs):
            if self == json_path:
                raise OSError(errno.ENOSPC, "No space left on device")
            return real_write_text(self, data, *args, **kwargs)

        with mock.patch.object(Path, "write_text", selective_write):
            out, err = io.StringIO(), io.StringIO()
            with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
                rc = cli.main([
                    "--self-check", "--timezone", "Asia/Shanghai",
                    "--json", str(json_path),
                ])
        err_text = err.getvalue()
        self.assertEqual(rc, 5)
        self.assertIn("error=disk_full", err_text)
        # analysis steps completed; only the report write failed
        self.assertIn("store=ok", err_text)


if __name__ == "__main__":
    unittest.main()
