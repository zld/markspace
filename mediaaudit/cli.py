"""Command line interface: argument parsing and subcommand dispatch."""
from __future__ import annotations

import argparse

EXIT_OK = 0
EXIT_MISMATCH = 1
EXIT_FATAL = 2


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="mediaaudit",
        description="Verify capture media sets against a show manifest.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    audit = sub.add_parser("audit", help="extract metadata and compare against the manifest")
    audit.add_argument("--manifest", required=True, help="path to the show manifest JSON")
    audit.add_argument("--db", required=True, help="SQLite database path")
    audit.add_argument("--json", dest="json_out", help="write the JSON report here")
    audit.add_argument("--workers", type=int, default=1, help="parallel extraction workers")
    audit.add_argument("--max-bytes", type=int, default=None, help="per-file size limit")

    pack = sub.add_parser("pack", help="verify inputs, then build a capture archive")
    pack.add_argument("--manifest", required=True, help="path to the show manifest JSON")
    pack.add_argument("--db", required=True, help="SQLite database path")
    pack.add_argument("--out", required=True, help="output ZIP archive path")
    pack.add_argument("--workers", type=int, default=1, help="parallel extraction workers")
    pack.add_argument("--max-bytes", type=int, default=None, help="per-file size limit")
    return parser


def main(argv=None) -> int:
    parser = build_parser()
    parser.parse_args(argv)  # dispatch wired in a later batch
    print("error: subcommand not implemented yet")
    return EXIT_FATAL
