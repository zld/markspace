"""End-to-end pipeline tests using generated fixtures (no external files)."""

from __future__ import annotations

import json
import tempfile
import unittest
from fractions import Fraction
from pathlib import Path

import numpy as np

from frameaudit.decode import DecodedFrame
from frameaudit.errors import (
    NoVideoStreamError,
    OutputError,
    ReorderLimitError,
    UsageError,
)
from frameaudit.faults import apply_faults, load_manifest
from frameaudit.fixture import generate_fixture
from frameaudit.order import BoundedFrameReorderer, reorder_frames
from frameaudit.pipeline import process_file
from frameaudit.repair import repair_timestamps, resolve_frame_rate
from frameaudit.report import write_frames_jsonl
from frameaudit.stats import compute_stats


def _frame(index: int, pts: int | None, pict: str = "P") -> DecodedFrame:
    return DecodedFrame(
        decode_index=index,
        pts=pts,
        dts=pts,
        time_base=Fraction(1, 1000),
        keyframe=pict == "I",
        pict_type=pict,
        width=8,
        height=8,
        luma=np.full((8, 8), index % 256, dtype=np.float32),
    )


class ReorderTests(unittest.TestCase):
    def test_stable_pts_order_with_decode_tiebreak(self):
        seq = [_frame(0, 0), _frame(1, 3), _frame(2, 1), _frame(3, 2)]
        out = list(reorder_frames(seq, limit=4))
        self.assertEqual([f.pts for f in out], [0, 1, 2, 3])
        self.assertEqual([f.decode_index for f in out], [0, 2, 3, 1])

    def test_equal_pts_keep_decode_order(self):
        out = list(reorder_frames([_frame(0, 5), _frame(1, 5)], limit=2))
        self.assertEqual([f.decode_index for f in out], [0, 1])

    def test_missing_pts_keeps_decode_position(self):
        seq = [_frame(0, 0), _frame(1, None), _frame(2, 1)]
        out = list(reorder_frames(seq, limit=8))
        self.assertEqual([f.decode_index for f in out], [0, 1, 2])

    def test_buffer_overflow_fails(self):
        buf = BoundedFrameReorderer(limit=1)
        buf.push(_frame(0, 10))
        with self.assertRaises(ReorderLimitError):
            buf.push(_frame(1, 5))

    def test_invalid_limit_rejected(self):
        with self.assertRaises(UsageError):
            BoundedFrameReorderer(limit=0)


class RepairTests(unittest.TestCase):
    def test_frame_rate_required_when_unknown(self):
        with self.assertRaises(UsageError):
            resolve_frame_rate(None, None)

    def test_all_four_repair_reasons(self):
        pts = [i * 33 for i in range(120)]
        pts[25] = None
        pts[50] = 48 * 33
        pts[75] = 77 * 33
        pts[100] = 99 * 33
        frames = [_frame(i, p) for i, p in enumerate(pts)]
        result = repair_timestamps(frames, Fraction(30, 1))
        self.assertEqual(result.repaired_count, 4)
        self.assertEqual(
            result.reason_counts,
            {"missing": 1, "backward": 1, "jump": 1, "duplicate": 1},
        )
        repaired = [r.repaired_pts for r in result.records]
        self.assertTrue(all(b > a for a, b in zip(repaired, repaired[1:])))
        self.assertIsNotNone(result.max_jump_ms)

    def test_healthy_stream_needs_no_repair(self):
        frames = [_frame(i, i * 33) for i in range(60)]
        result = repair_timestamps(frames, Fraction(30, 1))
        self.assertEqual(result.repaired_count, 0)
        self.assertTrue(all(r.repair_reason == "ok" for r in result.records))


class StatsTests(unittest.TestCase):
    def test_frame_type_totals_and_luma(self):
        frames = [
            _frame(0, 0, "I"),
            _frame(1, 33, "P"),
            _frame(2, 66, "B"),
            _frame(3, 99, "?"),
        ]
        result = repair_timestamps(frames, Fraction(30, 1))
        stats = compute_stats(result.records)
        self.assertEqual(stats.decoded_frames, 4)
        self.assertEqual(
            (stats.i_frames, stats.p_frames, stats.b_frames, stats.unknown_frames),
            (1, 1, 1, 1),
        )
        self.assertIsNotNone(stats.luma_mean)


class FixturePipelineTests(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory(prefix="frameaudit-test-")
        self.tmp = Path(self._tmp.name)
        self.media, self.manifest_path = generate_fixture(
            self.tmp / "clip.mp4",
            frame_count=120,
            fps=30,
            faults=4,
            overwrite=True,
        )

    def tearDown(self):
        self._tmp.cleanup()

    def test_fixture_manifest_shape(self):
        manifest = load_manifest(self.manifest_path)
        self.assertEqual(len(manifest["faults"]), 4)
        kinds = {entry["kind"] for entry in manifest["faults"]}
        self.assertEqual(kinds, {"missing", "backward", "jump", "duplicate"})

    def test_end_to_end_summary_and_jsonl(self):
        result = process_file(
            self.media,
            explicit_fps=30,
            faults_manifest=self.manifest_path,
        )
        s = result.summary
        self.assertEqual(s["decoded_frames"], 120)
        self.assertEqual(s["i_frames"] + s["p_frames"] + s["b_frames"], 120)
        self.assertGreaterEqual(s["i_frames"], 1)
        self.assertGreater(s["p_frames"], 0)
        self.assertGreater(s["b_frames"], 0)
        self.assertEqual(s["timestamp_repaired"], 4)
        self.assertEqual(
            set(s["repair_reasons"]),
            {"missing", "backward", "jump", "duplicate"},
        )

        jsonl_path = self.tmp / "frames.jsonl"
        write_frames_jsonl(jsonl_path, result.repair)
        rows = [json.loads(line) for line in jsonl_path.read_text().splitlines()]
        self.assertEqual(len(rows), 120)
        self.assertTrue(all("original_pts" in r and "repaired_pts" in r
                            for r in rows))

    def test_refuses_to_overwrite_outputs(self):
        with self.assertRaises(OutputError):
            generate_fixture(
                self.tmp / "clip.mp4", frame_count=30, fps=30, faults=0
            )
        result = process_file(
            self.media, explicit_fps=30, faults_manifest=self.manifest_path
        )
        existing = self.tmp / "taken.jsonl"
        existing.write_text("")
        with self.assertRaises(OutputError):
            write_frames_jsonl(existing, result.repair)

    def test_missing_media_raises_media_error(self):
        import av

        audio_path = self.tmp / "audio-only.mp4"
        container = av.open(str(audio_path), "w")
        try:
            stream = container.add_stream("aac", rate=48000)
            for _ in range(3):
                frame = av.AudioFrame.from_ndarray(
                    np.zeros((1, 1024), dtype=np.float32),
                    format="flt",
                    layout="mono",
                )
                frame.sample_rate = 48000
                for packet in stream.encode(frame):
                    container.mux(packet)
            for packet in stream.encode():
                container.mux(packet)
        finally:
            container.close()
        with self.assertRaises(NoVideoStreamError):
            process_file(audio_path)


class FaultApplicationTests(unittest.TestCase):
    def test_manifest_missing_clears_pts(self):
        frames = [_frame(i, i * 33) for i in range(10)]
        manifest = {"faults": [{"presentation_position": 3, "kind": "missing"}]}
        out = apply_faults(frames, manifest, Fraction(30, 1))
        self.assertIsNone(out[3].pts)
        self.assertIsNotNone(frames[3].pts)  # original untouched


if __name__ == "__main__":
    unittest.main()
