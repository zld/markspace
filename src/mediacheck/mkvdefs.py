"""Matroska element IDs and track-type mapping.

ID values include the EBML marker bit, matching the definitions in the
Matroska specification.  Kept apart from the parsers so the block scanner
and the track reader can both import them without a cycle.
"""

from __future__ import annotations

from .model import KIND_AUDIO, KIND_OTHER, KIND_SUBTITLE, KIND_VIDEO

EBML_HEADER = 0x1A45DFA3
DOCTYPE = 0x4282
SEGMENT = 0x18538067
INFO = 0x1549A966
TIMECODE_SCALE = 0x2AD7B1
DURATION = 0x4489
TITLE = 0x7BA9
TRACKS = 0x1654AE6B
TRACK_ENTRY = 0xAE
TRACK_NUMBER = 0xD7
TRACK_UID = 0x73C5
TRACK_TYPE = 0x83
CODEC_ID = 0x86
CODEC_PRIVATE = 0x63A2
DEFAULT_DURATION = 0x23E383
LANGUAGE = 0x22B59C
VIDEO = 0xE0
PIXEL_WIDTH = 0xB0
PIXEL_HEIGHT = 0xBA
AUDIO = 0xE1
SAMPLING_FREQUENCY = 0xB5
CHANNELS = 0x9F
BIT_DEPTH = 0x6264
CLUSTER = 0x1F43B675
CLUSTER_TIMECODE = 0xE7
SIMPLE_BLOCK = 0xA3
BLOCK_GROUP = 0xA0
BLOCK = 0xA1
BLOCK_DURATION = 0x9B
VOID = 0xEC

TRACK_TYPES = {
    1: KIND_VIDEO,
    2: KIND_AUDIO,
    17: KIND_SUBTITLE,
}

DEFAULT_TIMECODE_SCALE = 1_000_000  # nanoseconds per timecode tick


def kind_for_track_type(value: int) -> str:
    return TRACK_TYPES.get(value, KIND_OTHER)
