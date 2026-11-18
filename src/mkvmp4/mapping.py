"""Track mapping: stable, MP4-compatible track selection."""
from __future__ import annotations

from .matroska import (
    SUPPORTED_CODECS, TRACK_TYPE_AUDIO, TRACK_TYPE_VIDEO, Track,
)

_TYPE_ORDER = {TRACK_TYPE_VIDEO: 0, TRACK_TYPE_AUDIO: 1}
_TYPE_NAME = {TRACK_TYPE_VIDEO: "video", TRACK_TYPE_AUDIO: "audio"}


def unsupported_reason(track: Track) -> str | None:
    """Why a track cannot be carried in MP4, or None if it can."""
    if track.type not in _TYPE_ORDER:
        return f"track type {track.type} cannot be carried by MP4"
    if track.codec_id not in SUPPORTED_CODECS:
        return f"codec {track.codec_id!r} is not AVC/HEVC/AAC"
    if not track.codec_private:
        return "missing codec private data (parameter sets)"
    return None


def build_track_map(tracks: list[Track]) -> dict:
    """Map Matroska tracks to MP4 tracks.

    Ordering is stable: video before audio, default-flagged tracks first,
    then original track number — input file order cannot cause drift.
    Unsupported tracks are skipped with an explicit reason.
    """
    usable, skipped = [], []
    for t in tracks:
        reason = unsupported_reason(t)
        if reason is not None:
            skipped.append({"track_number": t.number,
                            "codec_id": t.codec_id, "reason": reason})
        else:
            usable.append(t)
    usable.sort(key=lambda t: (_TYPE_ORDER[t.type], not t.default, t.number))
    mapped = []
    for mp4_id, t in enumerate(usable, start=1):
        mapped.append({
            "mp4_track_id": mp4_id,
            "track_number": t.number,
            "type": _TYPE_NAME[t.type],
            "codec": SUPPORTED_CODECS[t.codec_id],
            "default": t.default,
            "language": t.language,
        })
    return {"tracks": mapped, "skipped": skipped}
