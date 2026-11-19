"""Command-line interface with stable exit codes.

Exit codes: 0 success, 1 at least one file failed, 2 usage/input error.
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

from . import __version__
from .batch import run_batch
from .config import (  # noqa: F401  (CHANNEL_MODES re-exported for callers)
    CHANNEL_MODES,
    DEFAULT_BLOCK_SIZE,
    Config,
    ResampleConfig,
    _valid_channel_mode,
)
from .errors import EXIT_FILE_FAILED, EXIT_OK, EXIT_USAGE, AudioPipeError, UsageError
from .report import render_terminal, write_report

DEFAULT_MAX_FILE_MB = 1024


def _channel_mode(value: str) -> str:
    """Accept 'preserve', 'mono', or an exact channel count ('1', 'exact:2')."""
    if _valid_channel_mode(value):
        return value
    raise argparse.ArgumentTypeError(
        "must be 'preserve', 'mono' or an exact channel count such as '2'"
    )


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="audiopipe",
        description="Resample and loudness-normalise PCM WAV assets for a game engine.",
    )
    parser.add_argument("--version", action="version", version=f"audiopipe {__version__}")
    sub = parser.add_subparsers(dest="command", required=True)

    batch = sub.add_parser("batch", help="process a file or directory tree")
    batch.add_argument("--input", required=True, type=Path)
    batch.add_argument("--output", required=True, type=Path)
    batch.add_argument("--rate", required=True, type=int, help="target sample rate in Hz")
    batch.add_argument(
        "--channels",
        type=_channel_mode,
        default="preserve",
        help="'preserve' channel order (default), 'mono' average, or an exact count to enforce",
    )
    batch.add_argument("--target-lufs", type=float, default=None)
    batch.add_argument("--max-peak-dbfs", type=float, default=None)
    batch.add_argument("--jobs", type=int, default=1)
    batch.add_argument("--block-size", type=int, default=DEFAULT_BLOCK_SIZE)
    batch.add_argument("--zero-crossings", type=int, default=32)
    batch.add_argument("--rolloff", type=float, default=0.945)
    batch.add_argument("--max-file-mb", type=int, default=DEFAULT_MAX_FILE_MB)
    batch.add_argument("--report", type=Path, default=None, help="batch summary JSON")
    batch.add_argument(
        "--report-dir",
        type=Path,
        default=None,
        help="write one JSON per input under this directory",
    )
    batch.add_argument("--quiet", action="store_true")
    return parser


def config_from_args(args: argparse.Namespace) -> Config:
    if args.max_file_mb < 1:
        raise UsageError("--max-file-mb must be >= 1")
    if args.jobs < 1:
        raise UsageError("--jobs must be >= 1")
    return Config(
        target_rate=args.rate,
        channels=args.channels,
        target_lufs=args.target_lufs,
        max_peak_dbfs=args.max_peak_dbfs,
        block_size=args.block_size,
        resample=ResampleConfig(
            zero_crossings=args.zero_crossings,
            rolloff=args.rolloff,
        ),
    )


def run(argv: list[str] | None = None) -> int:
    parser = build_parser()
    try:
        args = parser.parse_args(argv)
    except SystemExit as exc:  # argparse already printed a usage message
        return EXIT_OK if exc.code == 0 else EXIT_USAGE
    try:
        config = config_from_args(args)
        outcome = run_batch(
            args.input,
            args.output,
            config,
            jobs=args.jobs,
            max_bytes=args.max_file_mb * 1024 * 1024,
        )
        if not args.quiet:
            print(render_terminal(outcome))
        if args.report is not None:
            write_report(args.report, outcome, config)
            if not args.quiet:
                print(f"report: {args.report}")
        if args.report_dir is not None:
            from .report import write_file_reports

            paths = write_file_reports(args.report_dir, outcome)
            if not args.quiet:
                print(f"per-file reports: {len(paths)} in {args.report_dir}")
    except AudioPipeError as exc:
        print(f"error: {exc.message} [{exc.reason}]", file=sys.stderr)
        return exc.exit_code
    except KeyboardInterrupt:
        print("interrupted", file=sys.stderr)
        return EXIT_FILE_FAILED
    return outcome.exit_code


def main() -> None:
    sys.exit(run())


if __name__ == "__main__":
    main()
