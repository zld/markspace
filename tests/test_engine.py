"""Tests for the ABR engine: determinism, estimator, policy, simulator, CLI."""
from __future__ import annotations

import json
import os
import tempfile
import unittest

from abr_engine import cli
from abr_engine.estimator import ThroughputEstimator
from abr_engine.models import NetworkSample, Segment, ValidationError, validate_ladder
from abr_engine.policy import AbrPolicy, PolicyConfig
from abr_engine.report import SUMMARY_KEYS, summarize, to_payload
from abr_engine.simulator import simulate
from abr_engine.trace import generate_trace, load_trace, save_trace

LADDER = [300_000, 750_000, 1_200_000, 2_400_000, 4_000_000]


def make_sample(ts, bw=2_000_000.0, rtt=50.0, loss=0.0):
    return NetworkSample(timestamp_s=ts, bandwidth_bps=bw, rtt_ms=rtt, loss_rate=loss)


def flat_trace(bw=2_000_000.0, n=400, interval=1.0):
    return [make_sample(i * interval, bw) for i in range(n)]


class TraceTests(unittest.TestCase):
    def test_deterministic_per_seed(self):
        a = generate_trace(seed=41, duration_s=60, interval_s=1.0)
        b = generate_trace(seed=41, duration_s=60, interval_s=1.0)
        self.assertEqual(a, b)
        c = generate_trace(seed=42, duration_s=60, interval_s=1.0)
        self.assertNotEqual(a, c)

    def test_save_load_roundtrip(self):
        samples = generate_trace(seed=7, duration_s=30, interval_s=0.5)
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "trace.json")
            save_trace(samples, path)
            self.assertEqual(load_trace(path), samples)

    def test_invalid_interval_and_duration(self):
        for bad in (0.0, -1.0, float("nan"), float("inf")):
            with self.assertRaises(ValidationError):
                generate_trace(seed=1, duration_s=10, interval_s=bad)
            with self.assertRaises(ValidationError):
                generate_trace(seed=1, duration_s=bad, interval_s=1.0)

    def test_load_rejects_regression_and_missing_file(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "bad.json")
            with open(path, "w") as fh:
                json.dump({"version": 1, "samples": [
                    {"timestamp_s": 2.0, "bandwidth_bps": 1e6, "rtt_ms": 10, "loss_rate": 0},
                    {"timestamp_s": 1.0, "bandwidth_bps": 1e6, "rtt_ms": 10, "loss_rate": 0},
                ]}, fh)
            with self.assertRaisesRegex(ValidationError, r"samples\[1\]"):
                load_trace(path)
            with self.assertRaises(ValidationError):
                load_trace(os.path.join(tmp, "nope.json"))


class EstimatorTests(unittest.TestCase):
    def test_conservative_initialization(self):
        est = ThroughputEstimator(default_bandwidth_bps=123_000.0)
        self.assertEqual(est.estimate(0.0).bandwidth_bps, 123_000.0)
        est.add_sample(make_sample(0.0, 10_000_000.0))
        est.add_sample(make_sample(1.0, 10_000_000.0))
        got = est.estimate(1.0)
        self.assertLess(got.bandwidth_bps, 10_000_000.0)
        self.assertLess(got.confidence, 1.0)

    def test_uses_only_past_samples(self):
        est = ThroughputEstimator()
        est.add_sample(make_sample(0.0, 1_000_000.0))
        est.add_sample(make_sample(100.0, 9_000_000.0))
        self.assertLess(est.estimate(50.0).bandwidth_bps, 2_000_000.0)

    def test_timestamp_regression_rejected(self):
        est = ThroughputEstimator()
        est.add_sample(make_sample(5.0))
        with self.assertRaises(ValidationError):
            est.add_sample(make_sample(4.0))

    def test_gap_lowers_confidence(self):
        est = ThroughputEstimator(gap_threshold_s=2.0)
        for i in range(5):
            est.add_sample(make_sample(float(i)))
        full = est.estimate(4.0).confidence
        est.add_sample(make_sample(20.0))
        self.assertLess(est.estimate(20.0).confidence, full)


class PolicyTests(unittest.TestCase):
    def make_policy(self, **kwargs):
        cfg = PolicyConfig(up_consecutive=3, dwell_s=8.0, **kwargs)
        return AbrPolicy(LADDER, cfg)

    def estimate(self, bw, conf=1.0):
        from abr_engine.estimator import Estimate
        return Estimate(bw, conf, 10)

    def test_upswitch_requires_streak_and_dwell(self):
        policy = self.make_policy()
        rich = self.estimate(50_000_000.0)
        for seg in range(2):  # streak 1, 2: no upgrade yet
            d = policy.select(now_s=float(seg), estimate=rich, rtt_ms=10, loss_rate=0, buffer_s=20)
            self.assertEqual(d.switch, "none")
        d = policy.select(now_s=2.0, estimate=rich, rtt_ms=10, loss_rate=0, buffer_s=20)
        self.assertEqual(d.switch, "up")
        self.assertEqual(d.level, 1)
        # dwell: streak rebuilds but dwell since switch blocks the next upgrade
        for seg in range(3, 6):
            d = policy.select(now_s=float(seg), estimate=rich, rtt_ms=10, loss_rate=0, buffer_s=20)
            self.assertEqual(d.switch, "none")

    def test_downswitch_immediate(self):
        policy = self.make_policy()
        policy._level = 3
        d = policy.select(now_s=0.0, estimate=self.estimate(100_000.0),
                          rtt_ms=10, loss_rate=0, buffer_s=20)
        self.assertEqual(d.switch, "down")
        self.assertEqual(d.level, 0)

    def test_buffer_panic_forces_downgrade(self):
        policy = self.make_policy()
        policy._level = 2
        d = policy.select(now_s=0.0, estimate=self.estimate(50_000_000.0),
                          rtt_ms=10, loss_rate=0, buffer_s=1.0)
        self.assertEqual(d.switch, "down")


class SimulatorTests(unittest.TestCase):
    def test_deterministic(self):
        trace = generate_trace(seed=41, duration_s=180, interval_s=1.0)
        kwargs = dict(segments=60, segment_duration_s=4.0, initial_buffer_s=8.0)
        a = simulate(trace, LADDER, **kwargs)
        b = simulate(trace, LADDER, **kwargs)
        self.assertEqual(a.records, b.records)
        self.assertEqual(len(a.records), 60)

    def test_stall_accounting_on_poor_network(self):
        trace = flat_trace(bw=100_000.0)  # far below the lowest rung
        result = simulate(trace, LADDER, segments=5, segment_duration_s=4.0, initial_buffer_s=1.0)
        summary = summarize(result)
        self.assertGreater(summary["stall_event_count"], 0)
        self.assertGreater(summary["stall_duration_seconds"], 0.0)
        self.assertEqual(summary["final_buffer_seconds"], result.records[-1].buffer_after_s)

    def test_trace_exhaustion_records_continuation(self):
        trace = flat_trace(bw=2_000_000.0, n=5)  # ends at t=4
        result = simulate(trace, LADDER, segments=10, segment_duration_s=4.0, initial_buffer_s=8.0)
        self.assertTrue(result.continuation_intervals)
        start, end = result.continuation_intervals[-1]
        self.assertGreaterEqual(start, 4.0)
        self.assertGreater(end, start)

    def test_invalid_inputs_rejected(self):
        trace = flat_trace()
        with self.assertRaises(ValidationError):
            simulate([], LADDER, segments=5, segment_duration_s=4.0, initial_buffer_s=1.0)
        with self.assertRaises(ValidationError):
            simulate(trace, LADDER, segments=5, segment_duration_s=0.0, initial_buffer_s=1.0)
        with self.assertRaises(ValidationError):
            simulate(trace, LADDER, segments=0, segment_duration_s=4.0, initial_buffer_s=1.0)
        with self.assertRaises(ValidationError):
            simulate(trace, LADDER, segments=5, segment_duration_s=4.0, initial_buffer_s=float("nan"))
        with self.assertRaises(ValidationError):
            simulate(trace, [500_000, 500_000], segments=5, segment_duration_s=4.0, initial_buffer_s=1.0)

    def test_segment_size_conflict_rejected(self):
        with self.assertRaises(ValidationError):
            Segment(index=0, bitrate_bps=1_000_000, duration_s=4.0, size_bytes=999.0)
        ok = Segment(index=0, bitrate_bps=1_000_000, duration_s=4.0, size_bytes=500_000.0)
        self.assertEqual(ok.resolved_size_bytes(), 500_000.0)
        with self.assertRaises(ValidationError):
            simulate(flat_trace(), LADDER, segments=2, segment_duration_s=4.0,
                     initial_buffer_s=1.0, segment_bytes=12345.0)

    def test_summary_fields_and_units(self):
        result = simulate(generate_trace(seed=3, duration_s=60, interval_s=1.0),
                          LADDER, segments=10, segment_duration_s=4.0, initial_buffer_s=8.0)
        summary = summarize(result)
        self.assertEqual(tuple(summary.keys()), SUMMARY_KEYS)
        self.assertEqual(summary["switch_count"],
                         summary["switch_up_count"] + summary["switch_down_count"])
        payload = to_payload(result)
        self.assertEqual(payload["units"]["bitrate"], "bit/s")
        json.dumps(payload, allow_nan=False)


class CliTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.trace_path = os.path.join(self.tmp.name, "trace.json")

    def tearDown(self):
        self.tmp.cleanup()

    def run_cli(self, argv):
        return cli.main(argv)

    def test_generate_and_simulate_exit_zero(self):
        self.assertEqual(self.run_cli(["generate", "--seed", "41", "--duration", "180",
                                       "--interval", "1", "--output", self.trace_path]), 0)
        out = os.path.join(self.tmp.name, "result.json")
        code = self.run_cli(["simulate", "--trace", self.trace_path,
                             "--bitrates", "300000,750000,1200000,2400000,4000000",
                             "--segments", "60", "--segment-duration", "4",
                             "--initial-buffer", "8", "--output", out])
        self.assertEqual(code, 0)
        with open(out) as fh:
            payload = json.load(fh)
        self.assertEqual(len(payload["segments"]), 60)
        self.assertEqual(self.run_cli(["summarize", "--result", out]), 0)

    def test_invalid_inputs_exit_two(self):
        self.assertEqual(self.run_cli(["generate", "--seed", "1", "--duration", "10",
                                       "--interval", "0", "--output", self.trace_path]), 2)
        self.assertEqual(self.run_cli(["simulate", "--trace", self.trace_path,
                                       "--bitrates", "1000,500", "--segments", "5",
                                       "--segment-duration", "4", "--initial-buffer", "1"]), 2)
        save_trace(flat_trace(n=50), self.trace_path)
        self.assertEqual(self.run_cli(["simulate", "--trace", self.trace_path,
                                       "--bitrates", "300000,750000", "--segments", "5",
                                       "--segment-duration", "0", "--initial-buffer", "1"]), 2)


if __name__ == "__main__":
    unittest.main()
