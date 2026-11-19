"""Track selection and Matroska track number -> MP4 track ID mapping.

Only H.264/AVC video and AAC audio are accepted. Unsupported *codecs* and
encrypted or header-compressed tracks are fatal (the spec demands an explicit
failure with the track number); unimplemented track *kinds* such as subtitles
are skipped with a recorded reason instead of being dropped silently.
"""

from __future__ import annotations

from dataclasses import dataclass, field

from .matroska import (
    SUPPORTED_CODECS,
    TRACK_TYPE_AUDIO,
    TRACK_TYPE_SUBTITLE,
    TRACK_TYPE_VIDEO,
    Track,
)


class MappingError(ValueError):
    """Raised when no supported track exists or a track is unusable."""


@dataclass(slots=True)
class TrackMappingEntry:
    mkv_track_number: int
    mp4_track_id: int | None
    decision: str  # "accepted" | "skipped" | "rejected"
    reason: str
    codec_id: str
    track_type: int


@dataclass(slots=True)
class TrackMapping:
    entries: list[TrackMappingEntry] = field(default_factory=list)
    accepted: dict[int, int] = field(default_factory=dict)  # mkv num -> mp4 id

    @property
    def accepted_numbers(self) -> list[int]:
        return [e.mkv_track_number for e in self.entries if e.decision == "accepted"]

    @property
    def skipped(self) -> list[TrackMappingEntry]:
        return [e for e in self.entries if e.decision == "skipped"]

    @property
    def rejected(self) -> list[TrackMappingEntry]:
        return [e for e in self.entries if e.decision == "rejected"]


def select_tracks(tracks: list[Track]) -> TrackMapping:
    mapping = TrackMapping()
    next_mp4_id = 1
    for track in tracks:  # input order -> stable MP4 track IDs
        entry = TrackMappingEntry(
            mkv_track_number=track.number,
            mp4_track_id=None,
            decision="skipped",
            reason="",
            codec_id=track.codec_id,
            track_type=track.type,
        )
        if not track.enabled:
            entry.reason = "track is disabled (FlagEnabled=0)"
        elif track.encrypted:
            entry.decision = "rejected"
            entry.reason = "encrypted/content-encoded tracks are not supported"
        elif track.header_compression:
            entry.decision = "rejected"
            entry.reason = "header compression cannot be remuxed without re-encoding"
        elif track.type == TRACK_TYPE_VIDEO:
            if track.codec_id == "V_MPEG4/ISO/AVC":
                entry.decision, entry.reason = "accepted", "H.264/AVC video"
            else:
                entry.decision = "rejected"
                entry.reason = f"unsupported video codec {track.codec_id!r}"
        elif track.type == TRACK_TYPE_AUDIO:
            if track.codec_id == "A_AAC":
                entry.decision, entry.reason = "accepted", "AAC audio"
            else:
                entry.decision = "rejected"
                entry.reason = f"unsupported audio codec {track.codec_id!r}"
        elif track.type == TRACK_TYPE_SUBTITLE:
            entry.reason = "subtitle tracks are not implemented"
        else:
            entry.reason = f"track type {track.type} is not implemented"

        if entry.decision == "accepted":
            entry.mp4_track_id = next_mp4_id
            mapping.accepted[track.number] = next_mp4_id
            next_mp4_id += 1
        mapping.entries.append(entry)

    if mapping.rejected:
        details = "; ".join(
            f"track {e.mkv_track_number}: {e.reason}" for e in mapping.rejected
        )
        raise MappingError(f"unusable tracks ({details}); only "
                           f"{sorted(SUPPORTED_CODECS)} are supported")
    if not mapping.accepted:
        raise MappingError(
            "no supported H.264/AVC or AAC track found in input"
        )
    return mapping
