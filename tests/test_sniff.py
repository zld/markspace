import struct

import pytest

from mediacheck.sniff import SniffError, sniff_path


def test_mp4_basic_metadata(valid_mp4):
    result = sniff_path(valid_mp4)
    assert result.container == "mp4"
    assert result.file_size == valid_mp4.stat().st_size
    assert result.movie_timescale == 1000
    assert [t.handler for t in result.tracks] == ["video", "audio"]
    video, audio = result.tracks
    assert (video.track_id, video.codec, video.width, video.height) == \
        (1, "h264", 1920, 1080)
    assert (audio.track_id, audio.codec, audio.sample_rate, audio.channels) == \
        (2, "aac", 48000, 2)
    assert video.duration_units == 60000
    assert video.timescale == 30000


def test_mp4_language_and_edit_list(edge_dir):
    result = sniff_path(edge_dir / "offset_start.mp4")
    track = result.tracks[0]
    # The fixture uses an empty leading edit of 1000 movie units (1.0s).
    assert track.start_movie_units == 1000
    assert track.start_time_units == 0
    assert track.language == "und"


def test_wav_metadata(valid_wav):
    result = sniff_path(valid_wav)
    assert result.container == "wav"
    assert result.wav_format_tag == 1
    track = result.tracks[0]
    assert track.codec == "pcm_s16le"
    assert track.sample_rate == 48000
    assert track.channels == 1
    assert track.duration_units == 48000


def test_truncated_mp4_reports_offset(valid_mp4):
    broken = valid_mp4.with_name("broken.mp4")
    broken.write_bytes(valid_mp4.read_bytes()[:200])
    with pytest.raises(SniffError) as info:
        sniff_path(broken)
    error = info.value
    assert error.code in {"out_of_bounds", "truncated_box"}
    assert error.offset is not None


def test_oversized_box_is_rejected(valid_mp4):
    data = valid_mp4.read_bytes()
    idx = data.find(b"moov")
    out = bytearray(data)
    struct.pack_into(">I", out, idx - 4, len(data) * 2)
    broken = valid_mp4.with_name("oversize.mp4")
    broken.write_bytes(bytes(out))
    with pytest.raises(SniffError) as info:
        sniff_path(broken)
    assert info.value.code == "out_of_bounds"
    assert info.value.offset == idx - 4


def test_size_smaller_than_header_rejected(valid_mp4):
    data = bytearray(valid_mp4.read_bytes())
    idx = data.find(b"trak")
    struct.pack_into(">I", data, idx - 4, 4)
    broken = valid_mp4.with_name("tiny.mp4")
    broken.write_bytes(bytes(data))
    with pytest.raises(SniffError) as info:
        sniff_path(broken)
    assert info.value.code == "invalid_size"


def test_unknown_top_level_box_warns_but_succeeds(valid_mp4):
    data = valid_mp4.read_bytes()
    ftyp_size = struct.unpack_from(">I", data, 0)[0]
    extra = struct.pack(">I4s", 12, b"zzzz") + b"abcd"
    mixed = valid_mp4.with_name("mixed.mp4")
    mixed.write_bytes(data[:ftyp_size] + extra + data[ftyp_size:])
    result = sniff_path(mixed)
    assert result.container == "mp4"
    codes = [w.code for w in result.warnings]
    assert "unknown_box" in codes


def test_wave_format_extensible(tmp_path):
    import struct
    sr, channels, bits = 48000, 2, 24
    block = channels * bits // 8
    data = bytes(sr * block * 2)
    guid = bytes.fromhex("0100000000001000800000aa00389b71")
    fmt = struct.pack("<HHIIHH", 0xFFFE, channels, sr, sr * block, block, bits)
    fmt += struct.pack("<HHI", 22, bits, 0x3) + guid
    chunks = (b"fmt " + struct.pack("<I", len(fmt)) + fmt
              + b"data" + struct.pack("<I", len(data)) + data)
    riff = b"RIFF" + struct.pack("<I", 4 + len(chunks)) + b"WAVE" + chunks
    path = tmp_path / "ext.wav"
    path.write_bytes(riff)
    result = sniff_path(path)
    track = result.tracks[0]
    # The declared tag stays extensible; the resolved codec comes from GUID.
    assert result.wav_format_tag == 0xFFFE
    assert track.codec == "pcm_s16le"
    assert track.channels == 2
    assert track.bit_depth == 24


def test_unknown_signature(valid_wav):
    other = valid_wav.with_name("other.bin")
    other.write_bytes(b"NOPE" + b"\x00" * 40)
    with pytest.raises(SniffError) as info:
        sniff_path(other)
    assert info.value.code == "unknown_container"


def test_file_too_small(tmp_path):
    tiny = tmp_path / "tiny.bin"
    tiny.write_bytes(b"RIFF")
    with pytest.raises(SniffError) as info:
        sniff_path(tiny)
    assert info.value.code == "file_too_small"


def test_wav_truncated_data_chunk(valid_wav):
    data = valid_wav.read_bytes()
    broken = valid_wav.with_name("broken.wav")
    broken.write_bytes(data[: len(data) - 100])
    with pytest.raises(SniffError) as info:
        sniff_path(broken)
    assert info.value.code in {"out_of_bounds", "truncated_chunk"}


def test_no_moov_is_locatable(valid_mp4):
    data = valid_mp4.read_bytes()
    idx = data.find(b"moov")
    out = bytearray(data)
    out[idx:idx + 4] = b"free"
    broken = valid_mp4.with_name("nomoov.mp4")
    broken.write_bytes(bytes(out))
    with pytest.raises(SniffError) as info:
        sniff_path(broken)
    assert info.value.code == "missing_moov"
    assert info.value.offset == 0


def test_reader_never_reads_past_file_end(valid_mp4):
    from mediacheck.sniff import Reader
    size = valid_mp4.stat().st_size
    with valid_mp4.open("rb") as handle:
        reader = Reader(handle, size)
        with pytest.raises(SniffError) as info:
            reader.read_at(size - 4, 8)
        assert info.value.code == "out_of_bounds"
        with pytest.raises(SniffError):
            reader.read_at(-1, 4)
