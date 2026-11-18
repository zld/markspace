"""Track identity matching and difference generation.

Tracks are matched between a reference file and a candidate by
(track_id, media_type), so track order changes are tolerated. Tracks with
no counterpart and fields that cannot be compared (missing on either
side) are flagged with distinct mismatch kinds.
"""
from __future__ import annotations

from dataclasses import asdict, dataclass

PARAM_FIELDS = ("width", "height", "sample_rate", "channels")


@dataclass
class Mismatch:
    kind: str
    track_id: int | None = None
    media_type: str | None = None
    field: str | None = None
    expected: object = None
    actual: object = None
    detail: str = ""

    def to_dict(self) -> dict:
        return {k: v for k, v in asdict(self).items()
                if v is not None and v != ""}

    def sort_key(self):
        return (self.kind, self.track_id is None, self.track_id or 0,
                self.field or "")


def _track_sort_key(key):
    track_id, media_type = key
    return (track_id is None, track_id or 0, media_type or "")


def _track_map(info) -> dict:
    return {(t.track_id, t.media_type): t for t in info.tracks}


def check_internal(info, tolerance_ms: float) -> list[Mismatch]:
    """Consistency checks within one file (duplicates, declared vs actual)."""
    mismatches: list[Mismatch] = []
    seen: dict = {}
    for track in info.tracks:
        key = (track.track_id, track.media_type)
        if key in seen:
            mismatches.append(Mismatch(
                "duplicate_track_id", track.track_id, track.media_type,
                detail=f"track identity {key} appears more than once"))
        else:
            seen[key] = track
        if track.duration_ms is not None and \
                track.sample_duration_ms is not None:
            delta = abs(track.sample_duration_ms - track.duration_ms)
            if delta > tolerance_ms:
                mismatches.append(Mismatch(
                    "duration_delta", track.track_id, track.media_type,
                    "sample_table_duration_ms", track.duration_ms,
                    track.sample_duration_ms,
                    f"declared duration and sample table differ by "
                    f"{delta:.1f} ms"))
    return mismatches


def compare_to_reference(reference, candidate,
                         tolerance_ms: float) -> list[Mismatch]:
    """Compare a candidate file against the reference, order-insensitive."""
    mismatches: list[Mismatch] = []
    ref = _track_map(reference)
    cand = _track_map(candidate)
    for key in sorted(ref, key=_track_sort_key):
        if key not in cand:
            mismatches.append(Mismatch(
                "track_missing", key[0], key[1],
                detail="present in reference, absent in candidate"))
    for key in sorted(cand, key=_track_sort_key):
        if key not in ref:
            mismatches.append(Mismatch(
                "track_added", key[0], key[1],
                detail="absent in reference, present in candidate"))
    for key in sorted(ref.keys() & cand.keys(), key=_track_sort_key):
        ref_track, cand_track = ref[key], cand[key]
        track_id, media_type = key
        if ref_track.codec is None or cand_track.codec is None:
            if ref_track.codec != cand_track.codec:
                mismatches.append(Mismatch(
                    "incomparable_field", track_id, media_type, "codec",
                    ref_track.codec, cand_track.codec,
                    "codec missing on one side"))
        elif ref_track.codec != cand_track.codec:
            mismatches.append(Mismatch(
                "codec_changed", track_id, media_type, "codec",
                ref_track.codec, cand_track.codec))
        for field_name in PARAM_FIELDS:
            expected = getattr(ref_track, field_name)
            actual = getattr(cand_track, field_name)
            if expected is None or actual is None:
                if expected != actual:
                    mismatches.append(Mismatch(
                        "incomparable_field", track_id, media_type,
                        field_name, expected, actual,
                        "parameter missing on one side"))
            elif expected != actual:
                mismatches.append(Mismatch(
                    "param_changed", track_id, media_type, field_name,
                    expected, actual))
        if ref_track.duration_ms is None or cand_track.duration_ms is None:
            if ref_track.duration_ms != cand_track.duration_ms:
                mismatches.append(Mismatch(
                    "incomparable_field", track_id, media_type,
                    "duration_ms", ref_track.duration_ms,
                    cand_track.duration_ms,
                    "duration missing on one side"))
        else:
            delta = abs(cand_track.duration_ms - ref_track.duration_ms)
            if delta > tolerance_ms:
                mismatches.append(Mismatch(
                    "duration_delta", track_id, media_type, "duration_ms",
                    ref_track.duration_ms, cand_track.duration_ms,
                    f"track durations differ by {delta:.1f} ms"))
    return mismatches


def duration_deltas(info) -> list[float]:
    """Declared-vs-sample-table duration deltas within one file (ms)."""
    deltas = []
    for track in info.tracks:
        if track.duration_ms is not None and \
                track.sample_duration_ms is not None:
            deltas.append(abs(track.sample_duration_ms - track.duration_ms))
    return deltas


def cross_duration_deltas(reference, candidate) -> list[float]:
    """Per-track declared-duration deltas between two files (ms)."""
    ref = _track_map(reference)
    cand = _track_map(candidate)
    deltas = []
    for key in ref.keys() & cand.keys():
        r_ms, c_ms = ref[key].duration_ms, cand[key].duration_ms
        if r_ms is not None and c_ms is not None:
            deltas.append(abs(c_ms - r_ms))
    return deltas
