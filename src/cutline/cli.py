"""Terminal entry point for cutline.

Commands
--------
Commands and their handlers are added incrementally; this stage provides the
argument surface, path guards and exit-code contract.

Exit codes
----------
``0`` success
``1`` internal error
``2`` argument or data error (bad path, malformed file, illegal value)
``3`` unresolved conflicts remain, or duration deviation exceeds tolerance
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any, Sequence

from . import __version__
from .exchange import ExchangeError
from .model import ModelError
from .rational import TimeError

EXIT_OK = 0
EXIT_INTERNAL = 1
EXIT_USAGE = 2
EXIT_CONFLICT = 3


class CLIError(Exception):
    """A user-facing failure carrying its exit status."""

    def __init__(self, message: str, exit_code: int = EXIT_USAGE, details: Any = None):
        self.exit_code = exit_code
        self.details = details
        super().__init__(message)


# ---------------------------------------------------------------------------
# argument parsing
# ---------------------------------------------------------------------------


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="cutline",
        description="Deterministic timeline assembly, conflict resolution and render planning.",
    )
    parser.add_argument("--version", action="version", version=f"cutline {__version__}")
    sub = parser.add_subparsers(dest="command", required=True)

    def add_common(p: argparse.ArgumentParser, *, with_format: bool = False) -> None:
        p.add_argument(
            "--unknown-fields",
            choices=("error", "warn", "ignore"),
            default="error",
            help="policy for unknown exchange fields (default: error)",
        )
        p.add_argument(
            "--tolerance",
            type=int,
            default=0,
            metavar="FRAMES",
            help="allowed absolute duration drift in frames (default: 0)",
        )
        p.add_argument(
            "--plan-rate",
            default=None,
            metavar="RATE",
            help="frame rate for the render plan; accepts '24000/1001' (default: first track rate)",
        )
        p.add_argument(
            "--resolve",
            choices=("none", "priority", "shift", "reject"),
            default="none",
            help="conflict resolution strategy (default: none)",
        )
        if with_format:
            p.add_argument("--format", choices=("json", "text"), default="json")

    analyze = sub.add_parser("analyze", help="detect/resolve conflicts and summarise")
    analyze.add_argument("inputs", nargs="+", metavar="FILE", help="exchange file(s) to analyse")
    add_common(analyze, with_format=True)
    analyze.add_argument("--exchange-out", metavar="PATH", help="write the resolved timeline here")
    analyze.add_argument(
        "--exchange-version",
        choices=("v1", "v2"),
        default="v1",
        help="version used by --exchange-out (default: v1)",
    )
    analyze.add_argument("--plan-out", metavar="PATH", help="write the render plan JSON here")
    analyze.add_argument("--otio-out", metavar="PATH", help="write the resolved timeline as .otio")

    convert = sub.add_parser("convert", help="rewrite an exchange file in another version")
    convert.add_argument("input", metavar="FILE")
    convert.add_argument("--out", required=True, metavar="PATH")
    convert.add_argument("--version", choices=("v1", "v2"), default="v2")
    add_common(convert)

    otio_export = sub.add_parser("otio-export", help="write an OTIO timeline from an exchange file")
    otio_export.add_argument("input", metavar="FILE")
    otio_export.add_argument("--out", required=True, metavar="PATH")
    add_common(otio_export)

    otio_import = sub.add_parser("otio-import", help="read an OTIO timeline into the exchange format")
    otio_import.add_argument("input", metavar="FILE")
    otio_import.add_argument("--out", required=True, metavar="PATH")
    otio_import.add_argument("--version", choices=("v1", "v2"), default="v2")

    return parser


# ---------------------------------------------------------------------------
# guards
# ---------------------------------------------------------------------------


def _ensure_output_paths(inputs: Sequence[Path], outputs: Sequence[Path | None]) -> None:
    protected = {path.resolve() for path in inputs}
    seen: set[Path] = set()
    for output in outputs:
        if output is None:
            continue
        resolved = output.resolve()
        if resolved in protected:
            raise CLIError(f"refusing to overwrite input file: {output}", EXIT_USAGE)
        if resolved in seen:
            raise CLIError(f"duplicate output path: {output}", EXIT_USAGE)
        seen.add(resolved)


def _ensure_unique_inputs(inputs: Sequence[Path]) -> None:
    seen: set[Path] = set()
    for path in inputs:
        resolved = path.resolve()
        if resolved in seen:
            raise CLIError(f"duplicate input path: {path}", EXIT_USAGE)
        seen.add(resolved)


# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------


def main(argv: Sequence[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        raise CLIError(f"{args.command}: not implemented yet", EXIT_INTERNAL)
    except CLIError as exc:
        print(f"cutline: error: {exc}", file=sys.stderr)
        if exc.details:
            print(json.dumps(exc.details, indent=2), file=sys.stderr)
        return exc.exit_code
    except (ExchangeError, ModelError, TimeError) as exc:
        print(f"cutline: error: {exc}", file=sys.stderr)
        return EXIT_USAGE
    except BrokenPipeError:  # pragma: no cover
        return EXIT_OK
    parser.error(f"unknown command {args.command!r}")
    return EXIT_USAGE


if __name__ == "__main__":  # pragma: no cover
    sys.exit(main())
