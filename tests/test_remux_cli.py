"""High-level tests: track mapping, sample tables, remux integration, CLI codes.

Covers stable track selection, stts/co64 index rebuild rules, the end-to-end
remux with atomic output handling, and the documented process exit codes::

    0 ok, 2 usage, 3 corrupt input, 4 unsupported, 5 write/validation failure.
"""
from __future__ import annotations

import contextlib
import io
import os
import tempfile
import unittest
from unittest import mock

from mkvmp4 import (
    cli, elements, fixture, index, mapping, matroska, mp4inspect, pipeline,
)


def _track(number, ttype, codec, default=True, private=b"priv"):
    return matroska.Track(number=number, type=ttype, codec_id=codec,
                          default=default, codec_private=private)


class MappingTests(unittest.TestCase):
    def test_stable_order_independent_of_input_order(self):
        tracks = [
            _track(2, matroska.TRACK_TYPE_AUDIO, "A_AAC"),
            _track(4, matroska.TRACK_TYPE_VIDEO, "V_MPEGH/ISO/HEVC", default=False),
            _track(1, matroska.TRACK_TYPE_VIDEO, "V_MPEG4/ISO/AVC"),
        ]
        forward = mapping.build_track_map(tracks)["tracks"]
        reverse = mapping.build_track_map(list(reversed(tracks)))["tracks"]
        order = [t["track_number"] for t in forward]
        self.assertEqual(order, [t["track_number"] for t in reverse])
        self.assertEqual(order[0], 1)  # video sorts before audio
        self.assertEqual([t["mp4_track_id"] for t in forward], [1, 2, 3])

    def test_unsupported_tracks_list_reasons(self):
        tracks = [
            _track(1, matroska.TRACK_TYPE_SUBTITLE, "S_TEXT/UTF8"),
            _track(2, matroska.TRACK_TYPE_VIDEO, "V_MPEG4/ISO/AVC", private=b""),
            _track(3, matroska.TRACK_TYPE_VIDEO, "V_UNKNOWN"),
        ]
        result = mapping.build_track_map(tracks)
        self.assertEqual(result["tracks"], [])
        reasons = {s["track_number"]: s["reason"] for s in result["skipped"]}
        self.assertIn("cannot be carried", reasons[1])
        self.assertIn("parameter sets", reasons[2])
        self.assertIn("not AVC/HEVC/AAC", reasons[3])


class IndexTests(unittest.TestCase):
    def test_rle_and_stts_positive_deltas(self):
        self.assertEqual(index.rle([5, 5, 7, 9, 9]), [[2, 5], [1, 7], [2, 9]])
        self.assertEqual(index.build_stts([10, 10, 20]), [[2, 10], [1, 20]])
        with self.assertRaises(index.IndexBuildError):
            index.build_stts([10, 0])

    def test_ctts_omitted_when_all_zero(self):
        self.assertIsNone(index.build_ctts([0, 0, 0]))
        self.assertEqual(index.build_ctts([0, 5, 5]), [[1, 0], [2, 5]])

    def test_stss_is_one_based(self):
        self.assertEqual(index.build_stss([True, False, True]), [1, 3])

    def test_chunk_offsets_are_contiguous(self):
        offsets, end = index.chunk_offsets([4, 6, 2], base=100)
        self.assertEqual(offsets, [100, 104, 110])
        self.assertEqual(end, 112)

    def test_co64_selection_threshold(self):
        self.assertFalse(index.needs_co64([0, index.UINT32_MAX]))
        self.assertTrue(index.needs_co64([index.UINT32_MAX + 1]))

    def test_validate_rejects_count_and_bounds_mismatch(self):
        good_stts = [[3, 1]]
        with self.assertRaises(index.IndexBuildError):  # stts sum mismatch
            index.validate_tables(3, [[2, 1]], [1, 1, 1], [0, 1, 2], 4, 10)
        with self.assertRaises(index.IndexBuildError):  # offsets past file end
            index.validate_tables(3, good_stts, [1, 1, 1], [0, 1, 2], 99, 10)
        entries = index.validate_tables(
            3, good_stts, [1, 1, 1], [0, 1, 2], end_offset=4, file_size=10)
        self.assertEqual(entries, 1 + 3 + 3)


class RemuxIntegrationTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.src = os.path.join(self.tmp.name, "in.mkv")
        self.dst = os.path.join(self.tmp.name, "out.mp4")
        with open(self.src, "wb") as fh:
            fh.write(fixture.build_fixture())

    def tearDown(self):
        self.tmp.cleanup()

    def test_full_remux_matches_acceptance_shape(self):
        result = pipeline.remux(self.src, self.dst)
        self.assertIn(result["element_count"], range(20, 81))
        self.assertEqual(len(result["track_map"]), 2)
        self.assertGreater(result["sample_count"], 10)
        self.assertTrue(os.path.isfile(self.dst))
        for track in result["tracks"]:
            self.assertLess(abs(track["first_pts_ms"] - 0.0), 1.0)
            self.assertGreater(track["last_pts_ms"], 0.0)
        self.assertFalse(result["co64"])  # tiny file fits 32-bit offsets

    def test_inspect_round_trip_agrees_with_remux_stats(self):
        result = pipeline.remux(self.src, self.dst)
        check = mp4inspect.inspect_mp4(self.dst)
        self.assertEqual(check["format"], "mp4")
        self.assertEqual(check["sample_count"], result["sample_count"])
        self.assertEqual(len(check["tracks"]), len(result["tracks"]))
        for got, want in zip(check["tracks"], result["tracks"]):
            self.assertEqual(got["sample_count"], want["sample_count"])
            self.assertLess(abs(got["last_pts_ms"] - want["last_pts_ms"]), 1.0)

    def test_atomic_replace_preserves_target_and_cleans_temp(self):
        marker = b"PRECIOUS-ORIGINAL"
        with open(self.dst, "wb") as fh:
            fh.write(marker)
        blocked = os.path.join(self.tmp.name, "blocker")
        os.mkdir(blocked)  # final os.replace onto a directory must fail
        with self.assertRaises(pipeline.WriteFailedError):
            pipeline.remux(self.src, blocked)
        self.assertEqual(open(self.dst, "rb").read(), marker)
        leftovers = [n for n in os.listdir(self.tmp.name)
                     if n.startswith(".mkvmp4-")]
        self.assertEqual(leftovers, [])

    def test_input_size_limit_rejected(self):
        with mock.patch.object(pipeline, "MAX_INPUT_BYTES", 1):
            with self.assertRaises(pipeline.UsageError):
                pipeline.remux(self.src, self.dst)

    def test_identical_input_output_rejected(self):
        real = os.path.realpath(self.src)
        with mock.patch.object(pipeline.os.path, "realpath", return_value=real), \
             mock.patch.object(pipeline.os.path, "abspath", return_value=real):
            with self.assertRaises(pipeline.UsageError):
                pipeline.remux(self.src, self.src)


class CliExitCodeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.src = os.path.join(self.tmp.name, "in.mkv")
        with open(self.src, "wb") as fh:
            fh.write(fixture.build_fixture())

    def tearDown(self):
        self.tmp.cleanup()

    def _run(self, *argv):
        err = io.StringIO()
        with contextlib.redirect_stderr(err):
            code = cli.main(list(argv))
        return code, err.getvalue()

    def test_success_and_usage_codes(self):
        out = os.path.join(self.tmp.name, "o.mp4")
        self.assertEqual(cli.main(["remux", self.src, "--output", out]), 0)
        code, _ = self._run("remux", os.path.join(self.tmp.name, "missing"),
                            "--output", os.path.join(self.tmp.name, "x.mp4"))
        self.assertEqual(code, 2)

    def test_corrupt_input_code(self):
        bad = os.path.join(self.tmp.name, "bad.mkv")
        with open(bad, "wb") as fh:
            fh.write(b"\x1a\x45\xdf\xa3\x01junk-junk")
        code, message = self._run("inspect", bad)
        self.assertEqual(code, 3)
        self.assertIn("byte offset", message)

    def test_unsupported_track_code(self):
        only_sub = (fixture._ebml_header() + fixture._eid(elements.ID_SEGMENT)
                    + fixture._UNKNOWN_SIZE_8 + fixture._info()
                    + fixture._el(matroska.ID_TRACKS, fixture._track_entry(
                        9, 1, matroska.TRACK_TYPE_SUBTITLE,
                        "S_TEXT/UTF8", default=0)))
        path = os.path.join(self.tmp.name, "sub.mkv")
        with open(path, "wb") as fh:
            fh.write(only_sub)
        code, _ = self._run("remux", path,
                            "--output", os.path.join(self.tmp.name, "s.mp4"))
        self.assertEqual(code, 4)

    def test_write_failure_code(self):
        target_dir = os.path.join(self.tmp.name, "adir")
        os.mkdir(target_dir)
        code, _ = self._run("remux", self.src, "--output", target_dir)
        self.assertEqual(code, 5)


if __name__ == "__main__":
    unittest.main()
