"""Command line interface for audioprep.

Subcommands:

* ``fixtures`` -- generate deterministic WAV samples (see
  :mod:`audioprep.fixtures`)
* ``batch``    -- recursively convert a directory tree (see
  :mod:`audioprep.pipeline`)
* ``verify``   -- re-read converted files and recompute report statistics
  (see :mod:`audioprep.report`)

Exit codes: 0 success, 1 one or more files failed, 2 usage/configuration
error, 3 verify detected a report mismatch.
"""

from __future__ import annotations

import argparse
from pathlib import Path
from typing import Sequence

EXIT_OK = 0
EXIT_FILE_FAILURE = 1
EXIT_USAGE = 2
EXIT_VERIFY_MISMATCH = 3


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="audioprep",
        description="Local, reproducible batch audio preparation.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    p_fix = sub.add_parser("fixtures", help="generate deterministic WAV samples")
    p_fix.add_argument("--out", type=Path, required=True, help="output directory")

    p_batch = sub.add_parser("batch", help="batch convert an input directory")
    p_batch.add_argument("--input", dest="input_dir", type=Path, required=True)
    p_batch.add_argument("--output", dest="output_dir", type=Path, required=True)
    p_batch.add_argument("--report", type=Path, required=True)
    p_batch.add_argument("--sample-rate", type=int, required=True)
    p_batch.add_argument(
        "--channels", choices=("mono", "stereo", "keep"), required=True
    )
    p_batch.add_argument("--target-lufs", type=float, required=True)
    p_batch.add_argument("--peak-ceiling", type=float, required=True,
                         help="peak ceiling in dBFS (e.g. -1)")
    p_batch.add_argument("--block-frames", type=int, required=True)
    p_batch.add_argument("--workers", type=int, default=1)

    p_verify = sub.add_parser("verify", help="recompute stats and compare report")
    p_verify.add_argument("--report", type=Path, required=True)
    p_verify.add_argument("--output", dest="output_dir", type=Path, required=True)

    return parser


def main(argv: Sequence[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    # Heavy modules are imported lazily so argument errors stay cheap.
    if args.command == "fixtures":
        from audioprep.fixtures import generate_fixtures

        generate_fixtures(args.out)
        return EXIT_OK
    if args.command == "batch":
        from audioprep.pipeline import run_batch

        return run_batch(vars(args))
    if args.command == "verify":
        from audioprep.report import verify_report

        return verify_report(args.report, args.output_dir)
    parser.error(f"unknown command {args.command!r}")
    return EXIT_USAGE
