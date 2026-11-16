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
from mkvmp4.ebml.reader import EbmlReader  # noqa: E402
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


# ---------------------------------------------------------------------------
# Requirement 5: DTS/PTS timeline, rebasing and drift
# ---------------------------------------------------------------------------
class TestTimeline:
    def test_rebase_and_track_offsets(self, parsed):
        _, _, timeline = parsed
        assert abs(timeline.rebase_ms - 80.0) < 1e-9
        video = next(t for t in timeline.tracks if t.track.track_type == 1)
        audio = next(t for t in timeline.tracks if t.track.track_type == 2)
        assert video.first_pts_ms == 20 and video.last_pts_ms == 140
        assert audio.first_pts_ms == 0 and audio.last_pts_ms == 192
        assert len(video.samples) == 6 and len(audio.samples) == 10

    def test_b_frame_composition_offsets(self, parsed):
        _, _, timeline = parsed
        video = next(t for t in timeline.tracks if t.track.track_type == 1)
        # Decode order contains a B-frame: at least one non-zero ctts and
        # every DTS stays strictly monotonic with PTS >= DTS.
        offsets = [s.composition_offset_ms for s in video.samples]
        assert any(offset != 0 for offset in offsets)
        dts = [s.dts_ms for s in video.samples]
        assert all(b > a for a, b in zip(dts, dts[1:]))
        assert all(s.pts_ms >= s.dts_ms for s in video.samples)

    def test_audio_gap_preserved(self, parsed):
        _, _, timeline = parsed
        audio = next(t for t in timeline.tracks if t.track.track_type == 2)
        pts = [s.pts_ms for s in audio.samples]
        # 32 ms gap between the 48 ms and 80 ms presentation times.
        gap_index = pts.index(48)
        assert pts[gap_index + 1] == 80
        assert audio.samples[gap_index].duration_ms == 32

    def test_audio_dts_equals_pts(self, parsed):
        _, _, timeline = parsed
        audio = next(t for t in timeline.tracks if t.track.track_type == 2)
        assert all(s.dts_ms == s.pts_ms for s in audio.samples)

    def test_no_negative_timestamps(self, parsed):
        _, _, timeline = parsed
        for track in timeline.tracks:
            assert all(s.dts_ms >= 0 and s.pts_ms >= 0 for s in track.samples)


# ---------------------------------------------------------------------------
# Requirement 6/7: both exporters share one parse and validate indexes
# ---------------------------------------------------------------------------
class TestExporters:
    def test_flat_export_tables(self, parsed, tmp_path):
        _, _, timeline = parsed
        out = tmp_path / "flat.mp4"
        result = export_flat(timeline, str(out))
        assert result.total_samples == 16
        assert result.offset_box == "stco"
        assert result.offset_entries == 16
        data = out.read_bytes()
        index = validate_structure(data, expected=["ftyp", "moov", "mdat"])
        for name in ("stts", "ctts", "stsc", "stsz", "stco", "avcC", "esds"):
            assert name in index

    def test_flat_stco_offsets_inside_mdat(self, parsed, tmp_path):
        _, _, timeline = parsed
        out = tmp_path / "flat.mp4"
        export_flat(timeline, str(out))
        data = out.read_bytes()
        mdat_pos = data.find(b"mdat") - 4
        mdat_size = struct.unpack(">I", data[mdat_pos:mdat_pos + 4])[0]
        payload_start, payload_end = mdat_pos + 8, mdat_pos + mdat_size
        position = 0
        total = 0
        while True:
            at = data.find(b"stco", position)
            if at < 0:
                break
            count, = struct.unpack(">I", data[at + 8:at + 12])
            for i in range(count):
                offset, = struct.unpack(
                    ">I", data[at + 12 + 4 * i:at + 16 + 4 * i])
                assert payload_start <= offset < payload_end
            total += count
            position = at + 4
        assert total == 16

    def test_fragmented_export(self, parsed, tmp_path):
        _, _, timeline = parsed
        out = tmp_path / "frag.mp4"
        result = export_fragmented(timeline, str(out))
        assert result.fragment_count == 2
        assert result.sidx_refs == 2
        assert result.total_samples == 16
        data = out.read_bytes()
        index = validate_structure(
            data, expected=["ftyp", "moov", "sidx", "moof", "mdat"])
        for name in ("mvex", "trex", "tfhd", "tfdt", "trun"):
            assert name in index

    def test_both_exporters_share_timeline(self, parsed, tmp_path):
        _, _, timeline = parsed
        flat = export_flat(timeline, str(tmp_path / "a.mp4"))
        frag = export_fragmented(timeline, str(tmp_path / "b.mp4"))
        assert flat.total_samples == frag.total_samples == 16

    def test_atomic_write_leaves_no_tempfile(self, parsed, tmp_path):
        _, _, timeline = parsed
        export_flat(timeline, str(tmp_path / "ok.mp4"))
        leftovers = [n for n in os.listdir(tmp_path)
                     if n.startswith(".mkvmp4-")]
        assert not leftovers

    def test_index_validation_detects_corrupt_image(self, parsed, tmp_path):
        # A truncated emitted image must fail structural validation rather
        # than be mistaken for a valid MP4.
        _, _, timeline = parsed
        out = tmp_path / "ok.mp4"
        export_flat(timeline, str(out))
        data = bytearray(out.read_bytes())
        with pytest.raises(IndexValidationError):
            validate_structure(bytes(data[: len(data) // 2]),
                               expected=["ftyp", "moov", "mdat"])


# ---------------------------------------------------------------------------
# Requirement 8: corruption, limits and CLI exit codes
# ---------------------------------------------------------------------------
class TestFailuresAndExitCodes:
    def test_truncated_block_payload(self, fixture_path, tmp_path):
        raw = open(fixture_path, "rb").read()
        target = tmp_path / "truncated.mkv"
        target.write_bytes(raw[: len(raw) - 9])
        with pytest.raises(CorruptMKVError):
            mkv = parse_matroska(str(target), max_samples=MAX_SAMPLES,
                                 max_packet_size=MAX_PACKET)
            deinterlace(mkv, max_samples=MAX_SAMPLES,
                        max_packet_size=MAX_PACKET)

    def test_sample_count_limit(self, fixture_path):
        mkv = parse_matroska(fixture_path, max_samples=2,
                             max_packet_size=MAX_PACKET)
        with pytest.raises(LimitExceededError):
            deinterlace(mkv, max_samples=2, max_packet_size=MAX_PACKET)

    def test_packet_size_limit(self, fixture_path):
        with pytest.raises((LimitExceededError, CorruptMKVError)):
            mkv = parse_matroska(fixture_path, max_samples=MAX_SAMPLES,
                                 max_packet_size=3)
            deinterlace(mkv, max_samples=MAX_SAMPLES,
                        max_packet_size=3)

    def test_unsupported_codec_exit_code_three(self):
        from mkvmp4.errors import UnsupportedCodecError
        assert UnsupportedCodecError.exit_code == 3

    def test_index_validation_exit_code_five(self):
        assert IndexValidationError.exit_code == 5

    def test_usage_error_exit_code_two(self):
        from mkvmp4.errors import UsageError
        assert UsageError.exit_code == 2

    def test_io_failure_exit_code_four(self):
        from mkvmp4.errors import IOFailure
        assert IOFailure.exit_code == 4

    def test_cli_missing_input_returns_two(self):
        from mkvmp4.cli import main
        assert main(["remux", "/no/such/file.mkv", "/tmp/out.mp4"]) == 2

    def test_cli_corrupt_input_returns_three(self, tmp_path):
        from mkvmp4.cli import main
        bad = tmp_path / "bad.mkv"
        bad.write_bytes(b"\x1a\x45\xdf\xa3not-a-real-matroska")
        out = tmp_path / "out.mp4"
        assert main(["remux", str(bad), str(out)]) == 3

    def test_cli_demo_success_returns_zero(self, tmp_path):
        from mkvmp4.cli import main
        code = main(["demo", "--out-dir", str(tmp_path / "demo"),
                     "--mode", "both"])
        assert code == 0
        produced = os.listdir(str(tmp_path / "demo"))
        assert "synthetic.flat.mp4" in produced
        assert "synthetic.frag.mp4" in produced

    def test_write_failure_raises_io_failure_exit_four(self, tmp_path):
        # A regular file used as a directory makes the temp-file creation
        # fail deterministically with IOFailure (exit code 4), without
        # depending on file permission bits or the running user.
        from mkvmp4.errors import IOFailure
        from mkvmp4.mp4.atoms import write_file_atomic
        blocker = tmp_path / "not-a-dir"
        blocker.write_bytes(b"x")
        target = str(blocker / "out.mp4")
        with pytest.raises(IOFailure) as exc:
            write_file_atomic(target, b"payload")
        assert exc.value.exit_code == 4

    def test_cli_bad_mode_returns_two(self):
        from mkvmp4.cli import main
        with pytest.raises(SystemExit):
            main(["remux", "in.mkv", "out.mp4", "--mode", "nonsense"])
