"""Synthetic replay-clip fixture generation.

Container muxers reject non-monotonic packet timestamps, so the four replay
fault classes cannot all be embedded reliably at the container level.
Instead the fixture is built as two deterministic artefacts:

1. a clean H.264 clip (regular GOP, real I/P/B frames) re-muxed into an
   MP4 container on an exact 90 kHz time base;
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
# 90 kHz is the classic MPEG time base: 30 fps maps to an exact 3000-tick
# frame step, so every timestamp and the repaired cadence stay rational with
# no millisecond rounding error.
FIXTURE_TIME_BASE = Fraction(1, 90000)
FRAME_STEP_TICKS = 3000
GOP_SIZE = 25
MAX_B_FRAMES = 3
# Jump distortion in frame durations.  A +1 frame shift makes the gap twice a
# normal frame duration (66.7 ms at 30 fps), which just crosses the 1.5x
# repair threshold (50 ms).  The excess over one frame duration is therefore
# exactly one frame interval — the 33.3 ms reported as max_jump_ms.
JUMP_SHIFT_FRAMES = 1
# Backward distortion in frame durations.
BACKWARD_SHIFT_FRAMES = 2


def _fault_positions(frame_count: int, faults: int) -> dict[int, str]:
    if faults < 0 or faults > frame_count - 1:
        raise UsageError(
            f"cannot inject {faults} faults into {frame_count} frames"
        )
    chosen: dict[int, str] = {}
    kinds = [FAULT_ORDER[n % len(FAULT_ORDER)] for n in range(faults)]

    # A +1-frame jump shares a PTS with the following frame, which would read
    # as a second (duplicate) repair.  Placing the jump on the final frame
    # removes the follower, so the jump is the sole repair at that position.
    if "jump" in kinds:
        chosen[frame_count - 1] = "jump"
        kinds.remove("jump")

    # Remaining faults land on well-separated interior IDR frames.
    interior = [k for k in kinds if k != "jump"]
    if interior:
        idr_positions = list(range(GOP_SIZE, frame_count - 1, GOP_SIZE))
        if len(idr_positions) < len(interior):
            raise UsageError(
                f"need at least {len(interior)} interior IDR frames for fault "
                f"injection, clip of {frame_count} frames only has "
                f"{len(idr_positions)}"
            )
        for n, kind in enumerate(interior):
            idx = (n * len(idr_positions)) // max(len(interior), 1)
            chosen[idr_positions[idx]] = kind
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
        dst = av.open(str(out_path), "w", format="mp4")
        try:
            dst_stream = dst.add_stream_from_template(src_stream)
            packets = [
                p for p in src.demux(src_stream) if p.dts is not None
            ]
            for n, packet in enumerate(packets):
                pres = pres_of_packet[n]
                packet.stream = dst_stream
                packet.time_base = FIXTURE_TIME_BASE
                packet.pts = pres * FRAME_STEP_TICKS  # exact 30 fps grid
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
            "time_base": "1/90000",
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
