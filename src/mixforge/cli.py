"""Command-line entry point: render, stream and compare modes."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any, Sequence

import numpy as np

from .automation import automation_samples
from .errors import (
    AudioIOError,
    MixForgeError,
    ProcessingError,
)
from .graph import MixGraph
from .io import WavWriter, read_json, write_json
from .model import Project, parse_project
from .render import OFFLINE_CHUNK, render_offline, validate_block_size
from .stats import Summary, compare_samples
from .stream import stream_render

EXIT_OK = 0
EXIT_FAILURE = 1
EXIT_CONFIG = 2
COMPARE_BATCH_BLOCKS = 16


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="mixforge", description="Deterministic offline/streaming timeline mixer"
    )
    sub = parser.add_subparsers(dest="command", required=True)

    render = sub.add_parser("render", help="render a project to WAV and print JSON stats")
    render.add_argument("--project", required=True, type=Path)
    render.add_argument("--mode", choices=("offline", "stream", "compare"), default="offline")
    render.add_argument("--block-size", type=int, default=1024)
    render.add_argument("--output", type=Path, default=None)
    render.add_argument("--summary", type=Path, default=None)

    inspect = sub.add_parser("inspect", help="validate a project and print its plan")
    inspect.add_argument("--project", required=True, type=Path)
    inspect.add_argument("--block-size", type=int, default=1024)
    return parser


def load_project(path: Path) -> Project:
    if not path.exists():
        raise AudioIOError("project file does not exist", location=str(path))
    data = read_json(path)
    return parse_project(data, source_dir=path.parent, source=str(path))


def _compare_streaming(
    project: Project, block_size: int, *, output: Path | None = None
) -> tuple[Summary, float, int | None]:
    """Interleave streaming blocks with lazily rendered offline chunks.

    Only one offline render chunk and one comparison batch are resident at a
    time.  When ``output`` is given, the streaming master is written as it is
    produced, so the result is never assembled in memory.
    """

    stream_blocks, stream_summary = stream_render(project, block_size=block_size)
    batch_target = min(COMPARE_BATCH_BLOCKS * block_size, OFFLINE_CHUNK)
    max_diff = 0.0
    first_diff: int | None = None
    compared = 0
    consumed = 0
    reference = np.zeros((0, project.channels), dtype=np.float64)
    reference_start = 0
    reference_end = 0
    pending: list[np.ndarray] = []
    pending_frames = 0
    writer = WavWriter(output, project.sample_rate, project.channels) if output else None
    ok = False

    def ensure_reference(end: int, graph: MixGraph) -> None:
        nonlocal reference, reference_start, reference_end
        if end <= reference_end:
            return
        parts = [reference]
        while reference_end < end:
            take = min(OFFLINE_CHUNK, project.frames - reference_end)
            if take <= 0:
                break
            parts.append(graph.render_block(reference_end, take).master)
            reference_end += take
        reference = np.concatenate(parts, axis=0)
        if compared > reference_start:
            # Drop already-compared frames so the reference window stays
            # bounded by the batch size, not the project length.
            reference = reference[compared - reference_start :]
            reference_start = compared

    def check(joined: np.ndarray, graph: MixGraph) -> None:
        nonlocal max_diff, first_diff, compared
        ensure_reference(compared + joined.shape[0], graph)
        offset = compared - reference_start
        local_max, local_first = compare_samples(
            reference[offset : offset + joined.shape[0]], joined
        )
        if local_max > max_diff:
            max_diff = local_max
        if first_diff is None and local_first is not None:
            first_diff = compared + local_first
        compared += joined.shape[0]

    def flush_batch(graph: MixGraph) -> None:
        nonlocal pending_frames
        if not pending:
            return
        check(np.concatenate(pending, axis=0), graph)
        pending.clear()
        pending_frames = 0

    try:
        with MixGraph(project) as graph:
            for block in stream_blocks:
                consumed += 1
                if writer is not None:
                    writer.write(block)
                pending.append(block)
                pending_frames += block.shape[0]
                if pending_frames >= batch_target:
                    flush_batch(graph)
            flush_batch(graph)
            if compared != project.frames:
                raise ProcessingError(
                    "streaming output length differs from the project duration",
                    location="compare",
                    context={
                        "stream_frames": compared,
                        "project_frames": project.frames,
                        "streamed_blocks": consumed,
                        "block_size": block_size,
                    },
                )
            if writer is not None:
                if max_diff != 0.0:
                    # Never leave a mismatched preview that looks valid.
                    writer.abort()
                else:
                    writer.close()
            ok = True
    finally:
        if writer is not None and not ok:
            writer.abort()
    stream_summary.extra["streamed_blocks"] = consumed
    return stream_summary, max_diff, first_diff


def run_render(args: argparse.Namespace) -> tuple[dict[str, Any], int]:
    block_size = validate_block_size(args.block_size)
    project = load_project(args.project)

    if args.mode == "compare":
        summary, max_diff, first_diff = _compare_streaming(
            project, block_size, output=args.output
        )
        payload = summary.as_dict()
        payload.update(
            {
                "mode": "compare",
                "max_abs_diff": max_diff,
                "first_diff_sample": first_diff,
                "identical": max_diff == 0.0,
            }
        )
        if max_diff != 0.0:
            payload["status"] = "mismatch"
            payload["error"] = "offline and streaming samples differ"
            if args.summary is not None:
                write_json(args.summary, payload)
            return payload, EXIT_FAILURE
        if args.summary is not None:
            write_json(args.summary, payload)
        return payload, EXIT_OK

    if args.mode == "stream":
        blocks, summary = stream_render(project, block_size=block_size)
        writer = (
            WavWriter(args.output, project.sample_rate, project.channels)
            if args.output
            else None
        )
        ok = False
        try:
            for block in blocks:
                if writer is not None:
                    writer.write(block)
            if writer is not None:
                writer.close()
            ok = True
        finally:
            if writer is not None and not ok:
                writer.abort()
        summary.mode = "stream"
    else:
        audio, summary = render_offline(project, block_size=block_size)
        summary.mode = "offline"
        if args.output is not None:
            from .io import write_wav

            write_wav(args.output, audio, project.sample_rate)

    payload = summary.as_dict()
    if args.summary is not None:
        write_json(args.summary, payload)
    return payload, EXIT_OK


def run_inspect(args: argparse.Namespace) -> tuple[dict[str, Any], int]:
    block_size = validate_block_size(args.block_size)
    project = load_project(args.project)
    payload: dict[str, Any] = {
        "status": "ok",
        "sample_rate": project.sample_rate,
        "channels": project.channels,
        "frames": project.frames,
        "block_size": block_size,
        "tracks": [
            {
                "id": track.id,
                "path": track.path,
                "start_frame": track.start_frame,
                "gain_db": track.gain_db,
                "mute": track.mute,
                "bus": track.bus,
                "automation": (
                    None
                    if track.automation is None
                    else {
                        "interpolation": track.automation.interpolation,
                        "points": [
                            {"frame": point.frame, "gain_db": point.gain_db}
                            for point in track.automation.points
                        ],
                    }
                ),
                "fade": {
                    "in_frames": track.fade.in_frames,
                    "out_frames": track.fade.out_frames,
                },
            }
            for track in project.tracks
        ],
        "buses": [
            {"id": bus.id, "gain_db": bus.gain_db, "mute": bus.mute} for bus in project.buses
        ],
        "master": {
            "gain_db": project.master.gain_db,
            "limiter": project.master.limiter,
            "ceiling": project.master.ceiling,
        },
        "loop": (
            None
            if project.loop is None
            else {
                "start_frame": project.loop.start_frame,
                "end_frame": project.loop.end_frame,
                "count": project.loop.count,
            }
        ),
        "automation_samples": automation_samples(project, block_size),
    }
    return payload, EXIT_OK


def emit(payload: dict[str, Any]) -> None:
    print(json.dumps(payload, indent=2, ensure_ascii=False, allow_nan=False))


def main(argv: Sequence[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        if args.command == "render":
            payload, code = run_render(args)
        else:
            payload, code = run_inspect(args)
    except MixForgeError as exc:
        emit(exc.as_dict())
        return exc.exit_code
    except (OSError, ValueError) as exc:
        emit(ProcessingError(f"unexpected failure: {exc}").as_dict())
        return EXIT_FAILURE
    emit(payload)
    return code


if __name__ == "__main__":
    sys.exit(main())
