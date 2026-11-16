"""Unit tests for the EBML parser, codec mapping and timeline policy.

Run directly with ``.venv/bin/python tests/test_core.py`` (no third-party
test runner is required); the script exits non-zero on the first failure.
"""

from __future__ import annotations

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "src"))

from mkvmp4.codec import (  # noqa: E402
    _map_avc, map_codec, opus_frame_duration,
    validate_video_block, CodecRejectedError)
from mkvmp4.ebml import EbmlParser, Limits, read_vint  # noqa: E402
from mkvmp4.errors import EbmlError, LimitExceededError  # noqa: E402
from mkvmp4.matroska import (  # noqa: E402
    Track, Block, TRACK_TYPE_AUDIO, TRACK_TYPE_VIDEO, TRACK_TYPE_SUBTITLE)
from mkvmp4.timeline import build_timeline  # noqa: E402

PASS = 0


def check(condition: bool, label: str) -> None:
    global PASS
    if not condition:
        raise AssertionError(label)
    PASS += 1
    print(f"  ok: {label}")


def expect(exc_type, fn, label: str) -> None:
    global PASS
    try:
        fn()
    except exc_type:
        PASS += 1
        print(f"  ok: {label}")
    else:
        raise AssertionError(f"{label}: expected {exc_type.__name__}")


# --- EBML -------------------------------------------------------------------

def test_vint() -> None:
    print("test_vint")
    value, width, unknown = read_vint(b"\x81", 0, is_id=False,
                                      unknown_allowed=False)
    check((value, width, unknown) == (1, 1, False), "1-byte data VINT")
    value, width, unknown = read_vint(b"\xff" * 8, 0, is_id=False,
                                      unknown_allowed=True)
    check(width == 8 and unknown, "0xFF leading byte resolves to 8-byte width")
    expect(EbmlError,
           lambda: read_vint(b"\xff", 0, is_id=False,
                             unknown_allowed=True),
           "lone 0xFF in 1-byte buffer is a truncated VINT")
    eid, width, _ = read_vint(b"\x1a\x45\xdf\xa3", 0, is_id=True,
                              unknown_allowed=False)
    check(eid == 0x1A45DFA3 and width == 4, "element ID keeps marker bits")
    expect(EbmlError,
           lambda: read_vint(b"\x42", 0, is_id=True, unknown_allowed=False),
           "truncated ID VINT rejected")
    expect(EbmlError,
           lambda: read_vint(b"\x00\xff", 0, is_id=True,
                             unknown_allowed=False),
           "zero leading byte rejected")
    expect(EbmlError,
           lambda: read_vint(b"\xff", 0, is_id=False,
                             unknown_allowed=False),
           "unknown length rejected where not allowed")


def test_limits_and_boundaries() -> None:
    print("test_limits_and_boundaries")
    data = bytes.fromhex("1a45dfa38442868101")
    parser = EbmlParser(data, Limits(max_elements=1))
    roots = parser.iter_children(0, len(data))
    expect(LimitExceededError,
           lambda: parser.iter_children(roots[0].body_start,
                                        roots[0].body_end),
           "element count limit enforced")
    expect(LimitExceededError,
           lambda: EbmlParser(b"x" * 100, Limits(max_bytes=10)),
           "byte size limit enforced")
    buf = bytes.fromhex("1a45dfa3824283aabbcc")
    def cross() -> None:
        p = EbmlParser(buf)
        r = p.iter_children(0, len(buf))
        p.iter_children(r[0].body_start, r[0].body_end)
    expect(EbmlError, cross, "child crossing parent boundary rejected")


# --- codec ------------------------------------------------------------------

def _avcc(width_mbs: int, height_map: int) -> bytes:
    # Minimal but structurally valid avcC built with the SPS/PPS the codec
    # module accepts; reuses the fixture's bitstream builders when present.
    sys.path.insert(0, os.path.dirname(__file__))
    import make_fixtures as fx
    sps = fx.make_sps(width_mbs, height_map)
    pps = fx.make_pps()
    return (bytes([1, 66, 0, 30, 0xFF, 0xE1])
            + len(sps).to_bytes(2, "big") + sps
            + bytes([1]) + len(pps).to_bytes(2, "big") + pps)


def test_avc_mapping() -> None:
    print("test_avc_mapping")
    cfg = _avcc(20, 15)
    track = Track(1, 1, TRACK_TYPE_VIDEO, "V_MPEG4/ISO/AVC",
                  codec_private=cfg, pixel_width=320, pixel_height=240)
    mapping = _map_avc(track)
    check((mapping.width, mapping.height) == (320, 240), "SPS dimensions")
    check(mapping.nalu_length_size == 4, "4-byte NAL prefix")
    good = (5).to_bytes(4, "big") + b"\x65abcd"
    validate_video_block(mapping, good)
    check(True, "valid length-prefixed NAL block")
    expect(CodecRejectedError,
           lambda: validate_video_block(mapping, (5).to_bytes(4, "big")
                                        + b"ab"),
           "NAL crossing block boundary rejected")
    expect(CodecRejectedError,
           lambda: validate_video_block(mapping, b""),
           "empty video block rejected")
    bad = Track(1, 1, TRACK_TYPE_VIDEO, "V_MPEG4/ISO/AVC",
                codec_private=cfg, pixel_width=640, pixel_height=480)
    expect(CodecRejectedError, lambda: _map_avc(bad),
           "SPS/TrackEntry dimension mismatch rejected")


def test_aac_and_opus() -> None:
    print("test_aac_and_opus")
    sys.path.insert(0, os.path.dirname(__file__))
    import make_fixtures as fx
    aac = Track(2, 2, TRACK_TYPE_AUDIO, "A_AAC",
                codec_private=fx.make_aac_config(),
                sample_rate=48000, channels=2)
    mapping = map_codec(aac)
    check(mapping.timescale == 48000 and mapping.channels == 2,
          "AAC 48kHz stereo mapping")
    head = fx.make_opus_head()
    opus = Track(3, 3, TRACK_TYPE_AUDIO, "A_OPUS", codec_private=head,
                 channels=2)
    om = map_codec(opus)
    check(om.timescale == 48000 and om.entry_type == b"Opus",
          "Opus mapping to 48kHz Opus sample entry")
    # RFC 6716: CELT-only configs 16..19 map to 2.5/5/10/20 ms.
    check(opus_frame_duration(bytes([16 << 3])) == 120,
          "Opus 2.5ms packet duration")
    check(opus_frame_duration(bytes([19 << 3])) == 960,
          "Opus 20ms packet duration")
    expect(CodecRejectedError,
           lambda: opus_frame_duration(bytes([(17 << 3) | 3])),
           "Opus code-3 packet rejected")
    expect(CodecRejectedError,
           lambda: map_codec(Track(4, 4, TRACK_TYPE_SUBTITLE,
                                   "S_TEXT/UTF8")),
           "subtitle codec explicitly rejected")


# --- timeline ---------------------------------------------------------------

def test_timeline_policy() -> None:
    print("test_timeline_policy")
    video = Track(1, 1, TRACK_TYPE_VIDEO, "V_MPEG4/ISO/AVC",
                  default_duration_ns=40_000_000)
    neg = [Block(1, 0, 0, True, 0, b"a", pts_ns=-80_000_000),
           Block(1, 0, 0, False, 0, b"b", pts_ns=-40_000_000),
           Block(1, 0, 0, False, 0, b"c", pts_ns=0)]
    tl = build_timeline(video, neg)
    check(tl.stats.negative_start_shift_ms == 80.0,
          "negative start shifted to zero")
    check(tl.stats.rewritten_first_pts_ms == 0.0, "first PTS at zero")
    bframe = [Block(1, 0, 0x80, True, 0, b"x", pts_ns=p)
              for p in (40_000_000, 0, 80_000_000)]
    tl2 = build_timeline(video, bframe)
    dts = [s.dts for s in tl2.samples]
    check(all(b > a for a, b in zip(dts, dts[1:])), "DTS strictly increasing")
    check(any(s.cts_offset < 0 for s in tl2.samples),
          "negative ctts expresses B-frame reorder")
    dup = [Block(1, 0, 0, True, 0, b"x", pts_ns=0),
           Block(1, 0, 0, False, 0, b"y", pts_ns=0),
           Block(1, 0, 0, False, 0, b"z", pts_ns=40_000_000)]
    tl3 = build_timeline(video, dup)
    check(tl3.stats.duplicate_pts_corrections == 1,
          "duplicate PTS bumped and counted")
    audio = Track(2, 2, TRACK_TYPE_AUDIO, "A_AAC", sample_rate=44100.0)
    apackets = [Block(2, 0, 0, True, 0, b"a",
                      pts_ns=int(i * 1_000_000_000 / 30))
                for i in range(3)]
    tl4 = build_timeline(audio, apackets)
    check(tl4.stats.timescale == 44100, "audio timescale from sample rate")
    check(tl4.stats.rounding_affected_samples >= 1,
          "rounding residual reported")


def main() -> int:
    test_vint()
    test_limits_and_boundaries()
    test_avc_mapping()
    test_aac_and_opus()
    test_timeline_policy()
    print(f"\n{sys.argv[0]}: {PASS} checks passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
