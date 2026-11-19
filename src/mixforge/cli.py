"""Terminal entry point: parse arguments, render and emit a JSON summary.

Exit codes follow the project contract:
* ``0`` — success;
* ``2`` — invalid arguments or input/manifest errors;
* ``1`` — processing or file-write failures.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import tempfile
from dataclasses import asdict
from pathlib import Path

from . import __version__, automation
from .metrics import Metrics
from .model import ModelError, load_manifest
from .render import RenderError, render_offline, write_wav


def _atomic_write_text(path: Path, text: str) -> None:
    """Write via a temp file then atomically replace the destination."""

    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(
        prefix=f".{path.name}.", suffix=".tmp", dir=str(path.parent)
    )
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(text)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(tmp_name, path)
    except BaseException:
        try:
            os.unlink(tmp_name)
        except FileNotFoundError:
            pass
        raise


def _metrics_dict(metrics: Metrics) -> dict:
    return asdict(metrics)


def _automation_samples(config, total_frames: int) -> dict:
    samples: dict[str, list[dict]] = {}
    for track in config.tracks:
        if track.automation is None:
            continue
        curve_samples = automation.sample_curve(
            track.automation, total_frames, config.sample_rate
        )
        samples[track.name] = [
            {"frame": item.frame, "time": item.time, "gain": item.gain}
            for item in curve_samples
        ]
    return samples


def build_summary(config, result, mode: str, block_size: int) -> dict:
    bus_clips_pre = result.bus_pre_limiter.clips
    bus_clips_post = result.bus_post_limiter.clips
    return {
        "sample_rate": config.sample_rate,
        "channels": config.channels,
        "frames": result.frames,
        "mode": mode,
        "block_size": block_size,
        "limiter": config.limiter,
        "automation_samples": _automation_samples(config, result.frames),
        "tracks": {
            name: _metrics_dict(metrics)
            for name, metrics in result.track_metrics.items()
        },
        "bus": {
            **_metrics_dict(result.bus_post_limiter),
            "clips_pre_limiter": bus_clips_pre,
            "clips_post_limiter": bus_clips_post,
        },
    }


def cmd_render(args: argparse.Namespace) -> int:
    try:
        config = load_manifest(args.manifest)
    except ModelError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    if args.block_size is not None:
        if args.block_size <= 0:
            print("error: --block-size must be a positive integer", file=sys.stderr)
            return 2
        block_size = args.block_size
    else:
        block_size = config.block_size
    if args.limiter is not None:
        config = _with_limiter(config, args.limiter)
    try:
        if args.mode == "offline":
            result = render_offline(config)
        else:
            from .stream import render_stream

            result = render_stream(config, block_size)
        out_path = Path(args.out)
        write_wav(out_path, result.output, config.sample_rate)
        summary = build_summary(config, result, args.mode, block_size)
        _atomic_write_text(Path(args.summary), json.dumps(summary, indent=2, sort_keys=True))
    except RenderError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    except OSError as exc:
        print(f"error: failed to write output: {exc}", file=sys.stderr)
        return 1
    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0


def _with_limiter(config, enabled: bool):
    from dataclasses import replace

    return replace(config, limiter=enabled)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="mixforge", description="Deterministic local multi-track WAV mixer"
    )
    parser.add_argument("--version", action="version", version=__version__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    render_parser = subparsers.add_parser("render", help="render a mix session")
    render_parser.add_argument("--manifest", required=True, help="path to session JSON")
    render_parser.add_argument("--mode", choices=("offline", "stream"), default="stream")
    render_parser.add_argument("--block-size", type=int, default=None)
    limiter_group = render_parser.add_mutually_exclusive_group()
    limiter_group.add_argument("--limiter", dest="limiter", action="store_true")
    limiter_group.add_argument("--no-limiter", dest="limiter", action="store_false")
    render_parser.set_defaults(limiter=None)
    render_parser.add_argument("--out", required=True, help="output WAV path")
    render_parser.add_argument("--summary", required=True, help="output JSON summary path")
    render_parser.set_defaults(func=cmd_render)
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        return args.func(args)
    except ModelError as exc:  # safety net for validation outside cmd_render
        print(f"error: {exc}", file=sys.stderr)
        return 2
    except (RenderError, OSError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
