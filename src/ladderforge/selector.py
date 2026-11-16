"""Select a monotonic bitrate ladder from scan results.

Candidates are grouped into tiers by normalized target bitrate. Within a
tier, dominated candidates (another candidate has both higher-or-equal
quality and lower-or-equal actual bitrate) are dropped. A dynamic
program then finds the longest chain across tiers with strictly
increasing actual bitrate and non-decreasing quality score. Fewer than
two rungs is a hard failure -- never a forged PASS.
"""

from __future__ import annotations

from dataclasses import dataclass

MIN_RUNGS = 2


class LadderError(Exception):
    """A valid monotonic ladder cannot be formed (exit code 4)."""


@dataclass(frozen=True)
class Candidate:
    tier: int
    target_kbps: int
    actual_kbps: float
    quality: float
    alias_count: int
    params_key: str


def non_dominated(cands: list[Candidate]) -> list[Candidate]:
    """Drop candidates dominated on both quality and actual bitrate."""
    keep: list[Candidate] = []
    for c in cands:
        dominated = any(
            o is not c
            and o.quality >= c.quality
            and o.actual_kbps <= c.actual_kbps
            and (o.quality > c.quality or o.actual_kbps < c.actual_kbps)
            for o in cands
        )
        if not dominated:
            keep.append(c)
    return keep


def _to_candidates(rows: list[dict]) -> list[Candidate]:
    targets = sorted({r["target_kbps"] for r in rows})
    tier_of = {t: i for i, t in enumerate(targets)}
    return [
        Candidate(
            tier=tier_of[r["target_kbps"]],
            target_kbps=r["target_kbps"],
            actual_kbps=float(r["actual_kbps"]),
            quality=float(r["quality_score"]),
            alias_count=int(r["alias_count"]),
            params_key=r["params_key"],
        )
        for r in rows
    ]


def select_ladder(rows: list[dict], min_rungs: int = MIN_RUNGS) -> list[Candidate]:
    """Longest monotonic chain over per-tier non-dominated candidates."""
    if not rows:
        raise LadderError("no completed results")
    by_tier: dict[int, list[Candidate]] = {}
    for cand in _to_candidates(rows):
        by_tier.setdefault(cand.tier, []).append(cand)
    cands: list[Candidate] = []
    for tier in sorted(by_tier):
        cands.extend(non_dominated(by_tier[tier]))
    cands.sort(key=lambda c: (c.tier, c.actual_kbps, -c.quality))
    n = len(cands)
    # dp[i] = (chain length, total quality) of best chain ending at i.
    dp = [(1, c.quality) for i, c in enumerate(cands)]
    prev = [-1] * n
    for i in range(n):
        for j in range(i):
            a, b = cands[j], cands[i]
            if a.tier >= b.tier:
                continue
            if a.actual_kbps >= b.actual_kbps or a.quality > b.quality:
                continue
            cand_len = dp[j][0] + 1
            cand_q = dp[j][1] + b.quality
            if (cand_len, cand_q) > dp[i]:
                dp[i] = (cand_len, cand_q)
                prev[i] = j
    best = max(range(n), key=lambda i: dp[i])
    chain: list[Candidate] = []
    while best >= 0:
        chain.append(cands[best])
        best = prev[best]
    chain.reverse()
    if len(chain) < min_rungs:
        raise LadderError(
            f"only {len(chain)} monotonic rung(s); need at least {min_rungs}"
        )
    return chain
