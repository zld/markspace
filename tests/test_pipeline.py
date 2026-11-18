"""End-to-end batch tests over generated samples plus failure paths."""

from __future__ import annotations

import json
import unittest

import numpy as np

from audiosplit.audio_io import AudioError, read_wav
from audiosplit.config import DetectionConfig
from audiosplit.store import Store
from support import SR, Harness, base_args, make_samples, run_cli, write_wav


class SampleBatchTests(Harness):
    def test_generated_samples_end_to_end(self) -> None:
        samples = self.tmp / "samples"
        make_samples.generate(samples)
        export = self.tmp / "export"
        code, out, err = run_cli(
            ["batch", "--input", str(samples), "--export", str(export)]
            + base_args(self.tmp)
        )
        self.assertEqual(code, 0, err)
        self.assertIn("files_ok 4", out)
        self.assertIn("files_failed 0", out)
        total = int(out.split("segments_total ")[1].splitlines()[0])
        self.assertGreaterEqual(total, 4)
        self.assertLessEqual(total, 8)
        silence = float(out.split("silence_total_seconds ")[1].splitlines()[0])
        duration = float(out.split("duration_total_seconds ")[1].splitlines()[0])
        self.assertGreater(silence, 0.0)
        self.assertLess(silence, duration)
        self.assertIn("start_sample=", out)
        self.assertIn("end_sample=", out)
        self.assertTrue((export / "index.json").is_file())
        self.assertTrue((export / "events" / "manifest.json").is_file())
        self.assertEqual(len(list(export.glob("events/*.wav"))), 4)

    def test_database_partition_and_manifest(self) -> None:
        samples = self.tmp / "samples"
        make_samples.generate(samples)
        export = self.tmp / "export"
        run_cli(
            ["batch", "--input", str(samples), "--export", str(export)]
            + base_args(self.tmp)
        )
        with Store(self.tmp / "audio.sqlite") as store:
            rows = store.conn.execute("SELECT * FROM runs").fetchall()
            self.assertGreaterEqual(len(rows), 4)
            for row in rows:
                segments = store.load_segments(row["id"])
                silences = store.load_silences(row["id"])
                covered = [(s.start_sample, s.end_sample) for s in segments]
                covered += silences
                covered.sort()
                self.assertEqual(covered[0][0], 0)
                self.assertEqual(covered[-1][1], row["total_samples"])
                for left, right in zip(covered, covered[1:]):
                    self.assertLessEqual(left[1], right[0])
                self.assertEqual(
                    sum(end - start for start, end in silences),
                    row["silence_samples"],
                )
                self.assertEqual(len(segments), row["segment_count"])
        manifest = json.loads((export / "events" / "manifest.json").read_text())
        self.assertEqual(manifest["segment_count"], len(manifest["segments"]))
        self.assertEqual(
            manifest["silence_samples"],
            sum(item["length_samples"] for item in manifest["silences"]),
        )
        self.assertEqual(manifest["config"]["frame_ms"], 10.0)


class DurableRunTests(Harness):
    def test_duplicate_skip_and_new(self) -> None:
        samples = self.tmp / "samples"
        make_samples.generate(samples)
        args = ["batch", "--input", str(samples)] + base_args(self.tmp)
        run_cli(args)
        code, out, _ = run_cli(args)
        self.assertEqual(code, 0)
        self.assertIn("files_skipped 4", out)
        code, out, _ = run_cli(args + ["--on-duplicate", "new"])
        self.assertEqual(code, 0)
        self.assertNotIn("files_skipped", out)
        with Store(self.tmp / "audio.sqlite") as store:
            count = store.conn.execute("SELECT COUNT(*) FROM runs").fetchone()[0]
            distinct = store.conn.execute(
                "SELECT COUNT(DISTINCT fingerprint) FROM runs"
            ).fetchone()[0]
        self.assertEqual(count, 8)
        self.assertEqual(distinct, 4)

    def test_corrupt_file_continues_and_symlink_skipped(self) -> None:
        samples = self.tmp / "samples"
        samples.mkdir()
        good = np.zeros(SR, dtype=np.float64)
        good[SR // 2 : SR // 2 + 2205] = 0.3
        write_wav(samples / "good.wav", good)
        (samples / "broken.wav").write_bytes(b"RIFF\x00\x00\x00\x00WAVEjunk")
        (samples / "link.wav").symlink_to(samples / "good.wav")
        code, out, _ = run_cli(["batch", "--input", str(samples)] + base_args(self.tmp))
        self.assertEqual(code, 1)
        self.assertIn("files_failed 1", out)
        self.assertIn("files_ok 1", out)
        self.assertIn("broken.wav", out)
        self.assertNotIn("link.wav", out)

    def test_invalid_parameters_reject_batch(self) -> None:
        samples = self.tmp / "samples"
        make_samples.generate(samples)
        bad = ["batch", "--input", str(samples), "--db", str(self.tmp / "x.sqlite")]
        cases = [
            ["--frame-ms", "0"],
            ["--noise-percentile", "100"],
            ["--margin-db", "nan"],
            ["--onset-frames", "0"],
            ["--merge-ms", "-1"],
        ]
        for extra in cases:
            code, _, err = run_cli(bad + extra)
            self.assertEqual(code, 2, extra)
            self.assertIn("error:", err)
        self.assertFalse((self.tmp / "x.sqlite").exists())

    def test_database_error_exit_code_two(self) -> None:
        samples = self.tmp / "samples"
        make_samples.generate(samples)
        db_dir = self.tmp / "dbdir"
        db_dir.mkdir()
        code, _, err = run_cli(["batch", "--input", str(samples), "--db", str(db_dir)])
        self.assertEqual(code, 2)
        self.assertIn("database", err)

    def test_sample_rate_boundaries(self) -> None:
        path = self.tmp / "low.wav"
        write_wav(path, np.zeros(1000), rate=4000)
        with self.assertRaises(AudioError):
            read_wav(path, DetectionConfig())
        config = DetectionConfig(min_sample_rate=4000)
        config.validate()
        self.assertEqual(read_wav(path, config).sample_rate, 4000)


if __name__ == "__main__":
    unittest.main()
