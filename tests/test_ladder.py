"""Ladder selection, merging and monotonicity tests."""

from __future__ import annotations

import unittest

from abrscan.ladder import Rung, build_ladder, check_monotonicity, pareto_frontier
from abrscan.store import Measurement


def _measurement(key, kbps, quality):
    return Measurement(
        task_key=key,
        quant_step=20,
        block_size=8,
        encoded_bytes=1000,
        actual_kbps=kbps,
        quality_db=quality,
        payload_sha256="0" * 64,
    )


class LadderTests(unittest.TestCase):
    def test_dominated_candidates_are_removed(self):
        items = [
            _measurement("a", 100.0, 30.0),
            _measurement("b", 150.0, 29.0),
            _measurement("c", 150.0, 30.0),
            _measurement("d", 200.0, 35.0),
        ]
        keys = [candidate.task_key for candidate in pareto_frontier(items)]
        self.assertEqual(keys, ["a", "d"])

    def test_identical_results_merge(self):
        items = [
            _measurement("a", 100.0, 30.0),
            _measurement("b", 100.0, 30.0),
            _measurement("c", 200.0, 35.0),
        ]
        keys = [candidate.task_key for candidate in pareto_frontier(items)]
        self.assertEqual(len(keys), 2)

    def test_targets_share_rungs_and_infeasible_reported(self):
        items = [
            _measurement("low", 60.0, 20.0),
            _measurement("mid", 120.0, 28.0),
            _measurement("high", 300.0, 38.0),
        ]
        result = build_ladder(items, [40, 140, 150, 320])
        self.assertIn(40, result.infeasible_targets)
        self.assertEqual(len(result.rungs), 2)
        self.assertEqual(result.rungs[0].targets, (140, 150))
        self.assertEqual(result.rungs[1].targets, (320,))
        self.assertTrue(result.passed)

    def test_duplicate_targets_do_not_multiply_rungs(self):
        items = [_measurement("a", 100.0, 30.0)]
        result = build_ladder(items, [320, 320, 320])
        self.assertEqual(len(result.rungs), 1)
        self.assertEqual(result.rungs[0].targets, (320,))

    def test_monotonicity_violation_is_detected(self):
        rungs = (
            Rung(1, "a", 20, 8, 100.0, 30.0, (100,)),
            Rung(2, "b", 40, 8, 80.0, 25.0, (200,)),
            Rung(3, "c", 60, 8, 200.0, 24.0, (300,)),
        )
        violations = check_monotonicity(rungs)
        self.assertTrue(any("bitrate" in message for message in violations))
        self.assertTrue(any("quality" in message for message in violations))

    def test_built_ladder_always_passes_monotonicity(self):
        items = [
            _measurement("a", 100.0, 30.0),
            _measurement("b", 200.0, 25.0),
        ]
        result = build_ladder(items, [150, 250])
        self.assertTrue(result.passed)
        self.assertEqual(len(result.rungs), 1)

    def test_no_feasible_rung(self):
        result = build_ladder([_measurement("a", 100.0, 30.0)], [10])
        self.assertFalse(result.passed)
        self.assertEqual(result.infeasible_targets, (10,))


if __name__ == "__main__":
    unittest.main()
