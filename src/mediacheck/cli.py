"""Command-line interface.

Comparison model
----------------
A compare run establishes what each file *should* look like from, in order:

1. an explicit ``--baseline`` directory;
2. the ``baseline.json`` manifest next to a scanned file (produced by the
   sample generator and by ``snapshot``);
3. the most recent snapshot in the SQLite database for the same content
   digest.

The first scan of a file therefore succeeds with zero mismatches, while a
later run against a changed file is diffed against what was recorded.

Exit codes
----------
0  scan finished and everything is consistent
1  inconsistencies or corrupt inputs were found
2  command-line argument error
3  local storage or output failure
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path
from typing import Sequence

from . import __version__
from .commands import check_args, cmd_compare, cmd_pack, cmd_scan, cmd_snapshot
from .inputs import UsageError
from .store import StoreError
from .util import OutputError

EXIT_OK = 0
EXIT_MISMATCH = 1
EXIT_USAGE = 2
EXIT_STORAGE = 3

def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="mediacheck",
        description="Offline container metadata verifier (WAV/MP4).",
    )
    parser.add_argument(
        "--version", action="version", version=f"mediacheck {__version__}"
    )
    parser.add_argument(
        "--db",
        default="state.sqlite",
        help="SQLite snapshot database (default: state.sqlite)",
    )
    parser.add_argument(
        "--json",
        dest="json_path",
        help="write the full report as UTF-8 JSON to this path",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    compare = sub.add_parser(
        "compare", help="sniff media, compare against baselines, persist results"
    )
    compare.add_argument("paths", nargs="+", help="files or directories to scan")
    compare.add_argument(
        "--baseline",
        help="directory of baseline media; defaults to baseline.json or snapshots",
    )
    compare.add_argument(
        "--duration-tolerance",
        type=float,
        default=0.0,
        metavar="SECONDS",
        help="allowed absolute track duration deviation in seconds",
    )
    compare.add_argument(
        "--json",
        dest="json_path",
        help="write the full report as UTF-8 JSON to this path",
    )
    compare.add_argument(
        "--db",
        dest="db",
        default=argparse.SUPPRESS,
        help="SQLite snapshot database (default: state.sqlite)",
    )

    scan = sub.add_parser(
        "scan", help="sniff media and record snapshots without comparing"
    )
    scan.add_argument("paths", nargs="+", help="files or directories to scan")
    scan.add_argument(
        "--json", dest="json_path", help="write the scan report as JSON"
    )
    scan.add_argument(
        "--db",
        dest="db",
        default=argparse.SUPPRESS,
        help="SQLite snapshot database (default: state.sqlite)",
    )

    snapshot = sub.add_parser(
        "snapshot",
        help="write a baseline.json manifest from the current media state",
    )
    snapshot.add_argument("path", help="directory whose media become the baseline")
    snapshot.add_argument(
        "--out", help="baseline manifest path (default: <path>/baseline.json)"
    )

    pack = sub.add_parser(
        "pack", help="build a pack manifest from verified media"
    )
    pack.add_argument("paths", nargs="+", help="files or directories to pack")
    pack.add_argument("--out", required=True, help="manifest output path")
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        check_args(args)
        if args.command == "compare":
            return cmd_compare(args)
        if args.command == "scan":
            return cmd_scan(args)
        if args.command == "snapshot":
            return cmd_snapshot(args)
        if args.command == "pack":
            return cmd_pack(args)
        raise UsageError(f"unknown command: {args.command}")
    except UsageError as exc:
        print(f"argument error: {exc}", file=sys.stderr)
        return EXIT_USAGE
    except (StoreError, OutputError) as exc:
        print(f"storage error: {exc}", file=sys.stderr)
        return EXIT_STORAGE
    except ValueError as exc:
        print(f"storage error: {exc}", file=sys.stderr)
        return EXIT_STORAGE


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
