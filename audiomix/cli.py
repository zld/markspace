"""Command-line interface: argument parsing, exit codes, report output.

Exit codes: 0 success, 2 input/manifest error, 3 processing/write failure.
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

from .curve import CurveError
from .graph import build_graph
from .metrics import automation_report, build_report, signal_stats
from .model import (InputError, ManifestError, ProcessError, WriteError,
                    load_manifest, load_track_audio)
from .offline import render_offline
from .stream import render_stream


def _positive_int(text):
    value = int(text)
    if value <= 0:
        raise argparse.ArgumentTypeError(f"must be a positive integer: {text!r}")
    return value


def _parse(argv):
    parser = argparse.ArgumentParser(
        prog="audiomix",
        description="Local multitrack mixing tool (offline + streamed render).")
    sub = parser.add_subparsers(dest="command", required=True)
    for name in ("mix", "demo"):
        p = sub.add_parser(name, help="mix from a manifest" if name == "mix"
                           else "generate demo tracks and mix them")
        if name == "mix":
            p.add_argument("--manifest", required=True,
                           help="path to the JSON mix manifest")
        p.add_argument("--out", required=True, help="output WAV path (float32)")
        p.add_argument("--chunk-frames", type=_positive_int, default=4096,
                       help="streaming block size in frames")
        p.add_argument("--curve-samples", type=_positive_int, default=5,
                       help="automation sample points per track in the report")
        p.add_argument("--report", default="-",
                       help="report destination: '-' for stdout or a file path")
    return parser.parse_args(argv)


def write_output(path, data, sample_rate):
    """Write float WAV via a temp file + rename; failures leave no file."""
    path = Path(path)
    try:
        path.parent.mkdir(parents=True, exist_ok=True)
    except OSError as exc:
        raise WriteError(
            f"cannot create output directory '{path.parent}': {exc}") from exc
    fd, tmp = tempfile.mkstemp(prefix=path.name + ".", suffix=".part",
                               dir=str(path.parent))
    os.close(fd)
    try:
        sf.write(tmp, data, sample_rate, format="WAV", subtype="FLOAT")
        os.replace(tmp, path)
    except Exception as exc:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        if isinstance(exc, WriteError):
            raise
        raise WriteError(f"cannot write output '{path}': {exc}") from exc


def _load_fixture_builder():
    try:
        from scripts.generate_fixture import build_demo_manifest
    except ImportError:  # running from outside the repository root
        root = str(Path(__file__).resolve().parent.parent)
        if root not in sys.path:
            sys.path.insert(0, root)
        from scripts.generate_fixture import build_demo_manifest
    return build_demo_manifest


def _run_manifest(manifest, args):
    loaded = [(spec, *load_track_audio(spec)) for spec in manifest.tracks]
    rate = manifest.sample_rate

    offline_mix = render_offline(build_graph(loaded, rate))
    stream_mix = render_stream(build_graph(loaded, rate), args.chunk_frames)
    if offline_mix.shape != stream_mix.shape:
        raise ProcessError(
            f"offline/stream frame mismatch: {offline_mix.shape} vs {stream_mix.shape}")
    max_err = float(np.max(np.abs(offline_mix - stream_mix))) \
        if offline_mix.size else 0.0

    write_output(args.out, offline_mix, rate)

    track_reports = [
        {"name": spec.name, **signal_stats(audio)} for spec, audio, _ in loaded
    ]
    duration = offline_mix.shape[0] / rate
    times = np.linspace(0.0, duration, args.curve_samples)
    automation = automation_report(
        [(spec.name, spec.curve) for spec, _, _ in loaded], times)
    return build_report(rate, offline_mix, track_reports, automation, max_err)


def _run(args):
    if args.command == "demo":
        builder = _load_fixture_builder()
        with tempfile.TemporaryDirectory(prefix="audiomix-demo-") as tmp:
            manifest = load_manifest(builder(tmp))
            return _run_manifest(manifest, args)
    return _run_manifest(load_manifest(args.manifest), args)


def _emit_report(report, destination):
    text = json.dumps(report, indent=2)
    if destination == "-":
        print(text)
    else:
        Path(destination).write_text(text + "\n")


def main(argv=None):
    args = _parse(argv)
    try:
        report = _run(args)
    except (ManifestError, InputError, CurveError) as exc:
        print(f"input error: {exc}", file=sys.stderr)
        return 2
    except (ProcessError, WriteError) as exc:
        print(f"processing error: {exc}", file=sys.stderr)
        return 3
    except Exception as exc:  # unexpected processing failure
        print(f"processing error: {exc}", file=sys.stderr)
        return 3
    try:
        _emit_report(report, args.report)
    except OSError as exc:
        print(f"processing error: cannot write report: {exc}", file=sys.stderr)
        return 3
    return 0
