"""Tests for the ABR engine: models, estimator, policy, simulator, CLI."""
from __future__ import annotations

import contextlib
import io
import json
import os
import tempfile
import unittest

from abr_engine import cli
from abr_engine.estimator import BandwidthEstimator
from abr_engine.metrics import summarize
from abr_engine.models import BufferState, ConfigError, Ladder, NetworkSample
from abr_engine.policy import AbrPolicy, PolicyConfig
from abr_engine.simulator import (
    SimulationConfig,
    SimulationError,
    Trace,
    TraceError,
    run_simulation,
    trace_from_dict,
)
from abr_engine.tracegen import generate_trace


class LadderTests(unittest.TestCase):
    def test_rejects_duplicates(self):
        with self.assertRaises(ConfigError):
            Ladder.build([100, 100, 200], 4.0)

    def test_rejects_non_positive_and_bad_duration(self):
        with self.assertRaises(ConfigError):
            Ladder.build([100, 0], 4.0)
        with self.assertRaises(ConfigError):
            Ladder.build([100], 0.0)
        with self.assertRaises(ConfigError):
            Ladder.build([], 4.0)

    def test_sorts_rungs(self):
        self.assertEqual(Ladder.build([300, 100, 200], 4.0).bitrates,
                         (100, 200, 300))


class EstimatorTests(unittest.TestCase):
    def test_ignores_zero_duration_and_falls_back(self):
        est = BandwidthEstimator(window=4, decay=0.5, fallback_bandwidth=123.0)
        est.add_sample(1000, 0.0)
        est.add_sample(float("nan"), 1.0)
        self.assertEqual(est.estimate(), 123.0)
        self.assertTrue(est.used_fallback)
        self.assertEqual(est.last_fallback_reason, "no_valid_samples")
        self.assertEqual(est.fallback_count, 1)

    def test_weighted_harmonic_estimate(self):
        est = BandwidthEstimator(window=3, decay=0.5)
        for throughput in (1e6, 2e6, 3e6):
            est.add_sample(throughput, 1.0)
        # weights 0.25, 0.5, 1.0 -> harmonic mean 2.1e6
        self.assertAlmostEqual(est.estimate(), 2.1e6, places=1)
        self.assertFalse(est.used_fallback)


class PolicyTests(unittest.TestCase):
    def _policy(self, fallback=1000.0, danger=2.0, recover=5.0):
        ladder = Ladder.build([100, 200, 400, 800], 4.0)
        est = BandwidthEstimator(fallback_bandwidth=fallback)
        cfg = PolicyConfig(safety=0.5, danger_buffer=danger,
                           recover_buffer=recover)
        return AbrPolicy(ladder, cfg, est), est

    def test_upswitch_only_above_recover_buffer(self):
        policy, _ = self._policy()
        self.assertEqual(policy.select(3.0).switch, "none")  # below recover
        self.assertEqual(policy.select(6.0).switch, "up")
        self.assertEqual(policy.current, 400)  # 1000 * 0.5 ceiling

    def test_forced_downswitch_below_danger(self):
        policy, _ = self._policy()
        policy.select(6.0)  # upswitch to 400
        decision = policy.select(1.0)  # below danger
        self.assertEqual(decision.switch, "down")
        self.assertEqual(decision.bitrate, 200)

    def test_unsustainable_rung_steps_down(self):
        policy, est = self._policy()
        policy.select(6.0)
        est.add_sample(50, 1.0)  # collapse the estimate
        decision = policy.select(6.0)
        self.assertEqual(decision.switch, "down")
        self.assertEqual(decision.bitrate, 100)

    def test_recover_must_exceed_danger(self):
        with self.assertRaises(ConfigError):
            PolicyConfig(danger_buffer=5.0, recover_buffer=5.0).validate()


class TraceTests(unittest.TestCase):
    def test_time_regression_is_locatable(self):
        with self.assertRaisesRegex(TraceError, "sample 1"):
            Trace([NetworkSample(1.0, 100.0), NetworkSample(0.5, 100.0)])

    def test_negative_and_non_finite_bandwidth_rejected(self):
        with self.assertRaisesRegex(TraceError, "sample 0"):
            Trace([NetworkSample(0.0, -5.0)])
        with self.assertRaisesRegex(TraceError, "sample 0"):
            Trace([NetworkSample(0.0, float("nan"))])

    def test_missing_sample_holds_last_bandwidth(self):
        trace = Trace([NetworkSample(0.0, 1e6), NetworkSample(1.0, None)])
        self.assertAlmostEqual(trace.download_time(1e6, 0.5), 1.0)

    def test_zero_bandwidth_gap_then_recovery(self):
        trace = Trace([NetworkSample(0.0, 1e6), NetworkSample(1.0, 0.0),
                       NetworkSample(3.0, 1e6)])
        # 1s at 1e6, 2s of zero bandwidth, then 1s at 1e6 again.
        self.assertAlmostEqual(trace.download_time(2e6, 0.0), 4.0)

    def test_zero_tail_cannot_complete(self):
        trace = Trace([NetworkSample(0.0, 1e6), NetworkSample(1.0, 0.0)])
        with self.assertRaises(SimulationError):
            trace.download_time(1e6, 1.0)


class SimulatorTests(unittest.TestCase):
    def _run(self, samples, initial_buffer):
        trace = Trace(samples)
        ladder = Ladder.build([500_000], 4.0)
        cfg = SimulationConfig(ladder=ladder, segment_count=3,
                               initial_buffer=initial_buffer,
                               policy=PolicyConfig())
        return run_simulation(trace, cfg)

    def test_rebuffer_accumulates_and_buffer_never_negative(self):
        records = self._run([NetworkSample(0.0, 1e6)], initial_buffer=1.0)
        self.assertAlmostEqual(records[0].rebuffer_seconds, 1.0)  # 2s download
        self.assertAlmostEqual(records[1].rebuffer_seconds, 0.0)
        self.assertTrue(all(r.buffer_seconds >= 0.0 for r in records))
        self.assertAlmostEqual(records[0].buffer_seconds, 4.0)

    def test_summary_counts(self):
        records = self._run([NetworkSample(0.0, 1e6)], initial_buffer=1.0)
        summary = summarize(records)
        self.assertEqual(summary["segment_count"], 3)
        self.assertEqual(summary["fallback_count"], 1)  # first segment only
        self.assertGreaterEqual(summary["total_rebuffer_seconds"], 0.0)


class CliTests(unittest.TestCase):
    def _run_cli(self, argv):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            code = cli.main(argv)
        return code, out.getvalue()

    def _write_trace(self, seed=41, segments=20):
        fd, path = tempfile.mkstemp(suffix=".json")
        with os.fdopen(fd, "w") as fh:
            json.dump(generate_trace(seed, segments), fh)
        self.addCleanup(os.unlink, path)
        return path

    def test_json_run_ok_and_deterministic(self):
        path = self._write_trace()
        argv = ["--trace", path, "--bitrates", "300000,750000,1500000,3000000",
                "--segment-seconds", "4", "--initial-buffer", "12",
                "--safety", "0.82", "--json"]
        code1, out1 = self._run_cli(argv)
        code2, out2 = self._run_cli(argv)
        self.assertEqual((code1, code2), (0, 0))
        self.assertEqual(out1, out2)
        payload = json.loads(out1)
        self.assertEqual(len(payload["segments"]), 20)
        first = payload["segments"][0]
        for key in ("index", "selected_bitrate", "estimated_bandwidth",
                    "buffer_seconds", "rebuffer_seconds", "switch"):
            self.assertIn(key, first)
        self.assertIsInstance(payload["fallback_count"], int)
        self.assertGreaterEqual(payload["total_rebuffer_seconds"], 0.0)

    def test_invalid_inputs_exit_2(self):
        path = self._write_trace()
        for argv in (
            ["--trace", path, "--bitrates", "abc"],
            ["--trace", path, "--bitrates", "100,100"],
            ["--trace", path, "--bitrates", "100", "--segment-seconds", "0"],
            ["--trace", "no-such-file.json", "--bitrates", "100"],
        ):
            code, _ = self._run_cli(argv)
            self.assertEqual(code, 2, argv)

    def test_invalid_trace_content_exit_2(self):
        fd, path = tempfile.mkstemp(suffix=".json")
        with os.fdopen(fd, "w") as fh:
            json.dump({"samples": [{"time": 1.0, "bandwidth": 100},
                                   {"time": 0.5, "bandwidth": 100}]}, fh)
        self.addCleanup(os.unlink, path)
        code, out = self._run_cli(["--trace", path, "--bitrates", "100",
                                   "--json"])
        self.assertEqual(code, 2)
        self.assertIn("sample 1", json.loads(out)["error"]["message"])

    def test_runtime_error_exit_1(self):
        fd, path = tempfile.mkstemp(suffix=".json")
        with os.fdopen(fd, "w") as fh:
            json.dump({"segments": 3, "samples": [
                {"time": 0, "bandwidth": 1000000},
                {"time": 1, "bandwidth": 0}]}, fh)
        self.addCleanup(os.unlink, path)
        code, out = self._run_cli(["--trace", path, "--bitrates", "1000000",
                                   "--json"])
        self.assertEqual(code, 1)
        self.assertEqual(json.loads(out)["error"]["type"], "simulation_error")

    def test_batch_mode_reports_aggregate(self):
        code, out = self._run_cli(
            ["--seeds", "1,2,3", "--segments", "10",
             "--bitrates", "300000,750000,1500000", "--json"])
        self.assertEqual(code, 0)
        payload = json.loads(out)
        self.assertEqual(len(payload["runs"]), 3)
        agg = payload["aggregate"]
        self.assertIn(agg["worst_index"], (0, 1, 2))
        worst = payload["runs"][agg["worst_index"]]
        self.assertEqual(agg["max_rebuffer_seconds"],
                         worst["total_rebuffer_seconds"])


if __name__ == "__main__":
    unittest.main()
