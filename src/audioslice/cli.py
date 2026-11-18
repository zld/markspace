"""Terminal interface: argument parsing, summaries, exit codes.

Exit codes: 0 = success, 2 = invalid arguments, 3 = input error,
4 = batch completed but some files failed.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from .audio import AudioError
from .batch import BatchConfig, InputError, process_file, run_batch
from .detector import DetectorParams
from .segments import SegmentParams

EXIT_OK = 0
EXIT_ARGS = 2
EXIT_INPUT = 3
EXIT_PARTIAL = 4


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="audioslice",
        description="Extract dialogue/sound events from PCM recordings.",
    )
    sub = parser.add_subparsers(dest="command", required=True)
    for name, help_text in (("batch", "process every .wav in a directory"),
                            ("file", "process a single PCM file")):
        p = sub.add_parser(name, help=help_text)
        p.add_argument("--input", required=True,
                       help="input directory (batch) or .wav file (file)")
        p.add_argument("--json", action="store_true", help="emit JSON output")
        if name == "batch":
            p.add_argument("--db", required=True, help="SQLite database path")
        det = p.add_argument_group("detector")
        det.add_argument("--frame-ms", type=float, default=25.0)
        det.add_argument("--hop-ms", type=float, default=10.0)
        det.add_argument("--noise-quantile", type=float, default=0.3)
        det.add_argument("--onset-factor", type=float, default=3.0)
        det.add_argument("--offset-factor", type=float, default=1.5)
        seg = p.add_argument_group("segments")
        seg.add_argument("--min-event-ms", type=float, default=120.0)
        seg.add_argument("--min-silence-ms", type=float, default=250.0)
        seg.add_argument("--merge-gap-ms", type=float, default=80.0)
    return parser


def _config_from_args(args, parser) -> BatchConfig:
    config = BatchConfig(
        detector=DetectorParams(
            frame_ms=args.frame_ms, hop_ms=args.hop_ms,
            noise_quantile=args.noise_quantile,
            onset_factor=args.onset_factor, offset_factor=args.offset_factor,
        ),
        segments=SegmentParams(
            min_event_ms=args.min_event_ms,
            min_silence_ms=args.min_silence_ms,
            merge_gap_ms=args.merge_gap_ms,
        ),
    )
    try:
        config.detector.validate()
        config.segments.validate()
    except ValueError as exc:
        parser.error(str(exc))  # exits with code 2
    return config


def _print_file_human(res: dict) -> None:
    print(f"file={res['path']}")
    print(f"  sample_rate={res['sample_rate']} channels={res['channels']} "
          f"samples={res['num_samples']}")
    print(f"  noise_floor={res['noise_floor']:.6f} "
          f"high_threshold={res['high_threshold']:.6f} "
          f"low_threshold={res['low_threshold']:.6f}")
    print(f"  segments={len(res['segments'])} "
          f"silence_total_seconds={res['silence_total_seconds']:.3f} "
          f"(includes leading/trailing silence)")
    for i, s in enumerate(res["segments"]):
        print(f"  [{i}] start_sample={s['start_sample']} "
              f"end_sample={s['end_sample']} "
              f"duration_seconds={s['duration_seconds']:.3f} "
              f"peak={s['peak']:.4f} avg_rms={s['avg_rms']:.6f}")


def _print_batch_human(summary: dict) -> None:
    print(f"files={summary['files']} ok={summary['succeeded']} "
          f"failed={summary['failed']} segments={summary['segments']} "
          f"silence_total_seconds={summary['silence_total_seconds']:.3f}")
    for res in summary["results"]:
        _print_file_human(res)
    for err in summary["errors"]:
        print(f"error: {err['path']}: {err['error']}", file=sys.stderr)


def main(argv=None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    config = _config_from_args(args, parser)

    if args.command == "batch":
        try:
            summary = run_batch(args.input, args.db, config)
        except InputError as exc:
            print(f"error: {exc}", file=sys.stderr)
            return EXIT_INPUT
        if args.json:
            print(json.dumps(summary, indent=2))
        else:
            _print_batch_human(summary)
        return EXIT_PARTIAL if summary["failed"] else EXIT_OK

    # single file
    if not Path(args.input).is_file():
        print(f"error: input file not found: {args.input}", file=sys.stderr)
        return EXIT_INPUT
    try:
        res = process_file(args.input, config)
    except AudioError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_INPUT
    if args.json:
        print(json.dumps(res, indent=2))
    else:
        _print_file_human(res)
    return EXIT_OK


if __name__ == "__main__":
    sys.exit(main())
