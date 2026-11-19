"""State-boundary and error-path tests for the ABR engine."""
from __future__ import annotations

import io
import json
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path

from abr_engine.cli import main as cli_main
from abr_engine.estimator import (
    DownloadSample,
    EstimatorConfig,
    ThroughputEstimator,
)
from abr_engine.model import (
    MAX_SEGMENTS,
    BitrateLadder,
    BufferState,
    ConfigError,
    StreamConfig,
)
from abr_engine.policy import PolicyConfig, RatePolicy
from abr_engine.simulator import SimulationError, download_duration, simulate
from abr_engine.trace import (
    NetworkTrace,
    TraceSample,
    generate_trace,
    trace_from_json,
    trace_to_json,
)

LADDER = BitrateLadder((350_000, 700_000, 1_200_000, 2_000_000))


def flat_trace(bandwidth_bps: float, n: int = 10, dt: float = 0.5,
               rtt_ms: float = 0.0) -> NetworkTrace:
    return NetworkTrace(dt_s=dt, samples=tuple(
        TraceSample(t_s=i * dt, bandwidth_bps=bandwidth_bps, rtt_ms=rtt_ms,
                    outage=False)
        for i in range(n)
    ))


class ModelTest(unittest.TestCase):
    def test_ladder_rejects_bad_input(self):
        for bad in [(1, 2, 3), (0, 1, 2, 3), (1, 1, 2, 3), (4, 3, 2, 1)]:
            with self.assertRaises(ConfigError):
                BitrateLadder(bad)

    def test_stream_config_rejects_bad_input(self):
        with self.assertRaises(ConfigError):
            StreamConfig(0, 10, 0, 20)  # non-positive duration
        with self.assertRaises(ConfigError):
            StreamConfig(2, 0, 0, 20)  # no segments
        with self.assertRaises(ConfigError):
            StreamConfig(2, MAX_SEGMENTS + 1, 0, 20)  # over the cap
        with self.assertRaises(ConfigError):
            StreamConfig(2, 10, 30, 20)  # initial above max

    def test_buffer_boundary_exact_empty_counts_no_stall(self):
        buf = BufferState(level_s=2.0, max_s=20.0)
        self.assertEqual(buf.consume(2.0), 0.0)
        self.assertEqual(buf.total_rebuffer_s, 0.0)

    def test_buffer_stall_and_resume(self):
        buf = BufferState(level_s=1.0, max_s=20.0)
        self.assertAlmostEqual(buf.consume(3.0), 2.0)
        self.assertTrue(buf.stalled)
        buf.add_segment(2.0)
        self.assertFalse(buf.stalled)
        self.assertAlmostEqual(buf.total_rebuffer_s, 2.0)


class TraceTest(unittest.TestCase):
    def test_same_seed_byte_identical(self):
        a = trace_to_json(generate_trace(seed=41, duration_s=100.0))
        b = trace_to_json(generate_trace(seed=41, duration_s=100.0))
        self.assertEqual(a, b)
        c = trace_to_json(generate_trace(seed=42, duration_s=100.0))
        self.assertNotEqual(a, c)

    def test_roundtrip(self):
        trace = generate_trace(seed=7, duration_s=50.0)
        loaded = trace_from_json(trace_to_json(trace))
        self.assertEqual(trace, loaded)

    def test_rejects_invalid_documents(self):
        for bad in ["not json", "{}", "[]",
                    json.dumps({"version": 1, "dt_s": 0.5, "samples": []})]:
            with self.assertRaises(ConfigError):
                trace_from_json(bad)


class EstimatorTest(unittest.TestCase):
    def test_conservative_initial_value(self):
        est = ThroughputEstimator(EstimatorConfig(), conservative_bps=350_000)
        self.assertEqual(est.estimate_bps(), 350_000)

    def test_invalid_samples_never_pollute(self):
        est = ThroughputEstimator(EstimatorConfig(window=3), 350_000)
        est.add_sample(DownloadSample(0, 1.0))  # zero bytes
        est.add_sample(DownloadSample(100, 0.0))  # non-positive duration
        est.add_sample(DownloadSample(100, 1.0, crossed_outage=True))
        self.assertEqual(est.estimate_bps(), 350_000)

    def test_window_uses_bits_over_time(self):
        est = ThroughputEstimator(EstimatorConfig(window=2), 1.0)
        est.add_sample(DownloadSample(1_000_000, 1.0))  # 8 Mbps
        est.add_sample(DownloadSample(1_000_000, 2.0))  # 4 Mbps
        self.assertAlmostEqual(est.estimate_bps(), 16_000_000 / 3.0)

    def test_ema_smooths(self):
        est = ThroughputEstimator(EstimatorConfig(mode="ema", alpha=0.5), 1.0)
        est.add_sample(DownloadSample(1_000_000, 1.0))  # 8 Mbps
        est.add_sample(DownloadSample(2_000_000, 1.0))  # 16 Mbps
        self.assertAlmostEqual(est.estimate_bps(), 12_000_000.0)


class PolicyTest(unittest.TestCase):
    def test_upswitch_requires_consecutive_clearances(self):
        policy = RatePolicy(LADDER, PolicyConfig(hysteresis=3))
        for _ in range(2):
            self.assertEqual(policy.decide(10_000_000, buffer_s=30.0), 350_000)
        self.assertEqual(policy.decide(10_000_000, buffer_s=30.0), 2_000_000)

    def test_downswitch_is_immediate(self):
        policy = RatePolicy(LADDER, PolicyConfig(hysteresis=3))
        for _ in range(3):
            policy.decide(10_000_000, buffer_s=30.0)
        self.assertEqual(policy.decide(100_000, buffer_s=30.0), 350_000)

    def test_streak_resets_when_estimate_drops(self):
        policy = RatePolicy(LADDER, PolicyConfig(hysteresis=2))
        policy.decide(10_000_000, buffer_s=30.0)
        policy.decide(100_000, buffer_s=30.0)  # breaks the streak
        self.assertEqual(policy.decide(10_000_000, buffer_s=30.0), 350_000)

    def test_low_buffer_shrinks_budget(self):
        policy = RatePolicy(LADDER, PolicyConfig(hysteresis=1,
                                                 buffer_safety_s=4.0))
        # 1.0 Mbps estimate: affords 700k normally, only 350k when low.
        self.assertEqual(policy.decide(1_000_000, buffer_s=1.0), 350_000)


class SimulatorTest(unittest.TestCase):
    def test_download_splits_across_samples(self):
        # 1 Mbps for 1 s, then 3 Mbps: 2 Mbit takes 1 s + 0.5 s = 1.5 s.
        trace = NetworkTrace(dt_s=0.5, samples=tuple(
            TraceSample(i * 0.5, 1_000_000.0 if i < 2 else 3_000_000.0, 0.0,
                        False)
            for i in range(6)
        ))
        duration, outage = download_duration(trace, 0.0, 2_000_000)
        self.assertAlmostEqual(duration, 1.0 + 1_000_000 / 3_000_000)
        self.assertFalse(outage)

    def test_trace_ending_in_outage_fails(self):
        trace = NetworkTrace(dt_s=0.5, samples=(
            TraceSample(0.0, 1_000_000.0, 0.0, False),
            TraceSample(0.5, 0.0, 500.0, True),
        ))
        with self.assertRaises(SimulationError):
            download_duration(trace, 0.0, 10_000_000)

    def test_rebuffer_accumulates_in_simulated_time(self):
        # 2 s segments at 2 Mbps over a 1 Mbps link: each download is 4 s.
        class FixedPolicy:
            def decide(self, estimated_bps, buffer_s):
                return 2_000_000

        stream = StreamConfig(2.0, 3, initial_buffer_s=2.0, max_buffer_s=20.0)
        est = ThroughputEstimator(EstimatorConfig(), 350_000)
        result = simulate(flat_trace(1_000_000), stream, FixedPolicy(), est)
        # Each segment: 2 s buffered covers half of the 4 s download -> 2 s
        # stall per segment, and playback resumes once the segment lands.
        self.assertAlmostEqual(result.total_rebuffer_s, 6.0)
        self.assertEqual([r.rebuffer_s for r in result.records], [2.0] * 3)

    def test_switch_count_and_final_buffer(self):
        stream = StreamConfig(2.0, 10, initial_buffer_s=4.0, max_buffer_s=20.0)
        est = ThroughputEstimator(EstimatorConfig(), 350_000)
        policy = RatePolicy(LADDER, PolicyConfig())
        result = simulate(flat_trace(5_000_000, n=200), stream, policy, est)
        self.assertGreaterEqual(result.switch_count, 0)
        self.assertLessEqual(result.switch_count, 9)
        self.assertLessEqual(result.final_buffer_s, 20.0)
        self.assertEqual(result.total_rebuffer_s, 0.0)


class CliTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.trace_path = Path(self.tmp.name) / "trace.json"
        self.trace_path.write_text(
            trace_to_json(generate_trace(seed=41, duration_s=400.0)))

    def tearDown(self):
        self.tmp.cleanup()

    def run_cli(self, *argv):
        out = io.StringIO()
        with redirect_stdout(out):
            code = cli_main(list(argv))
        return code, out.getvalue()

    def base_args(self):
        return ["simulate", "--trace", str(self.trace_path), "--segments", "20",
                "--segment-duration", "2",
                "--bitrates", "350000,700000,1200000,2000000"]

    def test_happy_path_jsonl(self):
        code, out = self.run_cli(*self.base_args(), "--output", "jsonl")
        self.assertEqual(code, 0)
        lines = out.strip().split("\n")
        self.assertEqual(len(lines), 21)  # 20 segments + 1 summary
        record = json.loads(lines[0])
        for key in ("segment", "bitrate_bps", "estimated_bps", "buffer_s",
                    "rebuffer_s"):
            self.assertIn(key, record)
        summary = json.loads(lines[-1])
        self.assertIn("switch_count", summary)
        self.assertGreaterEqual(summary["total_rebuffer_s"], 0.0)

    def test_deterministic_output(self):
        _, first = self.run_cli(*self.base_args(), "--output", "jsonl")
        _, second = self.run_cli(*self.base_args(), "--output", "jsonl")
        self.assertEqual(first, second)

    def test_invalid_arguments_exit_2(self):
        cases = [
            ["--bitrates", "350000,350000,1200000,2000000"],  # duplicates
            ["--bitrates", "not,a,list,here"],
            ["--segment-duration", "0"],
            ["--segments", str(MAX_SEGMENTS + 1)],
            ["--initial-buffer", "30", "--max-buffer", "20"],
            ["--hysteresis", "0"],
            ["--trace", "/nonexistent/trace.json"],
        ]
        for override in cases:
            args = self.base_args()
            for j in range(0, len(override), 2):
                flag, value = override[j], override[j + 1]
                if flag in args:
                    args[args.index(flag) + 1] = value
                else:
                    args += [flag, value]
            code, out = self.run_cli(*args)
            self.assertEqual(code, 2, msg=f"{override} -> {code}")
            self.assertEqual(out, "")  # no fake partial summary

    def test_simulation_failure_exit_1(self):
        # Trace that ends in a permanent outage cannot complete downloads.
        trace = NetworkTrace(dt_s=0.5, samples=(
            TraceSample(0.0, 0.0, 500.0, True),
        ))
        self.trace_path.write_text(trace_to_json(trace))
        code, _ = self.run_cli(*self.base_args())
        self.assertEqual(code, 1)


if __name__ == "__main__":
    unittest.main()
