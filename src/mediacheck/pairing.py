"""Track matching between baseline and candidate models.

Pairing is by track kind and ordinal.  When ordinal pairing would hide a
plausible alternative match (the same codec at a different ordinal), the
pairing is reported as ambiguous with the candidate track numbers listed
instead of silently re-pairing or reporting a bare codec change.
"""

from __future__ import annotations

from .codecs import normalize_codec
from .model import Mismatch, Track, ValidationResult


def ignored(name: str, ignored_fields: set[str]) -> bool:
    return name in ignored_fields or f"codec_detail.{name}" in ignored_fields


def check_ordinal_types(result: ValidationResult, base: list[Track],
                        cand: list[Track]) -> None:
    """Report a kind change at the same position instead of a silent re-pair."""
    for index in range(min(len(base), len(cand))):
        left, right = base[index], cand[index]
        if left.kind == right.kind:
            continue
        alternatives = [c.track_id for c in cand
                        if c.kind == left.kind and c is not right]
        result.mismatches.append(Mismatch(
            kind="type_mismatch",
            message=(f"position {index + 1} is {left.kind} in the baseline "
                     f"but {right.kind} in the candidate"),
            baseline_track_id=left.track_id,
            candidate_track_id=right.track_id,
            field="kind",
            baseline_value=left.kind,
            candidate_value=right.kind,
            offset=right.offset,
            candidates=alternatives,
        ))


def pair_group(result: ValidationResult, base: list[Track],
               cand: list[Track], base_group: list[int],
               cand_group: list[int]) -> list[tuple[int, int]]:
    """Pair by ordinal, flagging codec matches at other ordinals.

    An ambiguous pair is still paired positionally for the remaining field
    checks, but the ambiguity itself is recorded: it is evidence that the
    track order drifted, which a positional comparison alone would miss.
    """
    pairs: list[tuple[int, int]] = []
    for position, (bi, ci) in enumerate(zip(base_group, cand_group)):
        b_track, c_track = base[bi], cand[ci]
        if ignored("codec", set(result.ignored_fields)):
            pairs.append((bi, ci))
            continue
        baseline_codec = normalize_codec(b_track.codec)
        if baseline_codec == normalize_codec(c_track.codec):
            pairs.append((bi, ci))
            continue
        alternatives = [
            cand[j].track_id
            for j in cand_group[position + 1:]
            if normalize_codec(cand[j].codec) == baseline_codec
        ]
        if alternatives:
            result.mismatches.append(Mismatch(
                kind="ambiguous_pairing",
                message=(
                    f"{b_track.kind} track {b_track.track_id} "
                    f"({b_track.codec}) matches candidate track(s) "
                    f"{alternatives} by codec, not the ordinal neighbour "
                    f"{c_track.track_id} ({c_track.codec})"),
                baseline_track_id=b_track.track_id,
                candidate_track_id=c_track.track_id,
                field="codec",
                baseline_value=baseline_codec,
                candidate_value=normalize_codec(c_track.codec),
                offset=c_track.offset,
                candidates=alternatives,
            ))
        pairs.append((bi, ci))
    return pairs
