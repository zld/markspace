"""Batch reuse of the detector, SQLite caching/rollback and CLI contracts."""

from __future__ import annotations

import json
import os
import sqlite3
import subprocess
import sys
import tempfile
import unittest

import numpy as np

from audioslice.batch import collect_wav_paths, run_batch
from audioslice.detector import DetectorConfig
from audioslice.errors import InputFileError, StoreError
from audioslice.store import Store, fingerprint, make_cache_key
from audioslice.wav import write_pcm_wav

SR = 48000
REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def make_wav(path: str, *, length_seconds: float = 1.0, active: bool = True) -> None:
    frames = int(SR * length_seconds)
    signal = np.zeros(frames)
    if active:
        signal[frames // 4:3 * frames // 4] = 3000.0
    write_pcm_wav(path, signal, SR, 2)


class BatchTest(unittest.TestCase):
    def setUp(self) -> None:
        self.dir = tempfile.mkdtemp()
        self.cfg = DetectorConfig(sample_rate=SR, window_ms=20, min_silence_ms=120)

    def test_recursive_collection_sorted_and_deduped(self) -> None:
        sub = os.path.join(self.dir, "nested")
        os.makedirs(sub)
        a = os.path.join(self.dir, "a.wav")
        b = os.path.join(sub, "b.wav")
        make_wav(a)
        make_wav(b)
        open(os.path.join(self.dir, "notes.txt"), "w").write("ignore me")
        paths = collect_wav_paths([self.dir])
        self.assertEqual(paths, sorted([os.path.abspath(a), os.path.abspath(b)]))
        # Same file passed twice is normalised to one entry.
        self.assertEqual(collect_wav_paths([a, os.path.abspath(a)]), [os.path.abspath(a)])
        with self.assertRaises(InputFileError):
            collect_wav_paths([os.path.join(self.dir, "ghost.wav")])

    def test_single_failure_does_not_block_other_files(self) -> None:
        good = os.path.join(self.dir, "good.wav")
        bad = os.path.join(self.dir, "bad.wav")
        make_wav(good)
        with open(bad, "wb") as fh:
            fh.write(b"RIFFxxxxWAVEbroken")
        summary = run_batch(collect_wav_paths([self.dir]), self.cfg)
        self.assertEqual(summary.files_ok, 1)
        self.assertEqual(summary.files_failed, 1)
        self.assertEqual(summary.files_skipped, 0)
        failed = next(o for o in summary.outcomes if o.status == "failed")
        self.assertEqual(failed.path, os.path.abspath(bad))
        self.assertTrue(failed.reason)

    def test_duplicate_path_in_batch_is_skipped(self) -> None:
        good = os.path.join(self.dir, "good.wav")
        make_wav(good)
        paths = collect_wav_paths([good])
        summary = run_batch(paths + paths, self.cfg)
        self.assertEqual(summary.files_ok, 1)
        self.assertEqual(summary.files_skipped, 1)


class StoreTest(unittest.TestCase):
    def setUp(self) -> None:
        self.dir = tempfile.mkdtemp()
        self.wav = os.path.join(self.dir, "a.wav")
        make_wav(self.wav)
        self.db = os.path.join(self.dir, "cache.sqlite3")
        self.cfg = DetectorConfig(sample_rate=SR, window_ms=20, min_silence_ms=120)

    def _result(self):
        from audioslice.detector import detect_samples
        signal = np.zeros(SR)
        signal[SR // 4:3 * SR // 4] = 3000.0
        return detect_samples(signal, self.cfg)

    def test_cache_key_changes_with_params_and_file(self) -> None:
        fp = fingerprint(self.wav)
        key1 = make_cache_key(fp, self.cfg.param_signature())
        key2 = make_cache_key(fp, {**self.cfg.param_signature(), "window_ms": 30.0})
        self.assertNotEqual(key1, key2)
        other = os.path.join(self.dir, "b.wav")
        make_wav(other, length_seconds=2.0)
        self.assertNotEqual(key1, make_cache_key(fingerprint(other), self.cfg.param_signature()))

    def test_repeat_run_does_not_duplicate_rows(self) -> None:
        result = self._result()
        fp = fingerprint(self.wav)
        key = make_cache_key(fp, self.cfg.param_signature())
        with Store(self.db) as store:
            self.assertTrue(store.save_result(key, fp, self.cfg.param_signature(), result))
            self.assertFalse(store.save_result(key, fp, self.cfg.param_signature(), result))
            self.assertEqual(store.get_segments(key), list(result.segments))
        with sqlite3.connect(self.db) as conn:
            self.assertEqual(conn.execute("SELECT COUNT(*) FROM results").fetchone()[0], 1)
            self.assertEqual(
                conn.execute("SELECT COUNT(*) FROM segments WHERE kind='segment'").fetchone()[0],
                len(result.segments),
            )

    def test_failed_write_rolls_back_without_half_segments(self) -> None:
        result = self._result()
        fp = fingerprint(self.wav)
        with Store(self.db) as store:
            good_key = make_cache_key(fp, self.cfg.param_signature())
            store.save_result(good_key, fp, self.cfg.param_signature(), result)
            bad_key = "f" * 64
            with self.assertRaises(sqlite3.Error):
                with store._conn:
                    store._conn.execute(
                        "INSERT INTO results (cache_key, result_version, path, file_size,"
                        " mtime_ns, params_json, sample_rate, total_frames,"
                        " silence_total_seconds) VALUES (?,?,?,?,?,?,?,?,?)",
                        (bad_key, 1, fp.path, fp.size, fp.mtime_ns, "{}",
                         SR, result.total_frames, 0.0))
                    # Invalid kind violates the CHECK constraint -> rollback.
                    store._conn.execute(
                        "INSERT INTO segments VALUES (?, 'nope', 0, 0, 1)", (bad_key,))
            self.assertFalse(store.has_result(bad_key))
            orphan_rows = store._conn.execute(
                "SELECT COUNT(*) FROM segments WHERE cache_key=?", (bad_key,)
            ).fetchone()[0]
            self.assertEqual(orphan_rows, 0)
            # Pre-existing committed result is untouched.
            self.assertTrue(store.has_result(good_key))

    def test_locked_and_corrupt_databases_raise_store_error(self) -> None:
        locked = os.path.join(self.dir, "locked.sqlite3")
        holder = sqlite3.connect(locked)
        holder.execute("CREATE TABLE t(x)")
        holder.execute("BEGIN EXCLUSIVE")
        try:
            with self.assertRaises(StoreError):
                Store(locked)
        finally:
            holder.rollback()
            holder.close()
        corrupt = os.path.join(self.dir, "corrupt.sqlite3")
        with open(corrupt, "wb") as fh:
            fh.write(b"not a database" * 16)
        with self.assertRaises(StoreError):
            Store(corrupt)

    def test_batch_uses_store_and_reuses_detector(self) -> None:
        with Store(self.db) as store:
            first = run_batch(collect_wav_paths([self.wav]), self.cfg, store)
            self.assertEqual(first.outcomes[0].status, "ok")
            second = run_batch(collect_wav_paths([self.wav]), self.cfg, store)
            self.assertEqual(second.outcomes[0].status, "cached")
            # Identical segmentation either way proves detector reuse.
            self.assertEqual(
                [s.as_dict() for s in first.outcomes[0].result.segments],
                [s.as_dict() for s in second.outcomes[0].result.segments],
            )
        with sqlite3.connect(self.db) as conn:
            self.assertEqual(conn.execute("SELECT COUNT(*) FROM results").fetchone()[0], 1)


class CliAcceptanceTest(unittest.TestCase):
    def setUp(self) -> None:
        self.dir = tempfile.mkdtemp()
        self.fixture = os.path.join(self.dir, "fixture.wav")
        script = os.path.join(REPO_ROOT, "scripts", "make_fixture.py")
        subprocess.run([sys.executable, script, "--out", self.fixture, "--seed", "17"],
                       check=True, capture_output=True, text=True)

    def run_cli(self, *extra: str) -> subprocess.CompletedProcess:
        return subprocess.run(
            [sys.executable, "-m", "audioslice.cli", "analyze", self.fixture, *extra],
            capture_output=True, text=True, cwd=self.dir,
        )

    def test_acceptance_json_contract(self) -> None:
        proc = self.run_cli("--window-ms", "20", "--noise-margin-db", "8",
                            "--min-silence-ms", "120", "--json")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        payload = json.loads(proc.stdout)
        self.assertEqual(payload["files_ok"], 1)
        self.assertEqual(payload["files_failed"], 0)
        self.assertEqual(payload["segment_count"], 2)
        self.assertEqual(payload["segments"], [
            {"start_sample": 4800, "end_sample": 19200},
            {"start_sample": 24000, "end_sample": 38400},
        ])
        self.assertEqual(payload["silence_total_seconds"], 0.22)

    def test_exit_codes(self) -> None:
        bad = os.path.join(self.dir, "bad.wav")
        with open(bad, "wb") as fh:
            fh.write(b"junk")
        mixed = subprocess.run(
            [sys.executable, "-m", "audioslice.cli", "analyze", bad, self.fixture,
             "--no-db"], cwd=self.dir, capture_output=True, text=True)
        self.assertEqual(mixed.returncode, 2)
        bad_param = self.run_cli("--window-ms", "0")
        self.assertEqual(bad_param.returncode, 1)


if __name__ == "__main__":
    unittest.main()
