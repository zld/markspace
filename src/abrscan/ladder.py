"""ABR ladder construction and monotonicity checks.

Pipeline:
1. Every finished measurement is a candidate.
2. Duplicate measurements (same bitrate and quality) are merged.
3. Dominated candidates are removed: A dominates B when A is no more
   expensive and no worse in quality, with at least one strict improvement.
4. Each target picks the best-quality remaining candidate that fits its
   bitrate ceiling; targets sharing a candidate share one rung.
5. Rungs are checked for strictly increasing bitrate and non-decreasing
   quality.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Iterable, Sequence

from .store import Measurement

_EPS = 1e-9


@dataclass(frozen=True)
class Candidate:
    task_key: str
    quant_step: int
    block_size: int
    actual_kbps: float
    quality_db: float


@dataclass(frozen=True)
class Rung:
    index: int
    task_key: str
    quant_step: int
    block_size: int
    actual_kbps: float
    quality_db: float
    targets: tuple[int, ...]


@dataclass(frozen=True)
class LadderResult:
    rungs: tuple[Rung, ...]
    infeasible_targets: tuple[int, ...]
    feasible_targets: tuple[int, ...]
    bitrate_strictly_increasing: bool
    quality_non_decreasing: bool
    violations: tuple[str, ...]

    @property
    def passed(self) -> bool:
        return bool(self.rungs) and not self.violations


def candidate_from(measurement: Measurement) -> Candidate:
    return Candidate(
        task_key=measurement.task_key,
        quant_step=measurement.quant_step,
        block_size=measurement.block_size,
        actual_kbps=measurement.actual_kbps,
        quality_db=measurement.quality_db,
    )


def pareto_frontier(candidates: Iterable[Candidate]) -> tuple[Candidate, ...]:
    """Return candidates not dominated in (bitrate, quality) space."""
    merged: dict[tuple[float, float], Candidate] = {}
    for candidate in sorted(
        candidates, key=lambda item: (item.actual_kbps, -item.quality_db)
    ):
        key = (
            round(candidate.actual_kbps, 6),
            round(candidate.quality_db, 6),
        )
        merged.setdefault(key, candidate)
    frontier: list[Candidate] = []
    best_quality = float("-inf")
    for candidate in sorted(
        merged.values(), key=lambda item: (item.actual_kbps, -item.quality_db)
    ):
        # Equal-quality, higher-bitrate entries are dominated by the first.
        if candidate.quality_db > best_quality + _EPS:
            frontier.append(candidate)
            best_quality = candidate.quality_db
    return tuple(frontier)


def check_monotonicity(rungs: Sequence[Rung]) -> tuple[str, ...]:
    """Return human-readable violations of the two ladder invariants.

    Bitrate must be strictly increasing and quality must not decrease from
    one rung to the next.  ``build_ladder`` constructs rungs from a Pareto
    frontier, so a non-empty result indicates corrupted or hand-built input;
    the check stays public so it can be exercised directly.
    """
    violations: list[str] = []
    for previous, current in zip(rungs, rungs[1:]):
        if not current.actual_kbps > previous.actual_kbps + _EPS:
            violations.append(
                f"rung {current.index} bitrate {current.actual_kbps:.3f} kbps is not"
                f" above rung {previous.index} ({previous.actual_kbps:.3f} kbps)"
            )
        if current.quality_db + _EPS < previous.quality_db:
            violations.append(
                f"rung {current.index} quality {current.quality_db:.3f} dB is below"
                f" rung {previous.index} ({previous.quality_db:.3f} dB)"
            )
    return tuple(violations)


def build_ladder(
    measurements: Sequence[Measurement], targets: Sequence[int]
) -> LadderResult:
    """Select rungs for each target and validate ladder monotonicity."""
    unique_targets = tuple(dict.fromkeys(int(t) for t in targets))
    frontier = pareto_frontier(candidate_from(item) for item in measurements)

    chosen: dict[str, Candidate] = {}
    rung_targets: dict[str, list[int]] = {}
    infeasible: list[int] = []
    for target in sorted(unique_targets):
        feasible = [item for item in frontier if item.actual_kbps <= target]
        if not feasible:
            infeasible.append(target)
            continue
        best = max(feasible, key=lambda item: (item.quality_db, -item.actual_kbps))
        chosen[best.task_key] = best
        rung_targets.setdefault(best.task_key, []).append(target)

    ordered = sorted(chosen.values(), key=lambda item: item.actual_kbps)
    rungs = tuple(
        Rung(
            index=index + 1,
            task_key=item.task_key,
            quant_step=item.quant_step,
            block_size=item.block_size,
            actual_kbps=item.actual_kbps,
            quality_db=item.quality_db,
            targets=tuple(rung_targets[item.task_key]),
        )
        for index, item in enumerate(ordered)
    )

    violations = check_monotonicity(rungs)
    bitrate_ok = not any("bitrate" in message for message in violations)
    quality_ok = not any("quality" in message for message in violations)
    return LadderResult(
        rungs=rungs,
        infeasible_targets=tuple(infeasible),
        feasible_targets=tuple(
            target for target in unique_targets if target not in infeasible
        ),
        bitrate_strictly_increasing=bitrate_ok,
        quality_non_decreasing=quality_ok,
        violations=violations,
    )
