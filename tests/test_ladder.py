"""Unit tests for monotonic ladder selection and alias accounting."""

from __future__ import annotations

import unittest

from ladderforge.errors import NonMonotonicLadderError
from ladderforge.ladder import Candidate, build_ladder, verify_monotonic
from ladderforge.plan import TargetTier


def cand(key: str, target_kbps: int, actual_bps: int, psnr: float, res: str = "320x180") -> Candidate:
    width, height = (int(part) for part in res.split("x"))
    return Candidate(
        task_key=key,
        resolution=res,
        width=width,
        height=height,
        target_bps=target_kbps * 1000,
        preset="medium",
        actual_bps=actual_bps,
        quality_psnr_db=psnr,
    )


class LadderTests(unittest.TestCase):
    def test_picks_closest_actual_bitrate(self) -> None:
        candidates = [
            cand("lo", 250, 240_000, 29.0),
            cand("mid", 500, 505_000, 30.0),
            cand("hi", 1000, 990_000, 31.0),
        ]
        ladder = build_ladder(
            candidates, [TargetTier(250), TargetTier(500), TargetTier(1000)]
        )
        self.assertEqual([r.task_key for r in ladder.rungs], ["lo", "mid", "hi"])
        self.assertTrue(verify_monotonic(ladder.rungs))

    def test_aliases_carried_onto_rungs(self) -> None:
        candidates = [
            cand("a", 250, 250_000, 29.0),
            cand("b", 500, 500_000, 30.0),
            cand("c", 1000, 1_000_000, 31.0),
        ]
        ladder = build_ladder(
            candidates,
            [TargetTier(250), TargetTier(500), TargetTier(1000, aliases=1)],
        )
        self.assertEqual([r.aliases for r in ladder.rungs], [0, 0, 1])
        self.assertEqual(ladder.rungs[-1].target_bps, 1_000_000)

    def test_distinct_targets_may_share_one_candidate(self) -> None:
        shared = cand("shared", 250, 260_000, 30.0)
        ladder = build_ladder([shared], [TargetTier(250), TargetTier(500)])
        self.assertEqual([r.target_bps for r in ladder.rungs], [250_000, 500_000])
        self.assertEqual([r.task_key for r in ladder.rungs], ["shared", "shared"])

    def test_prefers_psnr_when_bitrates_tie(self) -> None:
        candidates = [
            cand("worse", 500, 500_000, 29.0, res="320x180"),
            cand("better", 500, 500_000, 32.0, res="480x270"),
        ]
        ladder = build_ladder(candidates, [TargetTier(500)])
        self.assertEqual(ladder.rungs[0].task_key, "better")

    def test_psnr_drop_is_rejected(self) -> None:
        candidates = [
            cand("lo", 250, 250_000, 34.0),
            cand("hi", 1000, 1_000_000, 30.0),
        ]
        with self.assertRaises(NonMonotonicLadderError):
            build_ladder(candidates, [TargetTier(250), TargetTier(1000)])

    def test_bitrate_inversion_is_rejected(self) -> None:
        # Both candidates are in-band for both targets, so sharing is possible
        # only at equal bitrate; a strictly falling sequence is still invalid.
        candidates = [
            cand("lo", 250, 900_000, 30.0),
            cand("hi", 1000, 400_000, 33.0),
        ]
        ladder = build_ladder(candidates, [TargetTier(250), TargetTier(1000)])
        self.assertTrue(verify_monotonic(ladder.rungs))
        self.assertGreaterEqual(
            ladder.rungs[1].actual_bps, ladder.rungs[0].actual_bps
        )

    def test_alternate_candidate_can_rescue_monotonicity(self) -> None:
        # Closest pick at 1000 sits below the 500 rung's bitrate and PSNR.
        candidates = [
            cand("lo1", 250, 250_000, 28.0),
            cand("lo2", 250, 260_000, 28.2),
            cand("hi1", 1000, 240_000, 27.0),
            cand("hi2", 1000, 1_050_000, 31.0),
        ]
        ladder = build_ladder(candidates, [TargetTier(250), TargetTier(1000)])
        self.assertEqual(ladder.rungs[1].task_key, "hi2")
        self.assertTrue(verify_monotonic(ladder.rungs))

    def test_error_message_names_closest_picks(self) -> None:
        # The only candidate sits far below the 500 kbps target's band, so no
        # valid rung exists for that tier.
        candidates = [cand("only", 250, 260_000, 30.0)]
        with self.assertRaises(NonMonotonicLadderError) as ctx:
            build_ladder(candidates, [TargetTier(250), TargetTier(2000)])
        self.assertIn("250000", str(ctx.exception))
        self.assertIn("2000000", str(ctx.exception))

    def test_empty_candidates_rejected(self) -> None:
        with self.assertRaises(NonMonotonicLadderError):
            build_ladder([], [TargetTier(250)])

    def test_serialises_to_dict(self) -> None:
        ladder = build_ladder([cand("a", 250, 250_000, 29.123456)], [TargetTier(250)])
        payload = ladder.to_dict()
        self.assertEqual(payload["monotonicity"], "PASS")
        self.assertEqual(payload["rungs"][0]["actual_bps"], 250_000)
        self.assertEqual(payload["rungs"][0]["quality_psnr_db"], 29.1235)


if __name__ == "__main__":
    unittest.main()
