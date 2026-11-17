"""Synthetic replay-clip fixture generation.

Container muxers reject non-monotonic packet timestamps, so the four replay
fault classes cannot all be embedded reliably at the container level.
Instead the fixture is built as two deterministic artefacts:

1. a clean H.264 clip (regular GOP, real I/P/B frames) re-muxed into a
   Matroska body carried under the demo's ``.mp4`` extension — PyAV/FFmpeg
   probe container format by content;
2. a sidecar manifest (``.faults.json``) describing, by **presentation
   position**, which timestamp faults to apply.

``frameaudit scan --faults-manifest`` applies the manifest to the decoded
presentation-ordered frames before repair, so the whole pipeline — including
missing-PTS, duplicate, backward and over-threshold jump handling — runs
against real decoded frames and stays fully reproducible offline.
"""

from __future__ import annotations

import json
import tempfile
from fractions import Fraction
from pathlib import Path

import av
import numpy as np

from .errors import OutputError, UsageError

FAULT_ORDER = ("missing", "backward", "jump", "duplicate")
FIXTURE_TIME_BASE = Fraction(1, 1000)
GOP_SIZE = 25
MAX_B_FRAMES = 3
# Jump distortion in frame durations.  A +2 frame shift opens a gap of three
# frame durations (100 ms at 30 fps) without colliding with the next frame's
# PTS, so it produces exactly one ``jump`` repair with no cascade.  The gap
# exceeds the 1.5x repair threshold (50 ms); the repair restores the normal
# 33.3 ms per-frame cadence.
JUMP_SHIFT_FRAMES = 2
# Backward distortion in frame durations.
BACKWARD_SHIFT_FRAMES = 2


def _fault_positions(frame_count: int, faults: int) -> dict[int, str]:
    if faults < 0 or faults > frame_count - 1:
        raise UsageError(
            f"cannot inject {faults} faults into {frame_count} frames"
        )
    # Interior IDR positions are stable anchors; one fault per fault class.
    idr_positions = list(range(GOP_SIZE, frame_count, GOP_SIZE))
    if len(idr_positions) < faults:
        raise UsageError(
            f"need at least {faults} interior IDR frames for fault injection, "
            f"clip of {frame_count} frames only has {len(idr_positions)}"
        )
    chosen: dict[int, str] = {}
    for n in range(faults):
        idx = (n * len(idr_positions)) // faults
        chosen[idr_positions[idx]] = FAULT_ORDER[n % len(FAULT_ORDER)]
    return chosen


def _encode_raw(
    path: Path, frame_count: int, fps: int, width: int, height: int
) -> None:
    container = av.open(str(path), "w")
    try:
        stream = container.add_stream("h264", rate=fps)
        stream.width = width
        stream.height = height
        stream.pix_fmt = "yuv420p"
        stream.time_base = FIXTURE_TIME_BASE
        stream.gop_size = GOP_SIZE
        stream.max_b_frames = MAX_B_FRAMES
        stream.options = {"preset": "veryfast"}

        for i in range(frame_count):
            base = (i * 7) % 256
            rgb = np.zeros((height, width, 3), dtype=np.uint8)
            rgb[..., 0] = base
            rgb[..., 1] = (base + 85) % 256
            rgb[..., 2] = (base + 171) % 256
            frame = av.VideoFrame.from_ndarray(rgb, format="rgb24")
            frame.pts = i
            for packet in stream.encode(frame):
                container.mux(packet)
        for packet in stream.encode():
            container.mux(packet)
    finally:
        container.close()


def _remux_clean(raw_path: Path, out_path: Path, frame_count: int) -> None:
    """Re-mux the clean stream, assigning PTS by presentation position."""
    probe = av.open(str(raw_path))
    try:
        probe_stream = probe.streams.video[0]
        probe_packets = [
            p for p in probe.demux(probe_stream) if p.dts is not None
        ]
        # Rank packets by their source PTS to recover presentation order.
        order = sorted(
            range(len(probe_packets)), key=lambda i: probe_packets[i].pts
        )
        pres_of_packet = [0] * len(probe_packets)
        for pres, packet_i in enumerate(order):
            pres_of_packet[packet_i] = pres
    finally:
        probe.close()

    if len(probe_packets) != frame_count:
        raise UsageError(
            f"encoder produced {len(probe_packets)} packets, "
            f"expected {frame_count}"
        )

    src = av.open(str(raw_path))
    try:
        src_stream = src.streams.video[0]
        dst = av.open(str(out_path), "w", format="matroska")
        try:
            dst_stream = dst.add_stream_from_template(src_stream)
            packets = [
                p for p in src.demux(src_stream) if p.dts is not None
            ]
            for n, packet in enumerate(packets):
                pres = pres_of_packet[n]
                packet.stream = dst_stream
                packet.time_base = FIXTURE_TIME_BASE
                packet.pts = pres * round(1000 / 30)  # ms, 30 fps base
                packet.dts = None
                dst.mux(packet)
        finally:
            dst.close()
    finally:
        src.close()


def generate_fixture(
    out_path: str | Path,
    frame_count: int,
    fps: int,
    faults: int,
    width: int = 64,
    height: int = 64,
    overwrite: bool = False,
) -> tuple[Path, Path]:
    """Generate a clean clip plus its fault manifest.

    Returns ``(media_path, manifest_path)``.
    """
    if frame_count <= 0 or fps <= 0 or faults < 0:
        raise UsageError("frames and fps must be positive, faults >= 0")
    if faults > len(FAULT_ORDER):
        raise UsageError(
            f"at most {len(FAULT_ORDER)} fixture faults supported, got {faults}"
        )
    if fps != 30:
        # The remux step writes a fixed 30 fps PTS grid; keep the fixture
        # contract explicit rather than silently mixing rates.
        raise UsageError("fixture generation currently supports fps=30 only")

    out_path = Path(out_path)
    manifest_path = out_path.with_suffix(".faults.json")
    for target in (out_path, manifest_path):
        if target.exists() and not overwrite:
            raise OutputError(f"refusing to overwrite existing file: {target}")

    positions = _fault_positions(frame_count, faults)
    try:
        with tempfile.TemporaryDirectory(prefix="frameaudit-fixture-") as tmp:
            raw_path = Path(tmp) / "raw.mp4"
            _encode_raw(raw_path, frame_count, fps, width, height)
            _remux_clean(raw_path, out_path, frame_count)

        manifest = {
            "media": out_path.name,
            "frame_count": frame_count,
            "fps": fps,
            "time_base": "1/1000",
            "faults": [
                {
                    "presentation_position": pos,
                    "kind": kind,
                    "shift_frames": (
                        JUMP_SHIFT_FRAMES
                        if kind == "jump"
                        else BACKWARD_SHIFT_FRAMES
                        if kind == "backward"
                        else 0
                    ),
                }
                for pos, kind in sorted(positions.items())
            ],
        }
        manifest_path.write_text(json.dumps(manifest, indent=2) + "\n")
    except av.FFmpegError as exc:
        raise OutputError(
            f"failed to write fixture {out_path}: {exc.strerror or exc}"
        ) from exc
    return out_path, manifest_path
