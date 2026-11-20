"""Generate deterministic Matroska fixtures (no external media required)."""

from __future__ import annotations

import os

from . import ebml_ids as ids


def vint_size(value: int) -> bytes:
    for length in range(1, 9):
        if value < (1 << (7 * length)) - 1:
            return ((1 << (7 * length)) | value).to_bytes(length, "big")
    raise ValueError("value needs an unknown-size VINT")


UNKNOWN_SIZE = b"\x01\xff\xff\xff\xff\xff\xff\xff"


def elem_id(element_id: int) -> bytes:
    return ids.encode_id(element_id)


def leaf(element_id: int, payload: bytes) -> bytes:
    return elem_id(element_id) + vint_size(len(payload)) + payload


def uint_elem(element_id: int, value: int) -> bytes:
    width = 1 if value < 256 else 2 if value < 65536 else 4 if value < 2**32 else 8
    return leaf(element_id, value.to_bytes(width, "big"))


def int_elem(element_id: int, value: int) -> bytes:
    width = 1
    while not (-(1 << (8 * width - 1)) <= value < (1 << (8 * width - 1))):
        width += 1
    return leaf(element_id, value.to_bytes(width, "big", signed=True))


def float_elem(element_id: int, value: float) -> bytes:
    import struct
    return leaf(element_id, struct.pack(">d", value))


def text_elem(element_id: int, value: str) -> bytes:
    return leaf(element_id, value.encode("utf-8"))


def master(element_id: int, payload: bytes, *, unknown: bool = False) -> bytes:
    size = UNKNOWN_SIZE if unknown else vint_size(len(payload))
    return elem_id(element_id) + size + payload


def block_payload(track: int, relative: int, flags: int, frames: list[bytes],
                  lacing: str) -> bytes:
    lacing_bits = {"none": 0, "xiph": 1, "fixed": 2, "ebml": 3}[lacing]
    flags = (flags & 0xF9) | (lacing_bits << 1)
    body = bytearray()
    if lacing != "none":
        body.append(len(frames) - 1)
        if lacing == "xiph":
            for frame in frames[:-1]:
                size = len(frame)
                while size >= 255:
                    body.append(255)
                    size -= 255
                body.append(size)
        elif lacing == "ebml":
            first = frames[0]
            body += vint_size(len(first))
            for previous, current in zip(frames, frames[1:-1]):
                delta = len(current) - len(previous)
                # EBML signed lace VINT: bias by the positive range midpoint.
                width = 1
                while not (-(1 << (7 * width - 1)) + 1 <= delta
                           < (1 << (7 * width - 1))):
                    width += 1
                encoded = delta + (1 << (7 * width - 1)) - 1
                body += ((1 << (7 * width)) | encoded).to_bytes(width, "big")
        elif lacing != "fixed":
            raise ValueError(lacing)
    for frame in frames:
        body += frame
    head = vint_size(track) + int(relative).to_bytes(2, "big", signed=True)
    return head + bytes([flags]) + bytes(body)


def avcc(sps: bytes, pps: bytes) -> bytes:
    return (b"\x01\x64\x00\x1f\xff\xe1" + len(sps).to_bytes(2, "big") + sps +
            b"\x01" + len(pps).to_bytes(2, "big") + pps)


def asc() -> bytes:
    # AAC-LC, 48 kHz (index 3), stereo.
    return bytes([0x11, 0x90])


def _track_entry(number: int, track_type: int, codec: str, private: bytes,
                 default_duration: int | None, *, width: int = 0,
                 height: int = 0, channels: int = 0,
                 sample_rate: float = 0.0) -> bytes:
    parts = [uint_elem(ids.TRACK_NUMBER, number), uint_elem(ids.TRACK_UID, 1000 + number),
             uint_elem(ids.TRACK_TYPE, track_type), uint_elem(ids.FLAG_LACING, 1),
             text_elem(ids.CODEC_ID, codec), leaf(ids.CODEC_PRIVATE, private)]
    if default_duration is not None:
        parts.append(uint_elem(ids.DEFAULT_DURATION, default_duration))
    if track_type == 1:
        parts.append(master(ids.VIDEO, uint_elem(ids.PIXEL_WIDTH, width) +
                            uint_elem(ids.PIXEL_HEIGHT, height)))
    else:
        parts.append(master(ids.AUDIO, float_elem(ids.SAMPLING_FREQUENCY, sample_rate) +
                            uint_elem(ids.CHANNELS, channels)))
    return master(ids.TRACK_ENTRY, b"".join(parts))


def _simple(track: int, relative: int, frame: bytes, key: bool) -> bytes:
    return leaf(ids.SIMPLE_BLOCK,
                block_payload(track, relative, 0x80 if key else 0x00, [frame], "none"))


def _group(track: int, relative: int, frame: bytes, duration: int,
           references: int) -> bytes:
    parts = [leaf(ids.BLOCK, block_payload(track, relative, 0, [frame], "none"))]
    parts.append(uint_elem(ids.BLOCK_DURATION, duration))
    for _ in range(references):
        parts.append(int_elem(ids.REFERENCE_BLOCK, -1))
    return master(ids.BLOCK_GROUP, b"".join(parts))


def _segment(tracks: bytes, clusters: bytes, unknown: bool, duration_ms: float) -> bytes:
    info = master(ids.INFO, uint_elem(ids.TIMESTAMP_SCALE, 1_000_000) +
                  float_elem(ids.DURATION, duration_ms) +
                  text_elem(ids.MUXING_APP, "fixture") +
                  text_elem(ids.WRITING_APP, "fixture"))
    return master(ids.SEGMENT, info + tracks + clusters, unknown=unknown)


def _header() -> bytes:
    return master(ids.EBML, uint_elem(ids.EBML_VERSION, 1) +
                  uint_elem(ids.EBML_READ_VERSION, 1) +
                  uint_elem(ids.EBML_MAX_ID_LENGTH, 4) +
                  uint_elem(ids.EBML_MAX_SIZE_LENGTH, 8) +
                  text_elem(ids.DOC_TYPE, "matroska") +
                  uint_elem(ids.DOC_TYPE_VERSION, 4) +
                  uint_elem(ids.DOC_TYPE_READ_VERSION, 2))


VIDEO_SPS = bytes.fromhex("6764001facd9405005bb011000000300100000030320f1831960")
VIDEO_PPS = bytes.fromhex("68ebe3cb22c0")


def _video_frame(index: int, size: int) -> bytes:
    # Annex-B-style payload prefixed with a 4-byte length (arbitrary bytes,
    # but stable so tests can compare them byte-for-byte).
    seed = bytes([(index * 37 + 11) & 0xFF, (index * 53 + 7) & 0xFF, 0x65, 0x88])
    return seed + bytes(((index + j) * 13) & 0xFF for j in range(max(0, size - 4)))


def _audio_frame(index: int, size: int) -> bytes:
    return bytes(((index * 17 + j * 7) & 0xFF) for j in range(size))


def _video_track(default_duration: int) -> bytes:
    return _track_entry(1, 1, "V_MPEG4/ISO/AVC", avcc(VIDEO_SPS, VIDEO_PPS),
                        default_duration, width=64, height=48)


def _audio_track() -> bytes:
    return _track_entry(2, 2, "A_AAC", asc(), 32_000_000, channels=2,
                        sample_rate=48000.0)


def build_reordered() -> bytes:
    """Two timelines: video starts at 40 ms and is reordered (B-frames).

    Video: 4 BlockGroups, PTS 40/120/80/160 ms, no DefaultDuration so durations
    come from BlockDuration.  Audio: 7 SimpleBlocks with Xiph lacing, PTS 0..192.
    Cluster 1 uses a known size, Cluster 2 an unknown size.
    """

    video_sizes = [18, 22, 15, 20]
    # Decode order I P B B; presentation 40, 120, 80, 160 ms.
    relative_times = [40, 120, 80, 160]
    groups: list[bytes] = []
    for decode_index, relative in enumerate(relative_times):
        frame = _video_frame(decode_index, video_sizes[decode_index])
        references = 0 if decode_index == 0 else 1
        groups.append(_group(1, relative, frame, 40, references))
    cluster1 = master(ids.CLUSTER, uint_elem(ids.TIMECODE, 0) + b"".join(groups))
    # Audio: two laced blocks; first Xiph (4 frames), second fixed (3 frames).
    audio_frames = ([_audio_frame(i, 10 + i) for i in range(2)] +
                    [_audio_frame(i, 12) for i in range(2, 4)] +
                    [_audio_frame(i, 13 + i) for i in range(4, 6)] +
                    [_audio_frame(i, 15) for i in range(6, 7)])
    xiph = block_payload(2, 0, 0x80, audio_frames[:2], "xiph")
    fixed = block_payload(2, 64, 0x80, audio_frames[2:4], "fixed")
    cluster2 = master(ids.CLUSTER, uint_elem(ids.TIMECODE, 0) +
                      leaf(ids.SIMPLE_BLOCK, xiph) +
                      leaf(ids.SIMPLE_BLOCK, fixed) +
                      leaf(ids.SIMPLE_BLOCK, block_payload(
                          2, 128, 0x80, audio_frames[4:6], "ebml")) +
                      leaf(ids.SIMPLE_BLOCK, block_payload(
                          2, 192, 0x80, audio_frames[6:], "none")),
                      unknown=True)
    tracks = master(ids.TRACKS, _video_track(None) + _audio_track())
    return _header() + _segment(tracks, cluster1 + cluster2, True, 192.0)


def build_monotonic() -> bytes:
    """Monotonic timeline with a non-zero start, EBML and Xiph lacing."""

    video = [_simple(1, -900 + 40 * i, _video_frame(20 + i, 16 + i), True)
             for i in range(4)]
    audio_frames = ([_audio_frame(30 + i, 9 + i) for i in range(2)] +
                    [_audio_frame(33 + i, 12) for i in range(2, 4)] +
                    [_audio_frame(40 + i, 13 + i) for i in range(4, 6)])
    # Audio: 2 frames per block at 32 ms, starting together with video (100 ms).
    audio_ebml = block_payload(2, -900, 0x80, audio_frames[:2], "ebml")
    audio_fixed = block_payload(2, -836, 0x80, audio_frames[2:4], "fixed")
    audio_xiph = block_payload(2, -772, 0x80, audio_frames[4:], "xiph")
    cluster1 = master(ids.CLUSTER, uint_elem(ids.TIMECODE, 1000) +
                      video[0] + video[1] +
                      leaf(ids.SIMPLE_BLOCK, audio_ebml) +
                      leaf(ids.SIMPLE_BLOCK, audio_fixed))
    cluster2 = master(ids.CLUSTER, uint_elem(ids.TIMECODE, 1000) +
                      video[2] + video[3] +
                      leaf(ids.SIMPLE_BLOCK, audio_xiph))
    tracks = master(ids.TRACKS, _video_track(40_000_000) + _audio_track())
    return _header() + _segment(tracks, cluster1 + cluster2, False, 260.0)


def build_gapped() -> bytes:
    """Monotonic video with a 80 ms hole between the 2nd and 3rd frames.

    Preserve must keep the hole (stts deltas follow real PTS spacing), while
    rewrite keeps the presented PTS and fills DTS from block durations.
    """

    relative = [100, 140, 220, 260]
    video = [_simple(1, r, _video_frame(50 + i, 16 + i), True)
             for i, r in enumerate(relative)]
    audio_frames = [_audio_frame(60 + i, 10) for i in range(4)]
    audio = block_payload(2, 100, 0x80, audio_frames, "xiph")
    cluster1 = master(ids.CLUSTER, uint_elem(ids.TIMECODE, 0) +
                      video[0] + video[1] +
                      leaf(ids.SIMPLE_BLOCK, audio))
    cluster2 = master(ids.CLUSTER, uint_elem(ids.TIMECODE, 0) +
                      video[2] + video[3])
    tracks = master(ids.TRACKS, _video_track(40_000_000) + _audio_track())
    return _header() + _segment(tracks, cluster1 + cluster2, True, 300.0)


def build_raw(block_element: bytes) -> bytes:
    """One-cluster fixture containing a caller-supplied (possibly corrupt) block."""

    cluster = master(ids.CLUSTER, uint_elem(ids.TIMECODE, 0) + block_element)
    tracks = master(ids.TRACKS, _video_track(None) + _audio_track())
    return _header() + _segment(tracks, cluster, True, 0.0)


def generate(out_dir: str) -> dict[str, str]:
    os.makedirs(out_dir, exist_ok=True)
    paths = {}
    for name, builder in (("reordered", build_reordered),
                          ("monotonic", build_monotonic),
                          ("gapped", build_gapped)):
        path = os.path.join(out_dir, f"{name}.mkv")
        payload = builder()
        with open(path, "wb") as handle:
            handle.write(payload)
        paths[name] = path
    return paths
