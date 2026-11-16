"""Unified track model shared by both container parsers.

Raw codec identifiers from MP4 sample entries and Matroska CodecID strings
are normalized onto one vocabulary so tracks can be matched across
containers.  Fields the source file does not provide stay ``None``: a
missing value is never replaced by a fabricated default.
"""

from __future__ import annotations

from dataclasses import dataclass, asdict

# Normalized codec name -> raw identifiers that map to it.
_VIDEO_CODECS = {
    "avc": {"avc1", "avc3", "V_MPEG4/ISO/AVC"},
    "hevc": {"hvc1", "hev1", "V_MPEGH/ISO/HEVC"},
    "vp9": {"vp09", "V_VP9"},
    "vp8": {"vp08", "V_VP8"},
    "av1": {"av01", "V_AV1"},
    "mpeg4-visual": {"mp4v", "V_MPEG4/ISO/ASP"},
}
_AUDIO_CODECS = {
    "aac": {"mp4a", "A_AAC"},
    "opus": {"opus", "A_OPUS"},
    "mp3": {"mp3", ".mp3", "A_MPEG/L3"},
    "vorbis": {"vorbis", "A_VORBIS"},
    "ac3": {"ac-3", "A_AC3"},
    "ec3": {"ec-3", "A_EAC3"},
    "pcm": {"sowt", "in24", "A_PCM/INT/LIT"},
}
_SUBTITLE_CODECS = {
    "webvtt": {"wvtt", "S_TEXT/WEBVTT"},
    "ass": {"S_TEXT/ASS"},
    "srt": {"S_TEXT/UTF8"},
}


def normalize_codec(raw: str | None, track_type: str | None) -> str | None:
    """Map a container-specific codec id onto the shared vocabulary."""
    if raw is None:
        return None
    token = raw.strip()
    # MP4 sample entry kinds sometimes carry a trailing dot+version.
    head = token.split(".", 1)[0]
    tables = {
        "video": _VIDEO_CODECS,
        "audio": _AUDIO_CODECS,
        "subtitle": _SUBTITLE_CODECS,
    }
    for table in tables.values():
        for name, raws in table.items():
            if token in raws or head in raws:
                return name
    return token.lower()


@dataclass(slots=True)
class Track:
    file_path: str
    track_id: int | None
    track_type: str | None
    codec: str | None
    raw_codec: str | None
    duration_ms: int | None
    sample_rate: int | float | None
    channels: int | None
    width: int | None
    height: int | None

    @classmethod
    def from_raw(cls, raw: dict, file_path: str) -> "Track":
        track_type = raw.get("type")
        raw_codec = raw.get("codec")
        return cls(
            file_path=file_path,
            track_id=raw.get("track_id"),
            track_type=track_type,
            codec=normalize_codec(raw_codec, track_type),
            raw_codec=raw_codec,
            duration_ms=raw.get("duration_ms"),
            sample_rate=raw.get("sample_rate"),
            channels=raw.get("channels"),
            width=raw.get("width"),
            height=raw.get("height"),
        )

    def stable_params(self) -> dict:
        """Parameters that must agree for tracks to be considered the same
        logical track across a material group."""
        if self.track_type == "video":
            return {"width": self.width, "height": self.height}
        if self.track_type == "audio":
            return {"sample_rate": self.sample_rate, "channels": self.channels}
        return {}

    def match_key(self) -> tuple:
        params = self.stable_params()
        return (self.track_type, self.codec,
                tuple(sorted(params.items())))

    def to_dict(self) -> dict:
        return asdict(self)


def build_tracks(raw: dict, file_path: str) -> list[Track]:
    return [Track.from_raw(t, file_path) for t in raw.get("tracks", [])]


def codec_summary(tracks: list[Track]) -> dict[str, int]:
    """Count tracks per normalized codec (missing codec reported as null)."""
    summary: dict[str, int] = {}
    for track in tracks:
        key = track.codec if track.codec is not None else "null"
        summary[key] = summary.get(key, 0) + 1
    return dict(sorted(summary.items()))
