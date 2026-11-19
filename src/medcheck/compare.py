"""Track matching and consistency checks.

Matching is stable and order-independent: tracks pair on (kind, codec)
plus a duration window. Duplicate candidates are flagged as ambiguous
instead of being picked silently. Per-file checks compare container
duration against each track duration and compare sample/format
parameters across tracks of the same kind, within a configurable
tolerance (default 20 ms).
"""

from __future__ import annotations

DEFAULT_TOLERANCE_MS = 20.0

_MISSING_LANGUAGES = (None, "", "und")


def _duration_close(a: float | None, b: float | None, tolerance_ms: float) -> bool:
    if a is None or b is None:
        return True  # unknown durations never exclude a candidate
    return abs(a - b) <= tolerance_ms


def match_tracks(reference: list[dict], candidates: list[dict],
                 tolerance_ms: float = DEFAULT_TOLERANCE_MS
                 ) -> tuple[list[tuple[int, int]], list[int]]:
    """Match reference tracks to candidates independent of track order.

    Returns (pairs, ambiguous) where pairs are (ref_index, cand_index)
    and ambiguous lists reference indexes with more than one viable
    candidate; ambiguous references are left unmatched, never guessed.
    """
    pairs: list[tuple[int, int]] = []
    ambiguous: list[int] = []
    used: set[int] = set()
    for ri, ref in enumerate(reference):
        viable = [
            ci for ci, cand in enumerate(candidates)
            if ci not in used
            and cand.get("kind") == ref.get("kind")
            and cand.get("codec") == ref.get("codec")
            and _duration_close(ref.get("duration_ms"), cand.get("duration_ms"),
                                tolerance_ms)
        ]
        if len(viable) == 1:
            pairs.append((ri, viable[0]))
            used.add(viable[0])
        elif len(viable) > 1:
            ambiguous.append(ri)
    return pairs, ambiguous


def check_file(record: dict, tolerance_ms: float = DEFAULT_TOLERANCE_MS
               ) -> tuple[list[dict], float]:
    """Check one probe record; return (mismatches, max_duration_delta_ms)."""
    mismatches: list[dict] = []
    tracks = record.get("tracks", [])
    container_ms = record.get("container_duration_ms")
    max_delta = 0.0

    for idx, track in enumerate(tracks):
        dur = track.get("duration_ms")
        if dur is not None and container_ms is not None:
            delta = dur - container_ms
            max_delta = max(max_delta, abs(delta))
            if abs(delta) > tolerance_ms:
                mismatches.append({
                    "kind": "duration_mismatch",
                    "track": idx,
                    "delta_ms": round(delta, 3),
                    "detail": (
                        f"track {idx} ({track.get('kind')}/{track.get('codec')}) "
                        f"duration {dur:.1f} ms differs from container "
                        f"{container_ms:.1f} ms by {delta:+.1f} ms"),
                })
        declared = track.get("declared_duration_ms")
        if dur is not None and declared is not None:
            delta = dur - declared
            max_delta = max(max_delta, abs(delta))
            if abs(delta) > tolerance_ms:
                mismatches.append({
                    "kind": "declared_duration_mismatch",
                    "track": idx,
                    "delta_ms": round(delta, 3),
                    "detail": (
                        f"track {idx} media duration {dur:.1f} ms differs from "
                        f"declared header duration {declared:.1f} ms "
                        f"by {delta:+.1f} ms"),
                })
        if (record.get("format") == "mov" and track.get("kind") == "audio"
                and track.get("language") in _MISSING_LANGUAGES):
            mismatches.append({
                "kind": "missing_language",
                "track": idx,
                "delta_ms": None,
                "detail": f"track {idx} (audio/{track.get('codec')}) has no "
                          f"language tag",
            })

    for kind, fields in (("audio", ("sample_rate", "channels")),
                         ("video", ("width", "height"))):
        group = [(i, t) for i, t in enumerate(tracks) if t.get("kind") == kind]
        if len(group) < 2:
            continue
        for field_name in fields:
            values = {t.get(field_name) for _, t in group}
            values.discard(None)
            if len(values) > 1:
                mismatches.append({
                    "kind": "param_mismatch",
                    "track": None,
                    "delta_ms": None,
                    "detail": (
                        f"{kind} tracks disagree on {field_name}: "
                        f"tracks {[i for i, _ in group]} have "
                        f"{[t.get(field_name) for _, t in group]}"),
                })
    return mismatches, max_delta
