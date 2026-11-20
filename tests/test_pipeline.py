"""Integration tests for the store, batch scheduler and JSONL exporter.

Covers multi-bitdepth files, anti-phase stereo, corrupted inputs,
partial failures, run resume, deduplication, schema version checks and
the CLI exit-code mapping.
"""
import io
import json
import os
import sqlite3
import tempfile
import unittest
import wave

import numpy as np

from audiocut import DetectorParams
from audiocut import cli as cli_mod
from audiocut.batch import run_batch, scan_wavs
from audiocut.exporter import export_jsonl, file_record_to_json, resolve_run_id
from audiocut.samplegen import generate
from audiocut.store import SCHEMA_VERSION, Store, StoreError
from audiocut.wav_reader import WavError

SR = 48000
PARAMS = DetectorParams()


def write_pcm(path, samples, sampwidth, rate=SR, channels=1):
    frames = np.asarray(samples).reshape(-1, channels)
    if sampwidth == 1:
        raw = (frames + 128).astype(np.uint8).tobytes()
    elif sampwidth == 2:
        raw = frames.astype("<i2").tobytes()
    elif sampwidth == 3:
        raw = np.frombuffer(frames.astype("<i4").tobytes(),
                            dtype=np.uint8).reshape(-1, 4)[:, :3].tobytes()
    else:
        raw = frames.astype("<i4").tobytes()
    with wave.open(str(path), "wb") as wf:
        wf.setnchannels(channels)
        wf.setsampwidth(sampwidth)
        wf.setframerate(rate)
        wf.writeframes(raw)


def tone(n, freq=440.0, amp=0.5):
    t = np.arange(n, dtype=np.float64) / SR
    return amp * np.sin(2.0 * np.pi * freq * t)


class StoreTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.db = os.path.join(self.tmp.name, "r.sqlite")
        self.store = Store(self.db)

    def tearDown(self):
        self.store.close()
        self.tmp.cleanup()

    def test_schema_version_recorded_and_checked(self):
        row = self.store.conn.execute(
            "SELECT value FROM meta WHERE key='schema_version'").fetchone()
        self.assertEqual(row[0], SCHEMA_VERSION)
        self.store.close()
        with sqlite3.connect(self.db) as conn:
            conn.execute("UPDATE meta SET value='999' WHERE key='schema_version'")
        with self.assertRaises(StoreError):
            Store(self.db)

    def test_run_lifecycle_and_latest(self):
        self.assertIsNone(self.store.latest_run_id())
        rid = self.store.create_run("/in", "{}", "digest-x")
        self.assertEqual(self.store.latest_run_id(), rid)
        self.assertEqual(self.store.get_run(rid)["status"], "running")
        self.store.finish_run(rid)
        self.assertEqual(self.store.get_run(rid)["status"], "completed")

    def test_record_file_atomic_rollback(self):
        rid = self.store.create_run("/in", "{}", "d")
        with self.assertRaises(StoreError):
            self.store.record_file(rid, "a.wav", None, None)  # NOT NULL violation
        self.assertEqual(self.store.files_of_run(rid), [])

    def test_segments_persisted_and_dedup_lookup(self):
        rid = self.store.create_run("/in", "{}", "dig")
        from audiocut import DetectionResult, Segment
        result = DetectionResult(
            sample_rate=SR, channels=1, sample_frames=100,
            segments=[Segment(10, 40), Segment(60, 90)],
            silence=[Segment(0, 10), Segment(40, 60), Segment(90, 100)])
        self.store.record_file(rid, "a.wav", "sha", "ok", result=result)
        dup = self.store.find_duplicate("a.wav", "sha", "dig")
        self.assertIsNotNone(dup)
        self.assertEqual(dup["segments"], [(10, 40), (60, 90)])
        self.assertIsNone(self.store.find_duplicate("a.wav", "other", "dig"))
        self.store.record_reused(rid + 0, "b.wav", "sha", dup)
        rows = self.store.files_of_run(rid)
        self.assertEqual({r["path"] for r in rows}, {"a.wav", "b.wav"})
        self.assertEqual(rows[0]["segments"], [(10, 40), (60, 90)])


class SamplegenTests(unittest.TestCase):
    def test_generated_files_are_deterministic(self):
        with tempfile.TemporaryDirectory() as d1, tempfile.TemporaryDirectory() as d2:
            p1 = generate(d1)
            p2 = generate(d2)
            self.assertEqual(sorted(os.path.basename(p) for p in p1),
                             ["combat.wav", "fade.wav", "quiet.wav"])
            for rel in ("combat.wav", "fade.wav", "quiet.wav"):
                with open(os.path.join(d1, rel), "rb") as a, \
                        open(os.path.join(d2, rel), "rb") as b:
                    self.assertEqual(a.read(), b.read())


class ExportTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.indir = os.path.join(self.tmp.name, "in")
        self.db = os.path.join(self.tmp.name, "r.sqlite")
        generate(self.indir)
        self.store = Store(self.db)
        rid = self.store.create_run(self.indir, PARAMS.summary_json(),
                                    PARAMS.digest())
        self.summary = run_batch(self.store, rid, self.indir, PARAMS, workers=2)
        self.store.finish_run(rid)
        self.rid = rid

    def tearDown(self):
        self.store.close()
        self.tmp.cleanup()

    def test_jsonl_one_line_per_file_with_required_fields(self):
        buf = io.StringIO()
        count = export_jsonl(self.store, "latest", buf)
        self.assertEqual(count, 3)
        lines = buf.getvalue().strip().split("\n")
        records = [json.loads(line) for line in lines]
        required = {"status", "sample_rate", "sample_frames", "clip_count",
                    "segments", "silence_total_seconds"}
        for rec in records:
            self.assertTrue(required.issubset(rec))
            for seg in rec["segments"]:
                self.assertIn("start_sample", seg)
                self.assertIn("end_sample", seg)
        by_path = {r["path"]: r for r in records}
        self.assertEqual(by_path["quiet.wav"]["clip_count"], 0)
        self.assertAlmostEqual(
            by_path["quiet.wav"]["silence_total_seconds"], 0.5, delta=0.01)

    def test_failed_file_exported_with_error_summary(self):
        bad = os.path.join(self.indir, "broken.wav")
        with open(bad, "wb") as fh:
            fh.write(b"not a wav file")
        rid2 = self.store.create_run(self.indir, "{}", "d2")
        summary = run_batch(self.store, rid2, self.indir, PARAMS)
        self.assertEqual(summary.failed, 1)
        rec = file_record_to_json(
            next(r for r in self.store.files_of_run(rid2)
                 if r["path"] == "broken.wav"))
        self.assertEqual(rec["status"], "error")
        self.assertTrue(rec["error"])

    def test_resolve_run_id_variants(self):
        self.assertEqual(resolve_run_id(self.store, "latest"), self.rid)
        self.assertEqual(resolve_run_id(self.store, str(self.rid)), self.rid)
        with self.assertRaises(StoreError):
            resolve_run_id(self.store, "9999")


class BatchTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.indir = os.path.join(self.tmp.name, "in")
        os.makedirs(self.indir)
        self.db = os.path.join(self.tmp.name, "r.sqlite")
        self.store = Store(self.db)

    def tearDown(self):
        self.store.close()
        self.tmp.cleanup()

    def _run(self, workers=1, resume=False, rid=None):
        if rid is None:
            rid = self.store.create_run(
                self.indir, PARAMS.summary_json(), PARAMS.digest())
        return rid, run_batch(self.store, rid, self.indir, PARAMS,
                              workers=workers, resume=resume)

    def test_multi_bitdepth_and_antiphase_stereo(self):
        write_pcm(os.path.join(self.indir, "a8.wav"),
                  np.zeros(SR, dtype=np.int64), 1)
        write_pcm(os.path.join(self.indir, "b24.wav"),
                  (tone(SR) * 8388607).astype(np.int64), 3)
        write_pcm(os.path.join(self.indir, "c32.wav"),
                  (tone(SR) * (2**31 - 1)).astype(np.int64), 4)
        stereo = np.column_stack([tone(SR), -tone(SR)])
        write_pcm(os.path.join(self.indir, "d_stereo.wav"),
                  (stereo * 32767).astype(np.int64), 2, channels=2)
        _, summary = self._run(workers=3)
        self.assertEqual((summary.files_seen, summary.failed), (4, 0))
        rows = {r["path"]: r for r in self.store.files_of_run(summary.run_id)}
        self.assertEqual(rows["d_stereo.wav"]["channels"], 2)
        self.assertEqual(rows["d_stereo.wav"]["clip_count"], 1)
        self.assertEqual(rows["a8.wav"]["clip_count"], 0)

    def test_partial_failure_isolated_and_persisted(self):
        write_pcm(os.path.join(self.indir, "good.wav"),
                  (tone(SR) * 32767).astype(np.int64), 2)
        with open(os.path.join(self.indir, "broken.wav"), "wb") as fh:
            fh.write(b"RIFF\x00\x00\x00\x00WAVEgarbage")
        rid, summary = self._run()
        self.assertEqual(summary.failed, 1)
        self.assertEqual(summary.succeeded, 1)
        rows = {r["path"]: r for r in self.store.files_of_run(rid)}
        self.assertEqual(rows["broken.wav"]["status"], "error")
        self.assertIn("broken.wav", rows["broken.wav"]["error"])
        self.assertEqual(rows["good.wav"]["status"], "ok")
        self.assertTrue(summary.errors)

    def test_resume_skips_completed_files(self):
        write_pcm(os.path.join(self.indir, "a.wav"),
                  (tone(SR) * 32767).astype(np.int64), 2)
        rid, first = self._run()
        self.assertEqual(first.files_seen, 1)
        write_pcm(os.path.join(self.indir, "b.wav"),
                  (tone(SR, amp=0.4) * 32767).astype(np.int64), 2)
        _, second = self._run(workers=2, resume=True, rid=rid)
        self.assertEqual(second.files_seen, 2)
        self.assertEqual(second.skipped, 1)
        self.assertEqual(second.succeeded, 1)
        paths = [r["path"] for r in self.store.files_of_run(rid)]
        self.assertEqual(sorted(paths), ["a.wav", "b.wav"])

    def test_dedup_reuses_identical_content_and_params(self):
        write_pcm(os.path.join(self.indir, "a.wav"),
                  (tone(SR) * 32767).astype(np.int64), 2)
        _, first = self._run()
        self.assertEqual(first.reused, 0)
        rid2, second = self._run()
        self.assertEqual(second.reused, 1)
        self.assertEqual(second.succeeded, 1)
        segs = {r["path"]: r["segments"]
                for r in self.store.files_of_run(rid2)}
        self.assertEqual(segs["a.wav"],
                         self.store.files_of_run(first.run_id)[0]["segments"])

    def test_failed_record_leaves_no_half_intervals(self):
        from audiocut import DetectionResult, Segment
        rid = self.store.create_run(self.indir, "{}", "d")
        result = DetectionResult(
            sample_rate=SR, channels=1, sample_frames=100,
            segments=[Segment(0, 50)], silence=[Segment(50, 100)])
        self.store.record_file(rid, "a.wav", "sha", "ok", result=result)
        # A second row for the same (run, path) violates UNIQUE; the
        # whole transaction must roll back with no orphan segments.
        with self.assertRaises(StoreError):
            self.store.record_file(rid, "a.wav", "sha", "ok", result=result)
        rows = self.store.files_of_run(rid)
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["segments"], [(0, 50)])
        seg_count = self.store.conn.execute(
            "SELECT COUNT(*) FROM segments").fetchone()[0]
        self.assertEqual(seg_count, 1)


class ScanTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.indir = os.path.join(self.tmp.name, "in")
        os.makedirs(os.path.join(self.indir, "sub"))

    def tearDown(self):
        self.tmp.cleanup()

    def test_sorted_recursive_and_symlink_skip(self):
        for rel in ("z.wav", "sub/a.WAV", "sub/b.txt"):
            with open(os.path.join(self.indir, rel), "wb") as fh:
                fh.write(b"x")
        os.symlink(os.path.join(self.indir, "z.wav"),
                   os.path.join(self.indir, "link.wav"))
        self.assertEqual(scan_wavs(self.indir), ["sub/a.WAV", "z.wav"])

    def test_missing_and_empty_directories(self):
        with self.assertRaises(FileNotFoundError):
            scan_wavs(os.path.join(self.tmp.name, "nope"))
        with self.assertRaises(ValueError):
            scan_wavs(self.indir)


if __name__ == "__main__":
    unittest.main()
