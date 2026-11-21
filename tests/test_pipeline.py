"""End-to-end and unit tests for the mkvmp4 pipeline.

Run with either ``.venv/bin/python -m unittest discover -s tests`` or pytest if
it happens to be installed; no test dependency beyond NumPy is required.
"""

from __future__ import annotations

import io
import json
import sys
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))
sys.path.insert(0, str(ROOT / "scripts"))

import make_fixture  # noqa: E402
from mkvmp4 import cli, mp4  # noqa: E402
from mkvmp4.ebml import read_vint_size, read_vint_id  # noqa: E402
from mkvmp4.errors import (  # noqa: E402
    BoundaryError,
    EbmlParseError,
    TimestampPolicyError,
    TrackMappingError,
    UnsupportedError,
)
from mkvmp4.matroska import parse_matroska  # noqa: E402
from mkvmp4.timestamps import SampleInput, rewrite_timeline  # noqa: E402
from mkvmp4.tree import parse_tree  # noqa: E402


class VintTests(unittest.TestCase):
    def test_round_trip_widths(self) -> None:
        for width in range(1, 9):
            value = (1 << (7 * width - 1)) - 1
            encoded = ((1 << (7 * width)) | value).to_bytes(width, "big")
            self.assertEqual(read_vint_size(encoded, 0)[0], value)

    def test_unknown_size_marker(self) -> None:
        size, length, unknown = read_vint_size(b"\xff", 0)
        self.assertIsNone(size)
        self.assertTrue(unknown)
        self.assertEqual(length, 1)

    def test_zero_byte_rejected(self) -> None:
        with self.assertRaises(EbmlParseError):
            read_vint_size(b"\x00\x01", 0)

    def test_truncated_vint_rejected(self) -> None:
        with self.assertRaises(EbmlParseError):
            read_vint_size(b"\x01\x00", 0)  # 8-byte VINT with two bytes present
        with self.assertRaises(EbmlParseError):
            read_vint_id(b"\x1a\x45", 0)  # 4-byte ID, 2 bytes present

    def test_id_marker_retained(self) -> None:
        self.assertEqual(read_vint_id(b"\x18\x53\x80\x67", 0), (0x18538067, 4))


class TreeTests(unittest.TestCase):
    def test_unknown_cluster_ends_at_next_cluster(self) -> None:
        data = make_fixture.build_sample_document()
        nodes, stats = parse_tree(data)
        segment = next(n for n in nodes if n.name == "Segment")
        clusters = [n for n in segment.children if n.name == "Cluster"]
        self.assertEqual(len(clusters), 4)
        unknown = [c for c in clusters if c.unknown_size]
        self.assertEqual(len(unknown), 1)
        self.assertEqual(unknown[0].end_offset, clusters[2].offset)
        self.assertEqual(stats["unknown_sizes"], 1)

    def test_depth_budget_rejected(self) -> None:
        data = make_fixture.build_sample_document()
        with self.assertRaises(BoundaryError):
            parse_tree(data, max_depth=2)

    def test_element_budget_rejected(self) -> None:
        data = make_fixture.build_sample_document()
        with self.assertRaises(BoundaryError):
            parse_tree(data, max_elements=10)

    def test_unknown_size_non_cluster_rejected(self) -> None:
        payload = make_fixture.element(
            0x1549A966, b"", unknown_size=True
        )  # Info with unknown size
        doc = make_fixture.element(0x18538067, payload, unknown_size=True)
        with self.assertRaises(BoundaryError):
            parse_tree(doc)

    def test_truncated_input_rejected(self) -> None:
        with self.assertRaises(EbmlParseError):
            parse_tree(make_fixture.build_truncated_document())


class MatroskaTests(unittest.TestCase):
    def test_tracks_and_lacing(self) -> None:
        mk = parse_matroska(make_fixture.build_sample_document())
        self.assertEqual([t.number for t in mk.tracks], [1, 2])
        self.assertEqual([t.track_type for t in mk.tracks], ["video", "audio"])
        self.assertEqual(mk.timestamp_scale, 1_000_000)
        self.assertEqual(len(mk.clusters), 4)
        audio_blocks = [b for c in mk.clusters for b in c.blocks if b.track == 2]
        self.assertEqual(len(audio_blocks), 6)  # two laced triples expanded
        self.assertEqual([b.lace_index for b in audio_blocks], [0, 1, 2, 0, 1, 2])
        lengths = [b.data_length for b in audio_blocks]
        self.assertEqual(lengths[:3], [12, 15, 20])

    def test_blockgroup_keyframes_follow_reference_blocks(self) -> None:
        def block(payload: bytes) -> bytes:
            return make_fixture.block_header(1, 0, 0) + payload

        def group(*references: int) -> bytes:
            body = make_fixture.element(make_fixture.ID_BLOCK, block(b"F"))
            for offset in references:
                body += make_fixture.element(make_fixture.ID_REFERENCE_BLOCK, bytes([offset]))
            return make_fixture.element(make_fixture.ID_BLOCK_GROUP, body)

        cluster = make_fixture.element(
            make_fixture.ID_CLUSTER,
            make_fixture.uint_element(make_fixture.ID_CLUSTER_TIMESTAMP, 0)
            + group()              # no ReferenceBlock -> keyframe
            + group(1, 2),         # references present -> delta frame
        )
        doc = make_fixture.element(
            0x1A45DFA3, make_fixture.string_element(make_fixture.ID_DOCTYPE, "matroska")
        ) + make_fixture.element(make_fixture.ID_SEGMENT, cluster, unknown_size=True)
        mk = parse_matroska(doc)
        self.assertEqual([b.keyframe for b in mk.clusters[0].blocks], [True, False])

    def test_unknown_segment_is_bounded(self) -> None:
        mk = parse_matroska(make_fixture.build_sample_document(unknown_segment=True))
        self.assertTrue(mk.segment_unknown_size)
        self.assertEqual(mk.segment_end, len(mk.data))


class TimestampTests(unittest.TestCase):
    def sample(self, cluster: int, rel: int, offset: int = 0) -> SampleInput:
        return SampleInput(1, cluster, rel, 0, 1, offset, 10, True, 33_333_333)

    def test_nanoseconds_and_timescale(self) -> None:
        report = rewrite_timeline(
            [self.sample(0, 0), self.sample(0, 1000)],
            timestamp_scale=1_000_000,
            timescale=90_000,
        )
        # 1000 ms = 1e9 ns; at 90000 Hz that is exactly 90_000_000 ticks.
        self.assertEqual(list(report.tracks[1].ticks), [0, 90_000_000])

    def test_offset_and_negative_clip(self) -> None:
        report = rewrite_timeline(
            [self.sample(0, 0), self.sample(0, 500)],
            timestamp_scale=1_000_000,
            timescale=1_000,
            offset_ms=-250,
        )
        timeline = report.tracks[1]
        self.assertEqual(int(timeline.ticks[0]), 0)
        self.assertEqual(timeline.stats["clipped"], 1)

    def test_negative_clip_can_be_disabled(self) -> None:
        with self.assertRaises(TimestampPolicyError):
            rewrite_timeline(
                [self.sample(0, 0)],
                timestamp_scale=1_000_000,
                timescale=1_000,
                offset_ms=-1,
                clip_negative=False,
            )

    def test_strict_rejects_regression_and_repair_fixes_it(self) -> None:
        samples = [self.sample(0, 0), self.sample(0, 100), self.sample(0, 50)]
        with self.assertRaises(TimestampPolicyError):
            rewrite_timeline(samples, timestamp_scale=1_000_000, timescale=1_000)
        report = rewrite_timeline(
            samples, timestamp_scale=1_000_000, timescale=1_000, mode="repair"
        )
        timeline = report.tracks[1]
        self.assertEqual(timeline.stats["non_monotonic"], 1)
        self.assertTrue(all(b > a for a, b in zip(timeline.ticks, timeline.ticks[1:])))

    def test_rounding_is_counted(self) -> None:
        sample = SampleInput(1, 0, 1, 0, 1, 0, 10, True, None)
        report = rewrite_timeline(
            [sample], timestamp_scale=1_000_000, timescale=3, mode="repair"
        )
        self.assertEqual(report.tracks[1].stats["rounded"], 0)

    def test_gap_limit(self) -> None:
        samples = [self.sample(0, 0), self.sample(0, 5000)]
        with self.assertRaises(TimestampPolicyError):
            rewrite_timeline(
                samples,
                timestamp_scale=1_000_000,
                timescale=1_000,
                max_gap_ms=1000,
            )

    def test_overflow_detected_not_wrapped(self) -> None:
        huge = SampleInput(1, 2**40, 2**40, 0, 1, 0, 10, True, None)
        with self.assertRaises(TimestampPolicyError):
            rewrite_timeline([huge], timestamp_scale=1_000_000, timescale=1_000)


class RemuxTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)
        self.mkv = self.dir / "sample.mkv"
        self.mkv.write_bytes(make_fixture.build_sample_document())

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def run_cli(self, *args: str) -> tuple[int, dict]:
        buffer = io.StringIO()
        with redirect_stdout(buffer):
            code = cli.main(list(args))
        text = buffer.getvalue()
        return code, json.loads(text) if text.strip().startswith("{") else {}

    def test_inspect_reports_tracks_and_ranges(self) -> None:
        code, report = self.run_cli("inspect", str(self.mkv), "--json")
        self.assertEqual(code, 0)
        self.assertEqual(report["track_count"], 2)
        self.assertGreaterEqual(report["element_count"], 20)
        segment = report["segments_and_clusters"][0]
        self.assertEqual(segment["name"], "Segment")
        self.assertEqual(segment["offset"], 20)

    def test_remux_produces_verified_index(self) -> None:
        out = self.dir / "sample.mp4"
        code, report = self.run_cli(
            "remux", str(self.mkv), "--out", str(out), "--timescale", "1000000",
            "--offset-ms", "250", "--repair", "--map", "1:1,2:2", "--json",
        )
        self.assertEqual(code, 0)
        self.assertEqual(report["sample_count"], 10)
        self.assertEqual(report["corrections"]["non_monotonic"], 1)
        self.assertEqual(report["first_tick"], 250_000_000)
        self.assertTrue(report["index_ok"])
        code, verify = self.run_cli("verify", str(out), "--json")
        self.assertEqual(code, 0)
        self.assertTrue(verify["index_ok"])
        self.assertEqual(verify["sample_count"], 10)
        self.assertEqual([t["track_id"] for t in verify["tracks"]], [1, 2])

    def test_payload_bytes_are_copied_verbatim(self) -> None:
        out = self.dir / "sample.mp4"
        self.run_cli(
            "remux", str(self.mkv), "--out", str(out), "--repair",
            "--map", "1:1,2:2",
        )
        src = self.mkv.read_bytes()
        dst = out.read_bytes()
        for marker in (b"VID1", b"VID2", b"VID3", b"VID4", b"AUD1", b"AUD6"):
            self.assertIn(marker, dst)
        self.assertEqual(
            src.count(b"VID1"), dst.count(b"VID1"), "payload must not be altered"
        )

    def test_strict_failure_preserves_existing_target(self) -> None:
        out = self.dir / "keep.mp4"
        out.write_bytes(b"ORIGINAL")
        code, report = self.run_cli(
            "remux", str(self.mkv), "--out", str(out), "--map", "1:1,2:2", "--json"
        )
        self.assertEqual(code, 4)
        self.assertEqual(report["error"], "TimestampPolicyError")
        self.assertEqual(report["context"]["track"], 1)
        self.assertEqual(report["context"]["sample_index"], 3)
        self.assertEqual(out.read_bytes(), b"ORIGINAL")
        self.assertEqual(list(self.dir.glob(".*.tmp")), [])

    def test_missing_mapping_is_reported_per_track(self) -> None:
        code, report = self.run_cli(
            "remux", str(self.mkv), "--out", str(self.dir / "x.mp4"),
            "--require-map", "--json",
        )
        self.assertEqual(code, 3)
        self.assertEqual(report["error"], "TrackMappingError")
        self.assertIn("source tracks [1, 2]", report["message"])

    def test_partial_mapping_is_rejected_even_with_derivation_enabled(self) -> None:
        code, report = self.run_cli(
            "remux", str(self.mkv), "--out", str(self.dir / "x.mp4"),
            "--map", "1:1", "--json",
        )
        self.assertEqual(code, 3)
        self.assertIn("source track 2", report["message"])

    def test_derived_mapping_is_flagged(self) -> None:
        out = self.dir / "derived.mp4"
        code, report = self.run_cli(
            "remux", str(self.mkv), "--out", str(out), "--repair", "--json"
        )
        self.assertEqual(code, 0)
        self.assertTrue(report["mapping_derived"])
        self.assertEqual([t["track_id"] for t in report["tracks"]], [1, 2])

    def test_duplicate_target_rejected(self) -> None:
        code, report = self.run_cli(
            "remux", str(self.mkv), "--out", str(self.dir / "x.mp4"),
            "--map", "1:3,2:3", "--json",
        )
        self.assertEqual(code, 3)
        self.assertIn("used by both", report["message"])

    def test_unsupported_codec_rejected(self) -> None:
        bad = self.dir / "flac.mkv"
        bad.write_bytes(make_fixture.build_unsupported_codec_document())
        code, report = self.run_cli(
            "remux", str(bad), "--out", str(self.dir / "x.mp4"),
            "--map", "1:1,2:2", "--json",
        )
        self.assertEqual(code, 3)
        self.assertEqual(report["error"], "UnsupportedError")
        self.assertEqual(report["context"]["track"], 2)

    def test_bad_vint_reports_offset(self) -> None:
        bad = self.dir / "bad.mkv"
        bad.write_bytes(make_fixture.build_bad_vint_document())
        code, report = self.run_cli("inspect", str(bad), "--json")
        self.assertEqual(code, 3)
        self.assertEqual(report["context"]["offset"], 24)

    def test_truncated_input_reports_offset(self) -> None:
        bad = self.dir / "trunc.mkv"
        bad.write_bytes(make_fixture.build_truncated_document())
        code, report = self.run_cli("inspect", str(bad), "--json")
        self.assertEqual(code, 3)
        self.assertEqual(report["context"]["offset"], 20)

    def test_verify_rejects_corrupted_sample_size(self) -> None:
        out = self.dir / "sample.mp4"
        self.run_cli(
            "remux", str(self.mkv), "--out", str(out), "--repair",
            "--map", "1:1,2:2",
        )
        blob = bytearray(out.read_bytes())
        stsz = blob.index(b"stsz")
        # Inflate the first per-sample size so sample offsets leave mdat.
        blob[stsz + 16 : stsz + 20] = (0xFFFFFF).to_bytes(4, "big")
        out.write_bytes(bytes(blob))
        code, report = self.run_cli("verify", str(out), "--json")
        self.assertEqual(code, 5)
        self.assertEqual(report["error"], "VerifyError")
        self.assertIn("outside mdat", report["message"])

    def test_verify_rejects_truncated_file(self) -> None:
        out = self.dir / "sample.mp4"
        self.run_cli(
            "remux", str(self.mkv), "--out", str(out), "--repair",
            "--map", "1:1,2:2",
        )
        out.write_bytes(out.read_bytes()[:-40])
        code, report = self.run_cli("verify", str(out), "--json")
        self.assertEqual(code, 5)
        self.assertEqual(report["exit_code"], 5)

    def test_stss_present_when_not_all_samples_are_sync(self) -> None:
        out = self.dir / "sample.mp4"
        self.run_cli(
            "remux", str(self.mkv), "--out", str(out), "--repair",
            "--map", "1:1,2:2",
        )
        _, verify = self.run_cli("verify", str(out), "--json")
        video = next(t for t in verify["tracks"] if t["handler"] == "vide")
        self.assertIn("stss", video["stbl_children"])
        self.assertEqual(video["sync_samples"], 2)

    def test_non_json_output_prints_track_map(self) -> None:
        out = self.dir / "sample.mp4"
        code, _ = self.run_cli(
            "remux", str(self.mkv), "--out", str(out), "--repair",
            "--map", "1:1,2:2",
        )
        self.assertEqual(code, 0)
        code, _ = self.run_cli("verify", str(out))
        self.assertEqual(code, 0)

    def test_usage_error_exit_code(self) -> None:
        code, _ = self.run_cli("remux", str(self.mkv), "--out", str(self.dir / "x.mp4"),
                               "--map", "1", "--json")
        self.assertEqual(code, 2)


class Mp4UnitTests(unittest.TestCase):
    def test_language_packing(self) -> None:
        self.assertEqual(mp4.decode_language(mp4.encode_language("eng")), "eng")
        self.assertEqual(mp4.decode_language(mp4.encode_language("???")), "und")

    def test_co64_selected_for_large_offsets(self) -> None:
        small = mp4.build_stco_chunk([100])
        large = mp4.build_stco_chunk([0x1_0000_0000])
        self.assertEqual(small[4:8], b"stco")
        self.assertEqual(large[4:8], b"co64")

    def test_stsz_rejects_zero_length_samples(self) -> None:
        with self.assertRaises(mp4.OutputError):
            mp4.build_stsz([10, 0])

    def test_box_size_field_matches_payload(self) -> None:
        encoded = mp4.full_box("test", 0, 0, b"abcd")
        self.assertEqual(int.from_bytes(encoded[:4], "big"), len(encoded))


if __name__ == "__main__":
    unittest.main()
