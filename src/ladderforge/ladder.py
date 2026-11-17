"""Ladder construction: pick one candidate per target and enforce monotonicity.

Candidates come from every completed scan job, regardless of the target they
were encoded for.  For each unique target bitrate we choose the candidate whose
*actual* bitrate is closest to that target, subject to two global constraints:
as the target rises, actual bitrate must not fall and PSNR must not fall.

The selection is solved with dynamic programming over the target tiers, so two
different targets are allowed to select the same candidate; each target still
gets its own rung, and duplicate target requests are reported as ``aliases``.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Sequence

from .errors import NonMonotonicLadderError
from .plan import TargetTier

_EPS = 1e-9


@dataclass(frozen=True)
class Candidate:
    task_key: str
    resolution: str
    width: int
    height: int
    target_bps: int
    preset: str
    actual_bps: int
    quality_psnr_db: float


@dataclass(frozen=True)
class LadderRung:
    target_bps: int
    aliases: int
    resolution: str
    preset: str
    actual_bps: int
    quality_psnr_db: float
    task_key: str

    def to_dict(self) -> dict[str, object]:
        return {
            "target_bps": self.target_bps,
            "actual_bps": int(self.actual_bps),
            "quality_psnr_db": round(float(self.quality_psnr_db), 4),
            "aliases": self.aliases,
            "resolution": self.resolution,
            "preset": self.preset,
            "task_key": self.task_key,
        }


@dataclass
class Ladder:
    rungs: list[LadderRung] = field(default_factory=list)
    monotonicity: str = "PASS"

    def to_dict(self) -> dict[str, object]:
        return {
            "monotonicity": self.monotonicity,
            "rungs": [rung.to_dict() for rung in self.rungs],
        }


# A candidate may serve a target only if its actual bitrate is within this
# ratio of the target.  Without a band the DP can "cheat" monotonicity by
# assigning a far-off encode to a tier, producing a ladder that is monotone
# but useless (and would silently accept a PSNR regression).
ELIGIBILITY_RATIO = 2.0


def _eligible(candidate: Candidate, target_bps: int) -> bool:
    if target_bps <= 0:
        return False
    ratio = candidate.actual_bps / target_bps
    return 1.0 / ELIGIBILITY_RATIO <= ratio <= ELIGIBILITY_RATIO


def _compatible(previous: Candidate, current: Candidate) -> bool:
    # "Must not fall": equal actual bitrate is permitted, so two adjacent
    # targets may legitimately share the same candidate.  Each target still
    # keeps its own rung record.
    if current.actual_bps < previous.actual_bps:
        return False
    return current.quality_psnr_db >= previous.quality_psnr_db - _EPS


def _cost(candidate: Candidate, target_bps: int) -> float:
    # Primary term: absolute bitrate error (scaled to bits so it dominates).
    # Secondary term: nudge toward the higher-PSNR encode on near-ties.
    return abs(candidate.actual_bps - target_bps) * 1000.0 - candidate.quality_psnr_db


def _select(candidates: Sequence[Candidate], target_bps: Sequence[int]) -> list[Candidate] | None:
    """DP: minimise total |actual-target| under monotonic constraints."""
    if not candidates:
        return None
    count = len(candidates)
    # cost[i][j]: best cost with tier i served by candidate j.
    cost: list[list[float]] = []
    back: list[list[int]] = []
    for index, target in enumerate(target_bps):
        layer = [float("inf")] * count
        pointers = [-1] * count
        if index == 0:
            for current in range(count):
                if _eligible(candidates[current], target):
                    layer[current] = _cost(candidates[current], target)
        else:
            previous_cost = cost[index - 1]
            for current in range(count):
                if not _eligible(candidates[current], target):
                    continue
                best = float("inf")
                best_prev = -1
                for prev in range(count):
                    if previous_cost[prev] == float("inf"):
                        continue
                    if not _compatible(candidates[prev], candidates[current]):
                        continue
                    total = previous_cost[prev] + _cost(candidates[current], target)
                    if total < best:
                        best = total
                        best_prev = prev
                layer[current] = best
                pointers[current] = best_prev
        cost.append(layer)
        back.append(pointers)

    last = cost[-1]
    best_index = min(range(count), key=lambda j: last[j])
    if last[best_index] == float("inf"):
        return None

    selection: list[int] = [0] * len(target_bps)
    cursor = best_index
    for index in range(len(target_bps) - 1, -1, -1):
        selection[index] = cursor
        cursor = back[index][cursor] if index > 0 else -1
    return [candidates[j] for j in selection]


def build_ladder(
    candidates: Sequence[Candidate],
    targets: Sequence[TargetTier],
) -> Ladder:
    """Build a monotonic ladder, or raise :class:`NonMonotonicLadderError`."""
    if not candidates:
        raise NonMonotonicLadderError("no completed candidates to build a ladder from")
    if not targets:
        raise NonMonotonicLadderError("plan contains no bitrate targets")

    ordered_tiers = sorted(targets, key=lambda tier: tier.target_bps)
    target_values = [tier.target_bps for tier in ordered_tiers]

    selection = _select(candidates, target_values)
    if selection is None:
        closest = []
        for value in target_values:
            pick = min(candidates, key=lambda c: abs(c.actual_bps - value))
            closest.append(
                f"{value}->{pick.actual_bps}bps/{pick.quality_psnr_db:.2f}dB"
            )
        raise NonMonotonicLadderError(
            "no monotonic selection satisfies actual-bitrate and PSNR "
            f"constraints; closest independent picks: {', '.join(closest)}"
        )

    return Ladder(
        rungs=[
            LadderRung(
                target_bps=tier.target_bps,
                aliases=tier.aliases,
                resolution=candidate.resolution,
                preset=candidate.preset,
                actual_bps=int(candidate.actual_bps),
                quality_psnr_db=float(candidate.quality_psnr_db),
                task_key=candidate.task_key,
            )
            for tier, candidate in zip(ordered_tiers, selection)
        ],
        monotonicity="PASS",
    )


def verify_monotonic(rungs: Sequence[LadderRung]) -> bool:
    for previous, current in zip(rungs, rungs[1:]):
        if current.actual_bps < previous.actual_bps:
            return False
        if current.quality_psnr_db < previous.quality_psnr_db - _EPS:
            return False
    return True
