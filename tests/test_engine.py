"""Boundary and state-transition tests for the ABR engine."""

from __future__ import annotations

import json
import math
import os
import subprocess
import sys
import unittest
from contextlib import redirect_stderr, redirect_stdout
from io import StringIO

from abr_engine.buffer import BufferModel
from abr_engine.cli import EXIT_CONFIG_ERROR, EXIT_OK, EXIT_SIMULATION_ERROR, main
from abr_engine.errors import ConfigError
from abr_engine.estimator import BandwidthEstimator
from abr_engine.ladder import Ladder, parse_bitrates
from abr_engine.policy import AbrPolicy, PolicyConfig
from abr_engine.simulate import simulate, simulate_from_parameters
from abr_engine.trace import NetworkSample, NetworkTrace, generate_trace

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


class TraceTests(unittest.TestCase):
    def test_same_seed_reproduces_exact_samples(self) -> None:
        first = generate_trace(30, seed=41)
        second = generate_trace(30, seed=41)
        self.assertEqual(first.samples, second.samples)
        other = generate_trace(30, seed=42)
        self.assertNotEqual(first.samples, other.samples)

    def test_sample_count_and_valid_ranges(self) -> None:
        trace = generate_trace(50, seed=7)
        self.assertEqual(len(trace), 50)
        for sample in trace:
            self.assertGreater(sample.throughput_kbps, 0)
            self.assertTrue(math.isfinite(sample.throughput_kbps))
            self.assertGreaterEqual(sample.rtt_ms, 5.0)
            self.assertIn(sample.loss_probability, (0.0, 0.25))

    def test_loss_arrives_in_bounded_contiguous_bursts(self) -> None:
        trace = generate_trace(200, seed=3, loss_probability=0.5)
        self.assertTrue(any(sample.loss_burst for sample in trace))
        runs: list[int] = []
        current = 0
        for sample in trace:
            self.assertEqual(sample.loss_probability, 0.25 if sample.loss_burst else 0.0)
            if sample.loss_burst:
                current += 1
            elif current:
                runs.append(current)
                current = 0
        if current:
            runs.append(current)
        self.assertTrue(runs)
        # Adjacent bursts may merge, so only contiguous, positive runs are guaranteed.
        self.assertTrue(all(length >= 1 for length in runs))
        self.assertEqual(sum(runs), sum(sample.loss_burst for sample in trace))
        quiet = generate_trace(100, seed=5, loss_probability=0.0)
        self.assertFalse(any(sample.loss_burst for sample in quiet))

    def test_zero_and_negative_segments_rejected(self) -> None:
        for value in (0, -1):
            with self.assertRaises(ConfigError):
                generate_trace(value, seed=1)

    def test_invalid_network_parameters_rejected(self) -> None:
        with self.assertRaises(ConfigError):
            generate_trace(10, seed=1, base_kbps=0)
        with self.assertRaises(ConfigError):
            generate_trace(10, seed=1, base_kbps=-100)
        with self.assertRaises(ConfigError):
            generate_trace(10, seed=1, loss_probability=-0.1)
        with self.assertRaises(ConfigError):
            generate_trace(10, seed=1, loss_probability=1.5)
        with self.assertRaises(ConfigError):
            generate_trace(10, seed=1, base_rtt_ms=float("nan"))
        with self.assertRaises(ConfigError):
            generate_trace(10, seed=1, jitter_kbps=float("inf"))


class LadderTests(unittest.TestCase):
    def test_parse_bitrates(self) -> None:
        self.assertEqual(parse_bitrates("300, 750,1200"), (300.0, 750.0, 1200.0))

    def test_parse_rejects_empty_and_garbage(self) -> None:
        for text in ("", "  ", "300,,750", "300,abc", None):
            with self.assertRaises(ConfigError):
                parse_bitrates(text)

    def test_ladder_requires_strictly_increasing_positive(self) -> None:
        with self.assertRaises(ConfigError):
            Ladder((300.0, 300.0), 4.0)
        with self.assertRaises(ConfigError):
            Ladder((750.0, 300.0), 4.0)
        with self.assertRaises(ConfigError):
            Ladder((0.0, 300.0), 4.0)
        with self.assertRaises(ConfigError):
            Ladder((-1.0,), 4.0)
        with self.assertRaises(ConfigError):
            Ladder((), 4.0)
        with self.assertRaises(ConfigError):
            Ladder((300.0, 750.0), 0.0)

    def test_affordable_tier_is_inclusive(self) -> None:
        ladder = Ladder((300.0, 750.0, 1200.0), 4.0)
        self.assertEqual(ladder.affordable_tier(299.9), 0)
        self.assertEqual(ladder.affordable_tier(300.0), 0)
        self.assertEqual(ladder.affordable_tier(750.0), 1)
        self.assertEqual(ladder.affordable_tier(1199.9), 1)
        self.assertEqual(ladder.affordable_tier(5000.0), 2)
        with self.assertRaises(ConfigError):
            ladder.affordable_tier(float("nan"))


class EstimatorTests(unittest.TestCase):
    def test_cold_start_does_not_divide_by_zero(self) -> None:
        estimator = BandwidthEstimator()
        self.assertGreater(estimator.available_kbps(), 0)
        self.assertTrue(math.isfinite(estimator.available_kbps()))
        self.assertIsNone(estimator.raw_estimate_kbps())
        self.assertAlmostEqual(estimator.available_kbps(), 300.0)
        self.assertEqual(estimator.sample_count, 0)
        with self.assertRaises(ValueError):
            BandwidthEstimator(cold_start_kbps=0.0)

    def test_ewma_favours_recent_samples(self) -> None:
        estimator = BandwidthEstimator(forget_factor=0.5, safety_factor=1.0)
        estimator.update(1000.0)
        estimator.update(2000.0)
        self.assertAlmostEqual(estimator.raw_estimate_kbps(), 1500.0)
        estimator.update(2000.0)
        self.assertAlmostEqual(estimator.raw_estimate_kbps(), 1750.0)

    def test_safety_factor_applied(self) -> None:
        estimator = BandwidthEstimator(forget_factor=1.0, safety_factor=0.8)
        estimator.update(1000.0)
        self.assertAlmostEqual(estimator.available_kbps(), 800.0)

    def test_invalid_samples_do_not_pollute_estimate(self) -> None:
        estimator = BandwidthEstimator(forget_factor=0.5, safety_factor=1.0)
        estimator.update(1000.0)
        for bad in (float("nan"), float("inf"), -5.0, 0.0, "oops"):
            estimator.update(bad)
        self.assertEqual(estimator.rejected_samples, 5)
        self.assertAlmostEqual(estimator.raw_estimate_kbps(), 1000.0)
        estimator.update(1200.0)
        self.assertAlmostEqual(estimator.raw_estimate_kbps(), 1100.0)
        self.assertTrue(math.isfinite(estimator.available_kbps()))

    def test_rejects_bad_configuration(self) -> None:
        with self.assertRaises(ValueError):
            BandwidthEstimator(forget_factor=0.0)
        with self.assertRaises(ValueError):
            BandwidthEstimator(forget_factor=1.5)
        with self.assertRaises(ValueError):
            BandwidthEstimator(safety_factor=0.0)


class BufferTests(unittest.TestCase):
    def test_download_time_includes_transfer_and_rtt(self) -> None:
        model = BufferModel(30.0, 12.0)
        seconds = model.download_time(1200.0, 4.0, 2400.0, 100.0)
        self.assertAlmostEqual(seconds, 2.0 + 0.1)

    def test_buffer_grows_capped_at_capacity(self) -> None:
        model = BufferModel(20.0, 10.0)
        state = model.consume(300.0, 4.0, 3000.0)
        self.assertAlmostEqual(state.before_seconds, 10.0)
        self.assertAlmostEqual(state.download_seconds, 0.4)
        self.assertAlmostEqual(state.after_seconds, 13.6)
        state = model.consume(300.0, 4.0, 3000.0)
        self.assertAlmostEqual(state.after_seconds, 17.2)
        state = model.consume(300.0, 4.0, 3000.0)
        self.assertAlmostEqual(state.after_seconds, 20.0)

    def test_stall_accumulates_playout_shortfall(self) -> None:
        model = BufferModel(30.0, 2.0)
        state = model.consume(2600.0, 4.0, 1000.0, 0.0)
        self.assertAlmostEqual(state.download_seconds, 10.4)
        self.assertAlmostEqual(state.stall_seconds, 8.4)
        self.assertAlmostEqual(state.after_seconds, 4.0)
        self.assertAlmostEqual(model.total_stall_seconds, 8.4)
        state = model.consume(300.0, 4.0, 100000.0, 0.0)
        self.assertAlmostEqual(state.download_seconds, 0.012)
        self.assertAlmostEqual(state.stall_seconds, 0.0)
        self.assertAlmostEqual(model.total_stall_seconds, 8.4)
        self.assertGreater(state.after_seconds, 4.0)

    def test_configuration_errors(self) -> None:
        with self.assertRaises(ConfigError):
            BufferModel(0.0, 0.0)
        with self.assertRaises(ConfigError):
            BufferModel(30.0, -1.0)
        with self.assertRaises(ConfigError):
            BufferModel(10.0, 11.0)
        with self.assertRaises(ConfigError):
            BufferModel(30.0).consume(300.0, 4.0, 0.0)

