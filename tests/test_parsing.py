"""End-to-end and unit coverage for parsing, comparison and storage."""

from __future__ import annotations

import contextlib
import io
import json
import sqlite3
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))
sys.path.insert(0, str(ROOT / "scripts"))

import make_samples  # noqa: E402
from mediacheck import cli  # noqa: E402
from mediacheck.boxes import parse_box_tree  # noqa: E402
from mediacheck.compare import compare_containers  # noqa: E402
from mediacheck.model import SniffResult, Track  # noqa: E402
from mediacheck.sniff import sniff_file  # noqa: E402
from mediacheck.store import Store, StoreError  # noqa: E402


def run_cli(*argv: str) -> tuple[int, str]:
    stdout, stderr = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
        try:
            code = cli.main(list(argv))
        except SystemExit as exc:  # argparse usage errors
            code = int(exc.code or 0)
    return code, stdout.getvalue() + stderr.getvalue()


class SampleTree(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.tmp = Path(self._tmp.name)
        self.samples = self.tmp / "samples"
        make_samples.create_samples(self.samples)

    def tearDown(self) -> None:
        self._tmp.cleanup()


class WavParsingTests(SampleTree):
    def test_fields_and_unknown_chunks_skipped(self) -> None:
        result = sniff_file(self.samples / "params.wav")
        self.assertIsNone(result.error)
        self.assertEqual(result.container, "wav")
        self.assertIn("JUNK", result.metadata["chunks"])
        self.assertIn("LIST", result.metadata["chunks"])
        track = result.tracks[0]
        self.assertEqual(track.sample_rate, 44100)
        self.assertEqual(track.channels, 1)
        self.assertEqual(track.bit_depth, 16)
        self.assertEqual(track.block_align, 2)
        self.assertAlmostEqual(track.duration_ms, 1000.0, places=3)

    def test_missing_fmt_is_reported(self) -> None:
        path = self.tmp / "fmtless.wav"
        body = b"WAVE" + b"data" + (0).to_bytes(4, "little")
        path.write_bytes(b"RIFF" + len(body).to_bytes(4, "little") + body)
        result = sniff_file(path)
        self.assertIsNotNone(result.error)
        self.assertIn("fmt", result.error)

    def test_chunk_overrun_marks_truncated(self) -> None:
        data = bytearray(make_samples.build_params_wav())
        data[40:44] = (10_000_000).to_bytes(4, "little")  # inflate data size
        path = self.tmp / "bad.wav"
        path.write_bytes(bytes(data))
        result = sniff_file(path)
        self.assertTrue(result.truncated)
        self.assertTrue(any("declares" in w for w in result.warnings))


class Mp4ParsingTests(SampleTree):
    def test_multitrack_fields(self) -> None:
        result = sniff_file(self.samples / "reordered.mp4")
        self.assertIsNone(result.error)
        self.assertEqual(result.container, "mp4")
        self.assertEqual(len(result.tracks), 2)
        kinds = {t.kind: t for t in result.tracks}
        self.assertEqual(kinds["audio"].language, "eng")
        self.assertEqual(kinds["audio"].sample_rate, 48000)
        self.assertEqual(kinds["audio"].channels, 2)
        self.assertEqual(kinds["audio"].codec, "mp4a")
        self.assertEqual(kinds["video"].codec, "avc1")
        self.assertAlmostEqual(kinds["video"].duration_ms, 3000.0, places=3)

    def test_truncated_mdat_is_located(self) -> None:
        result = sniff_file(self.samples / "truncated.mp4")
        self.assertTrue(result.truncated)
        self.assertTrue(result.warnings)
        self.assertTrue(any("offset" in w for w in result.warnings))

    def test_large_size_header(self) -> None:
        payload = b"free"
        box = (1).to_bytes(4, "big") + payload + (16).to_bytes(8, "big")
        path = self.tmp / "large.mp4"
        path.write_bytes(box)
        with open(path, "rb") as handle:
            tree = parse_box_tree(handle, path.stat().st_size)
        self.assertEqual(tree.boxes[0].type, "free")
        self.assertEqual(tree.boxes[0].header_size, 16)

    def test_depth_limit(self) -> None:
        inner = b""
        for _ in range(20):
            inner = (8 + len(inner)).to_bytes(4, "big") + b"moov" + inner
        path = self.tmp / "deep.mp4"
        path.write_bytes(inner)
        result = sniff_file(path)
        self.assertIsNotNone(result.error)
        self.assertIn("depth", result.error)


if __name__ == "__main__":
    unittest.main()
