"""End-to-end and unit tests for the medcheck pipeline."""

from __future__ import annotations

import contextlib
import importlib.util
import io
import json
import os
import shutil
import sqlite3
import struct
import sys
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(ROOT, "src"))

from medcheck import cli  # noqa: E402
from medcheck.compare import match_tracks  # noqa: E402
from medcheck.probe import probe_file  # noqa: E402
from medcheck.riff import parse_wav  # noqa: E402


def _load_make_samples():
    spec = importlib.util.spec_from_file_location(
        "make_samples", os.path.join(ROOT, "scripts", "make_samples.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


make_samples = _load_make_samples()


class PipelineTestCase(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="medcheck-test-")
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.samples = os.path.join(self.tmp, "samples")
        make_samples.make_sample_set(self.samples)
        self.db = os.path.join(self.tmp, "index.sqlite3")
        self.json_path = os.path.join(self.tmp, "report.json")

    def run_scan(self, *paths, extra=()):
        out = io.StringIO()
        argv = ["scan", *paths, "--db", self.db, "--json", self.json_path,
                "--tolerance-ms", "20", *extra]
        with contextlib.redirect_stdout(out):
            code = cli.main(argv)
        return code, out.getvalue()


class TestEndToEnd(PipelineTestCase):
    def test_scan_sample_set(self):
        code, output = self.run_scan(self.samples)
        self.assertEqual(code, 1)  # inconsistencies found
        self.assertIn("files=3", output)
        self.assertIn("tracks=4", output)
        self.assertIn("errors=0", output)
        with open(self.json_path, encoding="utf-8") as fh:
            report = json.load(fh)
        for field in ("files", "track_count", "duration_delta_ms",
                      "codec_summary", "mismatches", "errors"):
            self.assertIn(field, report)
        self.assertEqual(len(report["files"]), 3)
        self.assertEqual(report["track_count"], 4)
        self.assertGreaterEqual(len(report["mismatches"]), 1)
        self.assertEqual(report["errors"], [])
        self.assertGreater(report["duration_delta_ms"], 1)
        self.assertLess(report["duration_delta_ms"], 1000)
        self.assertTrue(report["codec_summary"])

    def test_clean_files_exit_zero(self):
        clean = os.path.join(self.tmp, "clean")
        os.makedirs(clean)
        for name in ("clean_stereo.wav", "clean_mono.wav"):
            shutil.copy(os.path.join(self.samples, name), clean)
        code, _ = self.run_scan(clean)
        self.assertEqual(code, 0)

    def test_cache_reuse_across_runs(self):
        self.assertEqual(self.run_scan(self.samples)[0], 1)
        self.assertEqual(self.run_scan(self.samples)[0], 1)
        conn = sqlite3.connect(self.db)
        try:
            batches = conn.execute("SELECT COUNT(*) FROM batches").fetchone()[0]
            cached = conn.execute(
                "SELECT COUNT(*) FROM probe_cache").fetchone()[0]
        finally:
            conn.close()
        self.assertEqual(batches, 2)
        self.assertEqual(cached, 3)  # one entry per unique SHA-256

    def test_extension_mismatch_still_probed(self):
        odd = os.path.join(self.tmp, "clip.bin")
        shutil.copy(os.path.join(self.samples, "clean_stereo.wav"), odd)
        code, output = self.run_scan(odd)
        self.assertEqual(code, 0)
        self.assertIn("format=wav", output)

    def test_corrupt_file_isolated_from_batch(self):
        bad = os.path.join(self.samples, "broken.wav")
        with open(bad, "wb") as fh:
            fh.write(b"RIFF\x10\x00\x00\x00WAVEfmt ")  # truncated
        code, output = self.run_scan(self.samples)
        self.assertEqual(code, 1)
        summary = output.strip().splitlines()[-1]
        self.assertIn("files=4", summary)
        self.assertNotIn("errors=0", summary)  # file-level error reported

    def test_missing_path_is_usage_error(self):
        out = io.StringIO()
        with contextlib.redirect_stderr(out):
            code = cli.main(["scan", os.path.join(self.tmp, "nope")])
        self.assertEqual(code, 2)


class TestParsers(PipelineTestCase):
    def test_wav_chunk_out_of_bounds(self):
        path = os.path.join(self.tmp, "bad.wav")
        fmt = struct.pack("<HHIIHH", 1, 2, 48000, 192000, 4, 16)
        with open(path, "wb") as fh:
            fh.write(b"RIFF" + struct.pack("<I", 100) + b"WAVE")
            fh.write(b"fmt " + struct.pack("<I", len(fmt)) + fmt)
            fh.write(b"data" + struct.pack("<I", 10 ** 6) + b"\0" * 16)
        info = parse_wav(path)
        self.assertTrue(any("out of bounds" in e for e in info.errors))

    def test_wav_conflicting_fmt_chunks(self):
        path = os.path.join(self.tmp, "conflict.wav")
        fmt1 = struct.pack("<HHIIHH", 1, 2, 48000, 192000, 4, 16)
        fmt2 = struct.pack("<HHIIHH", 1, 1, 44100, 88200, 2, 16)
        payload = (b"WAVE"
                   + b"fmt " + struct.pack("<I", 16) + fmt1
                   + b"fmt " + struct.pack("<I", 16) + fmt2
                   + b"data" + struct.pack("<I", 4) + b"\0" * 4)
        with open(path, "wb") as fh:
            fh.write(b"RIFF" + struct.pack("<I", len(payload)) + payload)
        info = parse_wav(path)
        self.assertTrue(any("conflicting fmt" in e for e in info.errors))

    def test_mov_box_beyond_file_aborts(self):
        path = os.path.join(self.tmp, "bad.mov")
        with open(path, "wb") as fh:
            fh.write(struct.pack(">I4s", 16, b"ftyp") + b"qt  \0\0\0\0")
            fh.write(struct.pack(">I4s", 10 ** 9, b"moov"))
        record = probe_file(path)
        self.assertTrue(record["errors"])
        self.assertEqual(record["tracks"], [])

    def test_mov_missing_language_flagged(self):
        path = os.path.join(self.tmp, "nolang.mov")
        make_samples.write_mov(path, traks=[
            make_samples._audio_trak(1, movie_duration=1000,
                                     media_timescale=48000,
                                     media_duration=48000, channels=2,
                                     rate=48000, lang="und"),
        ], movie_duration=1000)
        record = probe_file(path)
        from medcheck.compare import check_file
        mismatches, _ = check_file(record, 20.0)
        self.assertTrue(any(m["kind"] == "missing_language"
                            for m in mismatches))

    def test_match_tracks_ambiguity(self):
        ref = [{"kind": "audio", "codec": "mp4a", "duration_ms": 1000.0}]
        cands = [{"kind": "audio", "codec": "mp4a", "duration_ms": 1000.0},
                 {"kind": "audio", "codec": "mp4a", "duration_ms": 1005.0}]
        pairs, ambiguous = match_tracks(ref, cands, 20.0)
        self.assertEqual(pairs, [])
        self.assertEqual(ambiguous, [0])
        # Order-independent: a unique candidate matches regardless of order.
        pairs, ambiguous = match_tracks(ref, [cands[1], {"kind": "video",
                                                         "codec": "avc1",
                                                         "duration_ms": 1}],
                                        20.0)
        self.assertEqual(pairs, [(0, 0)])
        self.assertEqual(ambiguous, [])


if __name__ == "__main__":
    unittest.main()
