"""Terminal entry point for mixforge.

Exit codes: 0 success, 2 manifest/argument error, 3 media or render failure,
4 offline/streaming disagreement.
"""

from __future__ import annotations

import argparse
import json
import sys
import tempfile
from pathlib import Path

import numpy as np

from .audio_io import write_float_wav
from .engine import process
from .errors import ConsistencyError, ManifestError, MixforgeError, RenderError
from .manifest import load_manifest

REVIEW_TIME_LIMIT = 64


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="mixforge",
        description="Deterministic offline/streaming audio mixdown.",
    )
    commands = parser.add_subparsers(dest="command", required=True)

    render = commands.add_parser("render", help="render a session manifest")
    render.add_argument("--manifest", required=True, type=Path,
                        help="path to session.json")
    render.add_argument("--rate", type=int, default=None,
                        help="output sample rate (default: manifest or 48000)")
    render.add_argument("--block-frames", type=int, default=None,
                        help="stream in blocks of this many frames "
                             "(default: single offline pass)")
    render.add_argument("--output", type=Path, default=None,
                        help="write the mixed master as 32-bit float WAV")
    render.add_argument("--report", type=Path, default=None,
                        help="write a JSON report of levels, automation and "
                             "offline/stream consistency")
    return parser


def _review_times(session, total_ms: float) -> list[float]:
    """Curve knot times plus evenly spaced review points, deduplicated."""
    times = {0.0, total_ms}
    for track in session.tracks:
        if track.automation is not None:
            times.update(p.ms for p in track.automation.points if p.ms <= total_ms)
    for bus in session.buses:
        if bus.automation is not None:
            times.update(p.ms for p in bus.automation.points if p.ms <= total_ms)
    base = sorted(times)
    if len(base) < 4:
        for i in range(1, 5):
            base.append(total_ms * i / 5.0)
        base = sorted(set(base))
    return base


def _report_payload(result: dict, session, rate: int) -> dict:
    graph = result["graph"]
    stats = result["stats"]
    total_ms = graph.total_frames * 1000.0 / rate
    times = _review_times(session, total_ms)
    track_gains = graph.gain_samples(np.array(times, dtype=np.float64))
    bus_gains = graph.bus_gain_samples(np.array(times, dtype=np.float64))
    comparison = result["comparison"]
    stream = result["stream"]
    return {
        "manifest": str(session.manifest_path),
        "rate": rate,
        "frames": graph.total_frames,
        "duration_ms": round(total_ms, 6),
        "tracks": [
            {
                "id": program.track.id,
                "source_rate": program.info.rate,
                "source_channels": program.info.channels,
                "source_frames": program.info.frames,
                "offset_ms": program.track.offset_ms,
                "offset_frame": program.start_frame,
                "bus": program.bus,
                "gain_db": program.track.gain_db,
                **stats["tracks"][program.track.id],
                "automation_samples": [
                    {"ms": float(ms), "gain": float(gain)}
                    for ms, gain in zip(times, track_gains[program.track.id])
                ],
            }
            for program in graph.programs
        ],
        "buses": [
            {
                "id": bus_id,
                "gain_db": graph.buses[bus_id].gain_db,
                "automation_samples": [
                    {"ms": float(ms), "gain": float(gain)}
                    for ms, gain in zip(times, bus_gains[bus_id])
                ],
            }
            for bus_id in graph.buses
        ],
        "master": stats["master"],
        "offline_stream_max_error": (
            comparison.max_error if comparison is not None else 0.0),
        "first_diff_frame": (
            comparison.first_diff_frame if comparison is not None else None),
        "first_diff_channel": (
            comparison.first_diff_channel if comparison is not None else None),
        "block_frames": (stream.block_lengths[0]
                         if stream is not None and stream.block_lengths else None),
        "block_count": len(stream.block_lengths) if stream is not None else 0,
        "last_block_frames": (
            stream.block_lengths[-1]
            if stream is not None and stream.block_lengths else None),
    }


def _write_report(path: Path, payload: dict) -> None:
    try:
        path.parent.mkdir(parents=True, exist_ok=True)
        fd, tmp_name = tempfile.mkstemp(prefix=".mixforge-report-",
                                        suffix=".json", dir=str(path.parent))
    except OSError as exc:
        raise RenderError(f"cannot prepare report path: {exc}",
                          field="report") from exc
    try:
        with open(fd, "w", encoding="utf-8") as handle:
            json.dump(payload, handle, indent=2)
            handle.write("\n")
        Path(tmp_name).replace(path)
    except (OSError, TypeError, ValueError) as exc:
        try:
            Path(tmp_name).unlink()
        except OSError:
            pass
        raise RenderError(f"cannot write report: {exc}", field="report") from exc


def _print_summary(payload: dict) -> None:
    print(f"tracks={len(payload['tracks'])} rate={payload['rate']} "
          f"frames={payload['frames']}")
    for track in payload["tracks"]:
        print(
            f"track={track['id']} peak={track['peak']:.6f} rms={track['rms']:.6f} "
            f"automation_samples={len(track['automation_samples'])} "
            f"clip_count={track['clip_count']}"
        )
    master = payload["master"]
    print(f"master peak={master['peak']:.6f} rms={master['rms']:.6f} "
          f"clip_count={master['clip_count']}")
    max_error = payload["offline_stream_max_error"]
    if payload["block_count"]:
        print(f"blocks={payload['block_count']} "
              f"block_frames={payload['block_frames']} "
              f"last_block_frames={payload['last_block_frames']}")
    print(f"offline_stream_max_error={max_error:.3e}")


def _command_render(args: argparse.Namespace) -> int:
    session = load_manifest(args.manifest)
    rate = args.rate if args.rate is not None else (session.rate or 48000)
    if rate <= 0:
        raise ManifestError("--rate must be > 0", field="--rate")
    _refuse_overwriting_inputs(args, session)
    try:
        result = process(session, rate, args.block_frames)
        payload = _report_payload(result, session, rate)
        if args.output is not None:
            write_float_wav(args.output, result["master"], rate)
    except MixforgeError as exc:
        _preserve_error_report(args, session, rate, exc)
        raise
    if args.report is not None:
        _write_report(args.report, payload)
    _print_summary(payload)
    return 0


def _refuse_overwriting_inputs(args: argparse.Namespace, session) -> None:
    protected = {track.path.resolve() for track in session.tracks}
    protected.add(session.manifest_path.resolve())
    for option, path in (("--output", args.output), ("--report", args.report)):
        if path is not None and path.resolve() in protected:
            raise ManifestError(
                f"{option} would overwrite an input file: {path}",
                field=option)


def _preserve_error_report(args: argparse.Namespace, session, rate: int,
                           exc: MixforgeError) -> None:
    """Write an error report next to --report so a failure stays diagnosable."""
    if args.report is None:
        return
    payload = {
        "status": "error",
        "manifest": str(session.manifest_path),
        "rate": rate,
        "error": exc.as_dict(),
    }
    try:
        _write_report(args.report, payload)
    except MixforgeError:
        pass  # the original error is the one worth reporting


def main(argv: list[str] | None = None) -> int:
    parser = _build_parser()
    try:
        args = parser.parse_args(argv)
    except SystemExit as exc:  # argparse already printed usage
        return int(exc.code) if isinstance(exc.code, int) else 2
    try:
        if args.command == "render":
            return _command_render(args)
    except MixforgeError as exc:
        print(f"error: {exc}", file=sys.stderr)
        if isinstance(exc, ManifestError) and not (exc.field or "").startswith("--"):
            print("hint: check the manifest fields named above", file=sys.stderr)
        return exc.exit_code
    except KeyboardInterrupt:
        print("error: interrupted", file=sys.stderr)
        return 3
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
