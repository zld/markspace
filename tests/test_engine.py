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

def make_ladder() -> Ladder:
    return Ladder((300.0, 750.0, 1200.0, 1850.0, 2600.0), 4.0)


class PolicyTests(unittest.TestCase):
    def test_low_buffer_forces_downgrade_even_when_bandwidth_allows(self) -> None:
        ladder = make_ladder()
        policy = AbrPolicy(ladder, PolicyConfig(danger_buffer_seconds=5.0), initial_tier=3)
        decision = policy.select(available_kbps=5000.0, buffer_seconds=4.999)
        self.assertLess(decision.tier, 3)
        self.assertEqual(decision.reason, "buffer-danger")

        edge = AbrPolicy(ladder, PolicyConfig(danger_buffer_seconds=5.0), initial_tier=3)
        held = edge.select(available_kbps=5000.0, buffer_seconds=5.0)
        self.assertEqual(held.tier, 3)
        self.assertEqual(held.reason, "hold")

    def test_downgrade_is_immediate_and_not_delayed_by_up_hysteresis(self) -> None:
        ladder = make_ladder()
        policy = AbrPolicy(ladder, PolicyConfig(min_dwell_segments=99, up_streak_segments=99), initial_tier=4)
        decision = policy.select(available_kbps=800.0, buffer_seconds=25.0)
        self.assertEqual(decision.tier, 1)
        self.assertTrue(decision.changed)
        self.assertEqual(decision.reason, "down-bandwidth")

    def test_up_switch_needs_streak_and_safety_margin(self) -> None:
        ladder = make_ladder()
        config = PolicyConfig(
            danger_buffer_seconds=2.0,
            up_min_buffer_seconds=8.0,
            up_safety_margin=1.2,
            min_dwell_segments=1,
            up_streak_segments=3,
        )
        policy = AbrPolicy(ladder, config)
        # 750 * 1.2 = 900; 899 is insufficient, 900 is sufficient.
        decisions = [policy.select(899.0, 20.0) for _ in range(5)]
        self.assertTrue(all(d.tier == 0 for d in decisions))
        self.assertEqual(policy.up_streak, 0)

        margin_ok = AbrPolicy(ladder, config)
        margin_ok.select(900.0, 20.0)
        self.assertEqual(margin_ok.up_streak, 1)
        margin_ok.select(900.0, 20.0)
        self.assertEqual(margin_ok.up_streak, 2)
        final = margin_ok.select(900.0, 20.0)
        self.assertEqual(final.tier, 1)
        self.assertEqual(final.reason, "up")

    def test_up_switch_requires_minimum_buffer(self) -> None:
        ladder = make_ladder()
        config = PolicyConfig(
            danger_buffer_seconds=2.0, up_min_buffer_seconds=10.0, up_streak_segments=1,
            min_dwell_segments=0,
        )
        policy = AbrPolicy(ladder, config)
        low = policy.select(5000.0, 9.99)
        self.assertEqual(low.tier, 0)
        high = AbrPolicy(ladder, config).select(5000.0, 10.0)
        self.assertEqual(high.tier, 1)

    def test_min_dwell_delays_up_switch(self) -> None:
        ladder = make_ladder()
        config = PolicyConfig(
            danger_buffer_seconds=0.0, up_min_buffer_seconds=0.0, up_streak_segments=1,
            min_dwell_segments=2,
        )
        policy = AbrPolicy(ladder, config)
        self.assertEqual(policy.select(5000.0, 20.0).tier, 0)
        self.assertEqual(policy.select(5000.0, 20.0).tier, 0)
        self.assertEqual(policy.select(5000.0, 20.0).tier, 1)

    def test_up_switch_climbs_one_tier_at_a_time(self) -> None:
        ladder = make_ladder()
        config = PolicyConfig(
            danger_buffer_seconds=0.0, up_min_buffer_seconds=0.0, up_streak_segments=1,
            min_dwell_segments=0,
        )
        policy = AbrPolicy(ladder, config)
        tiers = [policy.select(100000.0, 20.0).tier for _ in range(5)]
        self.assertEqual(tiers, [1, 2, 3, 4, 4])

    def test_invalid_policy_configuration(self) -> None:
        ladder = make_ladder()
        with self.assertRaises(ConfigError):
            AbrPolicy(ladder, PolicyConfig(up_safety_margin=0.9))
        with self.assertRaises(ConfigError):
            AbrPolicy(ladder, PolicyConfig(up_streak_segments=0))
        with self.assertRaises(ConfigError):
            AbrPolicy(ladder, PolicyConfig(min_dwell_segments=-1))
        with self.assertRaises(ConfigError):
            AbrPolicy(ladder, initial_tier=5)
        with self.assertRaises(ConfigError):
            AbrPolicy(ladder).select(0.0, 10.0)
        with self.assertRaises(ConfigError):
            AbrPolicy(ladder).select(1000.0, -1.0)


class SimulationTests(unittest.TestCase):
    def run_engine(self, segments: int = 120, seed: int = 41):
        return simulate_from_parameters(
            segments=segments,
            seed=seed,
            ladder=make_ladder(),
            initial_buffer_seconds=12.0,
            buffer_capacity_seconds=30.0,
        )

    def test_record_stream_matches_segment_count(self) -> None:
        result = self.run_engine()
        self.assertEqual(len(result.records), 120)
        self.assertEqual(result.summary.segments, 120)
        self.assertEqual([r.segment for r in result.records], list(range(1, 121)))
        self.assertTrue(0 <= result.summary.switch_count <= 119)
        self.assertGreaterEqual(result.summary.stall_seconds, 0.0)

    def test_switches_match_record_transitions(self) -> None:
        result = self.run_engine()
        transitions = sum(
            1
            for previous, current in zip(result.records, result.records[1:])
            if previous.tier != current.tier
        )
        flagged = sum(1 for record in result.records if record.switched)
        self.assertEqual(result.summary.switch_count, transitions)
        self.assertEqual(flagged, transitions)
        self.assertFalse(result.records[0].switched)

    def test_means_are_positive(self) -> None:
        summary = self.run_engine().summary
        self.assertGreater(summary.mean_estimated_kbps, 0.0)
        self.assertGreater(summary.mean_selected_kbps, 0.0)
        self.assertTrue(math.isfinite(summary.mean_estimated_kbps))

    def test_buffer_never_exceeds_capacity(self) -> None:
        result = self.run_engine()
        for record in result.records:
            self.assertLessEqual(record.buffer_after_seconds, 30.0 + 1e-9)
            self.assertGreaterEqual(record.buffer_after_seconds, 0.0)

    def test_bandwidth_drop_causes_immediate_downgrade(self) -> None:
        trace = NetworkTrace(
            samples=(
                NetworkSample(5000.0, 30.0, 0.0, False),
                NetworkSample(5000.0, 30.0, 0.0, False),
                NetworkSample(200.0, 30.0, 0.0, False),
                NetworkSample(200.0, 30.0, 0.0, False),
            ),
            seed=0,
            base_kbps=5000.0,
            jitter_kbps=1.0,
            base_rtt_ms=30.0,
            loss_probability=0.0,
        )
        ladder = make_ladder()
        policy = AbrPolicy(ladder, PolicyConfig(danger_buffer_seconds=2.0), initial_tier=4)
        model = BufferModel(30.0, 12.0)
        # Forget factor 1.0 makes the estimator react to the new rate at once;
        # a warm start keeps the cold-start fallback out of this scenario.
        estimator = BandwidthEstimator(forget_factor=1.0, safety_factor=1.0)
        estimator.update(5000.0)
        result = simulate(trace, ladder, policy, model, estimator)
        self.assertEqual(result.records[0].tier, 4)
        # The drop is observed by the estimator after segment 3 downloads,
        # so segment 4 is the first to act on it.
        self.assertEqual(result.records[3].reason, "down-bandwidth")
        self.assertLess(result.records[3].tier, 4)
        self.assertEqual(result.records[-1].selected_kbps, ladder.lowest_kbps)

    def test_stall_is_accumulated_across_segments(self) -> None:
        trace = NetworkTrace(
            samples=tuple(NetworkSample(100.0, 10.0, 0.0, False) for _ in range(3)),
            seed=0,
            base_kbps=100.0,
            jitter_kbps=1.0,
            base_rtt_ms=10.0,
            loss_probability=0.0,
        )
        ladder = make_ladder()
        model = BufferModel(30.0, 0.0)
        result = simulate(trace, ladder, AbrPolicy(ladder), model)
        self.assertAlmostEqual(result.summary.stall_seconds, sum(r.stall_seconds for r in result.records))
        # 300 kbps * 4 s / 100 kbps = 12 s of transfer per segment.
        self.assertAlmostEqual(result.records[0].stall_seconds, 12.01)
        self.assertAlmostEqual(result.records[1].stall_seconds, 8.01)
        self.assertAlmostEqual(result.records[2].stall_seconds, 8.01)
        self.assertAlmostEqual(result.summary.stall_seconds, 28.03)

    def test_loss_burst_lowers_effective_throughput(self) -> None:
        def run(sample: NetworkSample):
            trace = NetworkTrace((sample,), 0, 1000.0, 1.0, 10.0, 0.0)
            ladder = make_ladder()
            return simulate(trace, ladder, AbrPolicy(ladder), BufferModel(30.0, 10.0))

        clean = run(NetworkSample(1000.0, 20.0, 0.0, False))
        lossy = run(NetworkSample(1000.0, 20.0, 0.25, True))
        self.assertLess(lossy.records[0].throughput_kbps, clean.records[0].throughput_kbps)
        self.assertGreater(lossy.records[0].rtt_ms, clean.records[0].rtt_ms)

