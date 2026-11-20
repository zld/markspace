"""Bitrate-ladder selection with monotonicity guarantees.

Targets are deduplicated (duplicates become aliases of one rung).
A small DP picks one candidate per rung such that:

  * actual_bps is strictly increasing across rungs
  * quality_score is non-decreasing across rungs
  * every chosen candidate respects its rung's target cap

Rungs that cannot be satisfied are reported as ``unreachable`` and
counted as violations; bitrates are never fabricated.
"""
from __future__ import annotations

from dataclasses import dataclass, field


@dataclass
class Rung:
    target_bps: int
    aliases: list[int] = field(default_factory=list)
    status: str = "unreachable"
    actual_bps: int | None = None
    quality_score: float | None = None
    param_key: str | None = None

    def to_dict(self) -> dict:
        out = {
            "target_bps": self.target_bps,
            "status": self.status,
            "aliases": sorted(self.aliases),
        }
        if self.status == "ok":
            out["actual_bps"] = self.actual_bps
            out["quality_score"] = self.quality_score
            out["param_key"] = self.param_key
        return out


def unique_targets(targets: list[int]) -> tuple[list[int], int]:
    """Sort targets, merge duplicates; returns (unique, alias_count)."""
    uniq = sorted(set(targets))
    return uniq, len(targets) - len(uniq)


def select_ladder(candidates: list[dict], targets: list[int]) -> tuple[list[Rung], int]:
    """Pick rungs via DP; returns (rungs, violation_count)."""
    uniq, _ = unique_targets(targets)
    rungs = [Rung(target_bps=t, aliases=[t for x in targets if x == t]) for t in uniq]
    if not uniq:
        return rungs, 0
    cands = sorted(candidates, key=lambda c: (c["actual_bps"], c["param_key"]))
    # dp[i][j] = best total quality using candidate j for rung i
    # parent[i][j] = candidate index chosen for rung i-1
    NEG = float("-inf")
    dp: list[list[float]] = []
    parent: list[list[int | None]] = []
    for i, rung in enumerate(rungs):
        row = [NEG] * len(cands)
        prow: list[int | None] = [None] * len(cands)
        for j, c in enumerate(cands):
            if c["actual_bps"] > rung.target_bps:
                continue
            if i == 0:
                row[j] = c["quality_score"]
                continue
            best_q, best_k = NEG, None
            for k in range(j):
                prev = dp[i - 1][k]
                if prev == NEG:
                    continue
                pk = cands[k]
                if pk["actual_bps"] >= c["actual_bps"]:
                    continue  # strictly increasing bitrate
                if pk["quality_score"] > c["quality_score"]:
                    continue  # non-decreasing quality
                if prev > best_q:
                    best_q, best_k = prev, k
            if best_k is not None:
                row[j] = best_q + c["quality_score"]
                prow[j] = best_k
        dp.append(row)
        parent.append(prow)
    # reconstruct the best chain for the last rung
    last = max(range(len(cands)), key=lambda j: dp[-1][j], default=None)
    if last is None or not cands or dp[-1][last] == NEG:
        return rungs, _mark_unreachable(rungs, dp, cands)
    chain: list[int | None] = [None] * len(rungs)
    j: int | None = last
    for i in range(len(rungs) - 1, -1, -1):
        chain[i] = j
        j = parent[i][j] if j is not None else None
    violations = 0
    for i, idx in enumerate(chain):
        if idx is None:
            violations += 1
            continue
        c = cands[idx]
        rungs[i].status = "ok"
        rungs[i].actual_bps = c["actual_bps"]
        rungs[i].quality_score = c["quality_score"]
        rungs[i].param_key = c["param_key"]
    return rungs, violations


def _mark_unreachable(rungs: list[Rung], dp, cands) -> int:
    """Fill any rungs the full chain missed; count unreachable ones."""
    # Greedily serve prefix rungs that have at least one feasible candidate.
    violations = 0
    prev_bps, prev_q = 0, float("-inf")
    for i, rung in enumerate(rungs):
        best = None
        for c in cands:
            if c["actual_bps"] > rung.target_bps:
                continue
            if c["actual_bps"] <= prev_bps or c["quality_score"] < prev_q:
                continue
            if best is None or c["quality_score"] > best["quality_score"]:
                best = c
        if best is None:
            violations += 1
            continue
        rung.status = "ok"
        rung.actual_bps = best["actual_bps"]
        rung.quality_score = best["quality_score"]
        rung.param_key = best["param_key"]
        prev_bps, prev_q = best["actual_bps"], best["quality_score"]
    return violations


def validate_ladder(rungs: list[Rung]) -> dict:
    """Check monotonicity of the selected ladder."""
    ok_rungs = [r for r in rungs if r.status == "ok"]
    bitrate_mono = all(
        b.actual_bps < a.actual_bps  # type: ignore[operator]
        for a, b in zip(ok_rungs[1:], ok_rungs)
    )
    quality_mono = all(
        b.quality_score <= a.quality_score  # type: ignore[operator]
        for a, b in zip(ok_rungs[1:], ok_rungs)
    )
    caps_ok = all(
        r.actual_bps <= r.target_bps for r in ok_rungs  # type: ignore[operator]
    )
    violations = sum(1 for r in rungs if r.status != "ok")
    if not caps_ok:
        violations += 1
    return {
        "bitrate_monotonic": bitrate_mono,
        "quality_monotonic": quality_mono,
        "within_targets": caps_ok,
        "violations": violations,
    }
