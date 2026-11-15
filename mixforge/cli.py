"""Command line interface: argument parsing, exit codes, JSON report.

Exit codes: 0 ok, 2 argument/manifest error, 3 media or processing
failure, 4 strict clip threshold (--max-clip-samples) exceeded.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import tempfile
from pathlib import Path

import numpy as np
import soundfile as sf

from .engine import OFFLINE_BLOCK, render_to_buffer, render_to_file
from .errors import EXIT_CLIP, EXIT_MEDIA, EXIT_OK, EXIT_USAGE, ManifestError, MediaError
from .fixtures import make_demo_project
from .graph import MixGraph
from .manifest import load_manifest


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="mixforge", description=__doc__)
    sub = p.add_subparsers(dest="cmd", required=True)
    demo = sub.add_parser("demo", help="generate fixtures, render, verify, report")
    _common(demo)
    demo.add_argument("--seed", type=int, default=0)
    demo.add_argument("--stress", action="store_true", help="boost the bus to force clipping")
    render = sub.add_parser("render", help="render an existing manifest")
    _common(render)
    render.add_argument("--manifest", required=True)
    return p


def _common(p: argparse.ArgumentParser) -> None:
    p.add_argument("--out", required=True, help="output WAV path")
    p.add_argument("--report", help="write the JSON report here")
    p.add_argument("--target-rate", type=int, default=None, help="output sample rate")
    p.add_argument("--block-frames", type=int, default=1024, help="streaming block size")
    p.add_argument("--max-clip-samples", type=int, default=None,
                   help="exit 4 when the mix clip count exceeds this")


def _check_args(args: argparse.Namespace) -> None:
    if args.target_rate is not None and args.target_rate <= 0:
        raise ManifestError("--target-rate must be positive")
    if args.block_frames <= 0:
        raise ManifestError("--block-frames must be positive")
    if args.max_clip_samples is not None and args.max_clip_samples < 0:
        raise ManifestError("--max-clip-samples must be >= 0")


def _write_report(path: str | Path, report: dict) -> None:
    path = Path(path)
    tmp = path.with_name(f"{path.name}.tmp-{os.getpid()}")
    try:
        tmp.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
        os.replace(tmp, path)
    except OSError as exc:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise MediaError(f"cannot write report {path}: {exc}") from exc


def _run(args: argparse.Namespace) -> int:
    _check_args(args)
    if args.cmd == "demo":
        work = Path(tempfile.mkdtemp(prefix="mixforge-demo-"))
        manifest_path, master_db = make_demo_project(work, seed=args.seed, stress=args.stress)
        print(f"fixtures: {work}")
    else:
        manifest_path, master_db = Path(args.manifest), 0.0

    project = load_manifest(manifest_path)
    target_rate = args.target_rate or project.target_rate

    # Offline render -> WAV (validated inputs, atomic output).
    with MixGraph(project, target_rate, master_gain_db=master_db) as graph:
        stats = render_to_file(graph, args.out, OFFLINE_BLOCK)
        total_frames = graph.frames

    # Streaming render of the same manifest -> buffer, then compare.
    with MixGraph(project, target_rate, master_gain_db=master_db) as graph2:
        streamed, _ = render_to_buffer(graph2, args.block_frames)
    try:
        offline, _ = sf.read(str(args.out), dtype="float64", always_2d=True)
    except Exception as exc:
        raise MediaError(f"cannot read back output {args.out}: {exc}") from exc
    if offline.shape != streamed.shape:
        raise MediaError(
            f"offline/streaming frame mismatch: {offline.shape} vs {streamed.shape}"
        )
    max_abs = float(np.abs(offline - streamed).max()) if offline.size else 0.0

    automation_samples = [
        {"track": t.name, **pt}
        for t in graph2.tracks
        if t.curve is not None
        for pt in t.curve.sample_points()
    ]
    report = {
        "tracks": len(project.tracks),
        "rate": target_rate,
        "frames": total_frames,
        "block_frames": args.block_frames,
        "stress": bool(getattr(args, "stress", False)),
        "offline_stream_max_abs": max_abs,
        "clip_samples": stats.clip.count,
        "track_metrics": [m.as_dict() for m in stats.track_meters],
        "automation_samples": automation_samples,
    }
    if args.report:
        _write_report(args.report, report)

    print(f"tracks={report['tracks']} rate={target_rate} frames={total_frames} "
          f"clip_samples={stats.clip.count} offline_stream_max_abs={max_abs:.3e}")
    for m in stats.track_meters:
        print(f"track {m.name}: peak={m.peak:.4f} rms={m.rms:.4f}")
    if args.report:
        print(f"report: {args.report}")
    print(f"out: {args.out}")

    if args.max_clip_samples is not None and stats.clip.count > args.max_clip_samples:
        print(f"clip threshold exceeded: {stats.clip.count} > {args.max_clip_samples}",
              file=sys.stderr)
        return EXIT_CLIP
    return EXIT_OK


def main(argv=None) -> int:
    args = build_parser().parse_args(argv)
    try:
        return _run(args)
    except ManifestError as exc:
        print(f"manifest error: {exc}", file=sys.stderr)
        return EXIT_USAGE
    except MediaError as exc:
        print(f"media error: {exc}", file=sys.stderr)
        return EXIT_MEDIA
