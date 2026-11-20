"""Ladder selection, duplicate targets and metric edge-case tests."""
from __future__ import annotations

import unittest

import numpy as np

from ladderlab import ladder, metrics, rd


def _cand(key: str, bps: int, q: float, h: str | None = None) -> dict:
    return {
        "param_key": key,
        "actual_bps": bps,
        "quality_score": q,
        "stream_hash": h or key,
        "nbytes": bps,
        "per_clip": {},
    }


class LadderTest(unittest.TestCase):
    def test_duplicate_targets_merge_into_aliases(self) -> None:
        uniq, aliases = ladder.unique_targets([300, 300, 900])
        self.assertEqual(uniq, [300, 900])
        self.assertEqual(aliases, 1)
        cands = [_cand("a", 100, 30.0), _cand("b", 500, 40.0)]
        rungs, viol = ladder.select_ladder(cands, [300, 300, 900])
        self.assertEqual(viol, 0)
        self.assertEqual(rungs[0].aliases, [300, 300])
        self.assertEqual(len(rungs), 2)

    def test_strictly_increasing_bitrate_enforced(self) -> None:
        # Two rungs but only equal-bitrate candidates available for both.
        cands = [_cand("a", 100, 30.0), _cand("b", 100, 31.0)]
        rungs, viol = ladder.select_ladder(cands, [200, 300])
        self.assertGreaterEqual(viol, 1)
        ok = [r for r in rungs if r.status == "ok"]
        bps = [r.actual_bps for r in ok]
        self.assertEqual(bps, sorted(set(bps)))  # no equal pair chosen

    def test_quality_inversion_skipped(self) -> None:
        cands = [
            _cand("lo", 100, 30.0),
            _cand("dip", 200, 20.0),  # cheaper than hi but worse than lo
            _cand("hi", 300, 40.0),
        ]
        rungs, viol = ladder.select_ladder(cands, [150, 350])
        self.assertEqual(viol, 0)
        picked = [r.param_key for r in rungs]
        self.assertEqual(picked, ["lo", "hi"])

    def test_unreachable_rung_not_fabricated(self) -> None:
        cands = [_cand("a", 500, 30.0)]
        rungs, viol = ladder.select_ladder(cands, [100])
        self.assertEqual(viol, 1)
        self.assertEqual(rungs[0].status, "unreachable")
        self.assertIsNone(rungs[0].actual_bps)

    def test_validate_detects_monotonicity(self) -> None:
        cands = [_cand("a", 100, 30.0), _cand("b", 200, 35.0)]
        rungs, _ = ladder.select_ladder(cands, [150, 250])
        checks = ladder.validate_ladder(rungs)
        self.assertTrue(checks["bitrate_monotonic"])
        self.assertTrue(checks["quality_monotonic"])
        self.assertEqual(checks["violations"], 0)

    def test_stream_dedupe_keeps_best(self) -> None:
        results = [
            _cand("p1", 100, 30.0, h="dup"),
            _cand("p2", 100, 32.0, h="dup"),
            _cand("p3", 200, 33.0, h="uniq"),
        ]
        cands, dropped = rd.dedupe_candidates(results)
        self.assertEqual(dropped, 1)
        self.assertEqual(len(cands), 2)
        self.assertEqual(cands[0]["param_key"], "p2")  # higher quality kept


class MetricsEdgeTest(unittest.TestCase):
    def test_zero_error_gives_finite_score(self) -> None:
        a = np.zeros((2, 8, 8, 3), dtype=np.uint8)
        self.assertEqual(metrics.psnr_db(a, a), metrics.PSNR_CAP_DB)

    def test_shape_mismatch_has_reason(self) -> None:
        a = np.zeros((2, 8, 8, 3), dtype=np.uint8)
        b = np.zeros((3, 8, 8, 3), dtype=np.uint8)
        with self.assertRaises(metrics.MetricError) as ctx:
            metrics.psnr_db(a, b)
        self.assertIn("shape mismatch", str(ctx.exception))

    def test_zero_frames_has_reason(self) -> None:
        a = np.zeros((0, 8, 8, 3), dtype=np.uint8)
        with self.assertRaises(metrics.MetricError) as ctx:
            metrics.psnr_db(a, a)
        self.assertIn("frame count", str(ctx.exception))


if __name__ == "__main__":
    unittest.main()
