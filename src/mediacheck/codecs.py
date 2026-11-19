"""Codec identifier normalisation shared by compare and packer.

MP4/MOV sample-entry fourccs and Matroska CodecIDs name the same encoding
differently.  Comparing the raw strings would report a codec change for
every cross-container comparison, so both sides are mapped to a family name
first.
"""

from __future__ import annotations

# Values are lower-case family names; keys keep their conventional casing
# because Matroska CodecIDs are case sensitive.
CODEC_ALIASES = {
    "avc1": "h264", "avc3": "h264", "V_MPEG4/ISO/AVC": "h264",
    "hvc1": "h265", "hev1": "h265", "V_MPEGH/ISO/HEVC": "h265",
    "vp09": "vp9", "V_VP9": "vp9",
    "av01": "av1", "V_AV1": "av1",
    "mp4v": "mpeg4-part2", "V_MPEG4/ISO/ASP": "mpeg4-part2",
    "mp4a": "aac", "A_AAC": "aac", "A_AAC/MPEG4/LC": "aac",
    "ac-3": "ac3", "A_AC3": "ac3",
    "ec-3": "eac3", "A_EAC3": "eac3",
    "Opus": "opus", "A_OPUS": "opus",
    "fLaC": "flac", "A_FLAC": "flac",
    "A_VORBIS": "vorbis",
    "lpcm": "pcm", "sowt": "pcm", "twos": "pcm", "in24": "pcm",
}


def normalize_codec(codec: str) -> str:
    """Map a container-specific codec identifier to a family name.

    Unknown identifiers fall back to their lower-cased form, so a codec we
    do not know still compares equal to itself and unequal to anything else.
    """
    if codec in CODEC_ALIASES:
        return CODEC_ALIASES[codec]
    lowered = codec.lower()
    for alias, family in CODEC_ALIASES.items():
        if alias.lower() == lowered:
            return family
    return lowered
