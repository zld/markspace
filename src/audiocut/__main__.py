"""Command line entry point: ``python -m audiocut ...``.

Exit codes:
  0 - every input processed successfully
  1 - invalid parameters / configuration
  2 - one or more inputs were corrupt or failed processing
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from . import fixtures
from .batch import format_text, outcome_to_dict, run_batch
from .detector import DetectorConfig


class _ArgumentParser(argparse.ArgumentParser):
    """ArgumentParser whose usage errors map to exit code 1."""

    def error(self, message: str) -> None:  # pragma: no cover - exercised via CLI
        self.print_usage(sys.stderr)
        self.exit(1, f"{self.prog}: error: {message}\n")


def _add_detector_args(parser: argparse.ArgumentParser) -> None:
    defaults = DetectorConfig()
    parser.add_argument("--frame-ms", type=float, default=defaults.frame_ms)
    parser.add_argument("--block-seconds", type=float, default=defaults.block_seconds)
    parser.add_argument("--noise-quantile", type=float, default=defaults.noise_quantile)
    parser.add_argument("--voice-factor", type=float, default=defaults.voice_factor)
    parser.add_argument("--silence-factor", type=float, default=defaults.silence_factor)
    parser.add_argument("--min-segment-s", type=float, default=defaults.min_segment_s)
    parser.add_argument("--min-silence-s", type=float, default=defaults.min_silence_s)
    parser.add_argument("--merge-gap-s", type=float, default=defaults.merge_gap_s)
    parser.add_argument("--db", default=None, help="optional SQLite database path")
    parser.add_argument("--json", action="store_true", help="emit JSON instead of text")


def _config_from_args(args: argparse.Namespace) -> DetectorConfig:
    return DetectorConfig(
        frame_ms=args.frame_ms,
        block_seconds=args.block_seconds,
        noise_quantile=args.noise_quantile,
        voice_factor=args.voice_factor,
        silence_factor=args.silence_factor,
        min_segment_s=args.min_segment_s,
        min_silence_s=args.min_silence_s,
        merge_gap_s=args.merge_gap_s,
    )


def _emit(report, as_json: bool) -> None:
    if as_json:
        print(json.dumps(
            {"results": [outcome_to_dict(o) for o in report.outcomes]},
            indent=2, sort_keys=True,
        ))
    else:
        print(format_text(report))


def build_parser() -> argparse.ArgumentParser:
    parser = _ArgumentParser(prog="audiocut", description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    analyze = sub.add_parser("analyze", help="detect segments and silence in WAV files")
    analyze.add_argument("paths", nargs="+", help="WAV files or directories")
    _add_detector_args(analyze)

    export = sub.add_parser("export", help="slice WAV files into a directory")
    export.add_argument("paths", nargs="+", help="WAV files or directories")
    export.add_argument("--outdir", required=True, help="output directory for slices")
    _add_detector_args(export)

    demo = sub.add_parser("demo", help="generate a synthetic fixture and run the pipeline")
    demo.add_argument("--workdir", required=True, help="directory for demo inputs/outputs")
    _add_detector_args(demo)
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)

    try:
        config = _config_from_args(args)
        config.validate()
    except ValueError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1

    if args.command == "demo":
        workdir = Path(args.workdir)
        workdir.mkdir(parents=True, exist_ok=True)
        source = workdir / "demo_voice.wav"
        fixtures.create_demo_wav(source)
        outdir = workdir / "slices"
        db_path = str(workdir / "audiocut.sqlite3")
        report = run_batch(
            [str(source)], config=config, db_path=db_path, outdir=str(outdir)
        )
        _emit(report, args.json)
        return 2 if report.has_errors else 0

    if not args.paths:
        print("error: at least one input path is required", file=sys.stderr)
        return 1
    outdir = getattr(args, "outdir", None)
    report = run_batch(args.paths, config=config, db_path=args.db, outdir=outdir)
    _emit(report, args.json)
    return 2 if report.has_errors else 0


if __name__ == "__main__":
    raise SystemExit(main())
