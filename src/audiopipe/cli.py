"""Command-line entry point and exit-code mapping.

Commands
--------
``normalize``
    Batch-process every WAV under ``--input`` into ``--output`` and write
    a JSON report.
``verify``
    Re-read the produced WAVs and compare them against a report.

Exit codes
----------
* ``0`` -- everything succeeded;
* ``1`` -- at least one file failed, but the report was written;
* ``2`` -- invalid arguments or batch-level configuration error;
* ``3`` -- the report could not be written.
"""

from __future__ import annotations

import argparse
import math
import os
import sys

from . import __version__
from .batch import (
    BatchConfig,
    ConfigError,
    format_summary,
    run_batch,
    scan_inputs,
    verify_report,
    VerificationError,
    _validate_batch_config,
)
from .report import ReportWriteError, write_json

EXIT_OK = 0
EXIT_FILE_FAILURES = 1
EXIT_CONFIG = 2
EXIT_REPORT = 3


def _finite_float(text: str) -> float:
    try:
        value = float(text)
    except ValueError as exc:
        raise argparse.ArgumentTypeError(f"not a number: {text!r}") from exc
    if not math.isfinite(value):
        raise argparse.ArgumentTypeError(f"must be finite: {text!r}")
    return value


def _sample_rate(text: str) -> int:
    try:
        value = int(text)
    except ValueError as exc:
        raise argparse.ArgumentTypeError(f"not an integer: {text!r}") from exc
    if not 8000 <= value <= 384000:
        raise argparse.ArgumentTypeError("sample rate must be between 8000 and 384000")
    return value


def _lufs(text: str) -> float:
    value = _finite_float(text)
    if not -70.0 <= value <= 0.0:
        raise argparse.ArgumentTypeError("must be between -70 and 0 LUFS")
    return value


def _positive_int(text: str) -> int:
    try:
        value = int(text)
    except ValueError as exc:
        raise argparse.ArgumentTypeError(f"not an integer: {text!r}") from exc
    if value < 1:
        raise argparse.ArgumentTypeError("must be >= 1")
    return value


def _block_frames(text: str) -> int:
    value = _positive_int(text)
    if value > 1_048_576:
        raise argparse.ArgumentTypeError("must be <= 1048576")
    return value


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="audiopipe",
        description="Offline WAV resampling and loudness normalisation pipeline.",
    )
    parser.add_argument("--version", action="version", version=f"audiopipe {__version__}")
    sub = parser.add_subparsers(dest="command", required=True)

    norm = sub.add_parser(
        "normalize",
        help="resample and loudness-normalise a directory tree of WAV files",
    )
    norm.add_argument("--input", required=True, help="input directory (scanned recursively)")
    norm.add_argument("--output", required=True, help="output directory (mirrors the input tree)")
    norm.add_argument("--report", required=True, help="path of the JSON report to write")
    norm.add_argument("--rate", required=True, type=_sample_rate, help="output sample rate in Hz")
    norm.add_argument(
        "--target-lufs", type=_lufs, default=-18.0, help="target LUFS, -70..0 (default: -18)"
    )
    norm.add_argument(
        "--max-gain-db",
        type=_finite_float,
        default=12.0,
        help="maximum gain applied in dB (default: 12)",
    )
    norm.add_argument(
        "--max-peak-db",
        type=_finite_float,
        default=-0.1,
        help="peak ceiling in dBFS used when planning gain (default: -0.1)",
    )
    norm.add_argument(
        "--format",
        dest="output_format",
        choices=("PCM_16", "PCM_24"),
        default="PCM_24",
        help="output PCM encoding (default: PCM_24)",
    )
    norm.add_argument(
        "--jobs", type=_positive_int, default=1, help="parallel worker processes (default: 1)"
    )
    norm.add_argument(
        "--block-frames",
        type=_block_frames,
        default=65536,
        help="frames per streaming block (default: 65536)",
    )

    verify = sub.add_parser(
        "verify",
        help="re-read produced WAVs and check a report's measurements",
    )
    verify.add_argument("--report", required=True, help="path of the JSON report to verify")
    return parser


def _run_normalize(args: argparse.Namespace) -> int:
    config = BatchConfig(
        input_dir=args.input,
        output_dir=args.output,
        report_path=args.report,
        output_rate=args.rate,
        output_subtype=args.output_format,
        target_lufs=args.target_lufs,
        max_gain_db=args.max_gain_db,
        max_peak_db=args.max_peak_db,
        jobs=args.jobs,
        block_frames=args.block_frames,
    )
    try:
        if not os.path.isdir(config.input_dir):
            raise ConfigError(f"input directory does not exist: {config.input_dir}")
        _validate_config(config)
        file_count = len(scan_inputs(config.input_dir))
        if file_count == 0:
            raise ConfigError(f"no WAV files found under {config.input_dir}")
        report = run_batch(config)
    except ConfigError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_CONFIG
    except ReportWriteError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_REPORT
    except ValueError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_CONFIG

    try:
        write_json(config.report_path, report)
    except ReportWriteError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_REPORT

    print(format_summary(report))
    for record in report["files"]:
        if record["status"] != "ok":
            error = record.get("error") or {}
            print(
                f"failed: {record['relative_path']}: {error.get('message', 'unknown error')}",
                file=sys.stderr,
            )
        else:
            print(_record_line(record))
    print(f"report: {config.report_path}")
    return EXIT_OK if report["summary"]["failed"] == 0 else EXIT_FILE_FAILURES


def _record_line(record: dict) -> str:
    """One-line summary of a successful file, for visual cross-checking."""
    after = record["after"]
    gain = record["gain"]
    lufs = after["lufs"]
    lufs_text = "n/a" if lufs is None else f"{lufs:.2f}"
    return (
        f"  {record['relative_path']}: rate={record['output']['sample_rate']} "
        f"channels={record['output']['channels']} "
        f"frames={record['output']['frames']} "
        f"padding={record['padding_frames']} "
        f"rms={after['rms']:.4f} lufs={lufs_text} "
        f"gain_db={gain['applied_gain_db']:.2f} ({gain['reason']})"
    )


def _validate_config(config: BatchConfig) -> None:
    """Run the batch-level checks up front so exit code 2 wins over 3."""
    _validate_batch_config(config)


def _run_verify(args: argparse.Namespace) -> int:
    try:
        document = verify_report(args.report)
    except VerificationError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_CONFIG
    summary = document["summary"]
    for record in document["files"]:
        if record["status"] == "mismatch":
            for check in record["checks"]:
                if not check["ok"]:
                    print(
                        f"mismatch: {record['relative_path']} {check['name']}: "
                        f"reported={check['reported']!r} recomputed={check['recomputed']!r}",
                        file=sys.stderr,
                    )
            if "error" in record:
                print(
                    f"mismatch: {record['relative_path']}: {record['error']['message']}",
                    file=sys.stderr,
                )
    print(
        f"verified={summary['checked']} mismatched={summary['mismatched']} "
        f"skipped={summary['skipped']}"
    )
    return EXIT_OK if summary["mismatched"] == 0 else EXIT_FILE_FAILURES


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    if args.command == "normalize":
        return _run_normalize(args)
    if args.command == "verify":
        return _run_verify(args)
    parser.error(f"unknown command {args.command!r}")
    return EXIT_CONFIG


if __name__ == "__main__":
    sys.exit(main())
