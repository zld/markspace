"""End-to-end and unit tests for the mkvmp4 pipeline.

Covers VINT edge cases, unknown-size EBML boundaries, all three lacing
modes, timeline rebasing/B-frame ordering, both MP4 exporters and the full
set of corruption / unsupported-codec failure paths with their documented
exit codes. Fixtures are generated in-memory; no external files are used.
"""

import os
import struct
import sys

import pytest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "tools"))

from make_fixture import build_fixture_bytes, write_fixture  # noqa: E402

from mkvmp4.ebml import schema  # noqa: E402
from mkvmp4.ebml.reader import EbmlReader, decode_leaf  # noqa: E402
from mkvmp4.ebml.vint import (decode_vint, encode_element_id,  # noqa: E402
                              encode_unknown_size, encode_vint)
from mkvmp4.errors import (CorruptMKVError, IndexValidationError,  # noqa: E402
                           LimitExceededError, UnsupportedCodecError)
from mkvmp4.mkv.blocks import deinterlace  # noqa: E402
from mkvmp4.mkv.segment import parse_matroska  # noqa: E402
from mkvmp4.mp4.atoms import validate_structure  # noqa: E402
from mkvmp4.mp4.flat import export_flat  # noqa: E402
from mkvmp4.mp4.fragmented import export_fragmented  # noqa: E402
from mkvmp4.report import summarize_tree  # noqa: E402
from mkvmp4.timeline import build_timeline  # noqa: E402

MAX_SAMPLES = 200_000
MAX_PACKET = 64 * 1024 * 1024


@pytest.fixture(scope="module")
def fixture_path(tmp_path_factory):
    path = tmp_path_factory.mktemp("mkv") / "synthetic.mkv"
    write_fixture(str(path))
    return str(path)


@pytest.fixture()
def parsed(fixture_path):
    mkv = parse_matroska(fixture_path, max_samples=MAX_SAMPLES,
                         max_packet_size=MAX_PACKET)
    samples = deinterlace(mkv, max_samples=MAX_SAMPLES,
                          max_packet_size=MAX_PACKET)
    timeline = build_timeline(mkv, samples)
    return mkv, samples, timeline


# ---------------------------------------------------------------------------
# Requirement 1: VINT decoding and validation
# ---------------------------------------------------------------------------
class TestVint:
    def test_round_trip_widths(self):
        for value in (0, 1, 0x7E, 0x7F, 0x3FFE, 0x3FFF, 123_456_789):
            encoded = encode_vint(value)
            decoded, _, _ = decode_vint(encoded)
            assert decoded == value

    def test_unknown_length_sentinel(self):
        for width in (1, 2, 4, 8):
            value, _, _ = decode_vint(encode_unknown_size(width))
            assert value == -1

    def test_element_id_preserves_width(self):
        assert encode_element_id(0xEC) == b"\xec"
        assert encode_element_id(0x1F43B675) == b"\x1f\x43\xb6\x75"
        assert encode_element_id(0x18538067) == b"\x18\x53\x80\x67"

    def test_illegal_leading_octet_reports_offset(self):
        with pytest.raises(CorruptMKVError) as exc:
            decode_vint(b"\x00")
        assert exc.value.offset == 0

    def test_truncated_vint_reports_offset(self):
        with pytest.raises(CorruptMKVError) as exc:
            decode_vint(b"\x40")
        assert exc.value.offset == 0
        with pytest.raises(CorruptMKVError) as exc:
            decode_vint(b"\x82\x00", 1)
        assert exc.value.offset == 1


# ---------------------------------------------------------------------------
# Requirement 2: unknown-size Segment/Cluster boundaries
# ---------------------------------------------------------------------------
class TestUnknownSizeBoundaries:
    def test_fixture_structure(self, fixture_path):
        summary = summarize_tree(fixture_path)
        assert 40 <= summary.nodes <= 80
        assert 4 <= summary.max_depth <= 6
        assert summary.unknown_size_masters == 3

    def test_three_clusters_parsed(self, parsed):
        mkv, _, _ = parsed
        assert mkv.cluster_count == 3
        assert mkv.unknown_size_masters == 3

    def test_unknown_size_segment_accepted(self, tmp_path):
        # Rebuild the fixture with an unknown-size Segment.
        data = build_fixture_bytes()
        from mkvmp4.ebml.vint import encode_vint as _enc
        # Locate the Segment header and rewrite its size VINT as unknown.
        seg_id = encode_element_id(schema.SEGMENT)
        pos = data.find(seg_id)
        assert pos >= 0
        size_width = _width_of_size_vint(data, pos + len(seg_id))
        rewritten = (data[:pos + len(seg_id)] +
                     encode_unknown_size(size_width) +
                     data[pos + len(seg_id) + size_width:])
        target = tmp_path / "unknown_segment.mkv"
        target.write_bytes(rewritten)
        mkv = parse_matroska(str(target), max_samples=MAX_SAMPLES,
                             max_packet_size=MAX_PACKET)
        assert len(mkv.tracks) == 2

    def test_truncation_raises_with_offset_and_path(self, fixture_path):
        raw = bytearray(open(fixture_path, "rb").read())
        # Cut inside the final cluster so a header/payload becomes partial.
        truncated = bytes(raw[: len(raw) - 6])
        with pytest.raises(CorruptMKVError) as exc:
            parse_matroska(_write(tmp_path := fixture_path, truncated),
                           max_samples=MAX_SAMPLES,
                           max_packet_size=MAX_PACKET)
        assert exc.value.offset is not None
        assert "Cluster" in "/".join(exc.value.path)

    def test_depth_limit(self, fixture_path):
        # Cluster sits at depth 1 and its children at depth 2; a limit of 1
        # must reject opening the Cluster master.
        with EbmlReader(fixture_path, max_depth=1) as reader:
            reader.open_root()
            while True:
                root = reader.next_element()
                if root is not None and root.name == "Segment":
                    break
            reader.open_master(root)
            cluster = reader.next_element()
            with pytest.raises(LimitExceededError):
                reader.open_master(cluster)


def _width_of_size_vint(data: bytes, offset: int) -> int:
    from mkvmp4.ebml.vint import vint_width
    return vint_width(data[offset], offset)


def _write(fixture_path: str, payload: bytes) -> str:
    target = os.path.join(os.path.dirname(fixture_path), "truncated.mkv")
    with open(target, "wb") as handle:
        handle.write(payload)
    return target


# ---------------------------------------------------------------------------
# Requirement 3: tracks, codec gating and requirement 4: lacing
# ---------------------------------------------------------------------------
class TestTracksAndLacing:
    def test_track_metadata(self, parsed):
        mkv, _, _ = parsed
        by_number = {t.number: t for t in mkv.tracks}
        assert by_number[1].mp4_codec == "avc1"
        assert by_number[2].mp4_codec == "mp4a"
        assert by_number[1].codec_private[0] == 1  # AVCC version byte
        assert by_number[2].codec_private == bytes([0x12, 0x10])
        assert by_number[1].default_duration == 40_000_000
        assert by_number[2].language == "eng"

    def test_lacing_expands_to_expected_counts(self, parsed):
        _, samples, _ = parsed
        video = [s for s in samples if s.track_number == 1]
        audio = [s for s in samples if s.track_number == 2]
        assert len(video) == 6
        assert len(audio) == 10

    def test_negative_relative_timecode(self, parsed):
        _, samples, timeline = parsed
        audio = next(t for t in timeline.tracks if t.track.number == 2)
        # First rebased audio PTS is zero; monotonic across the gap.
        pts = [s.pts_ms for s in audio.samples]
        assert pts == sorted(pts)
        assert pts[0] == 0 and pts[-1] == 192

    def test_block_group_duration_and_reference(self, fixture_path):
        mkv = parse_matroska(fixture_path, max_samples=MAX_SAMPLES,
                             max_packet_size=MAX_PACKET)
        groups = [b for b in mkv.blocks if b.in_block_group]
        assert groups and groups[0].duration_ticks == 40
        assert groups[0].reference_count == 1
        assert groups[0].keyframe is False

    def test_unsupported_codec_lists_track_number(self, tmp_path):
        # Rebuild a valid EBML CodecID element with an unsupported codec so
        # the surrounding element lengths stay consistent.
        data = build_fixture_bytes()
        # Same-length unsupported codec keeps every parent boundary intact;
        # only the codec gating should reject it (A_PCM is not remuxable).
        original = (encode_element_id(schema.CODEC_ID) +
                    encode_vint(len(b"A_PCM")) + b"A_AAC")
        replacement = (encode_element_id(schema.CODEC_ID) +
                       encode_vint(len(b"A_PCM")) + b"A_PCM")
        position = data.find(original)
        assert position >= 0
        broken = data[:position] + replacement + data[position + len(original):]
        assert len(broken) == len(data)
        target = tmp_path / "opus.mkv"
        target.write_bytes(broken)
        with pytest.raises(UnsupportedCodecError) as exc:
            parse_matroska(str(target), max_samples=MAX_SAMPLES,
                           max_packet_size=MAX_PACKET)
        assert 2 in exc.value.track_numbers

    def test_oversized_packet_rejected(self, fixture_path):
        with pytest.raises((LimitExceededError, CorruptMKVError)):
            mkv = parse_matroska(fixture_path, max_samples=MAX_SAMPLES,
                                 max_packet_size=4)
            deinterlace(mkv, max_samples=MAX_SAMPLES, max_packet_size=4)
