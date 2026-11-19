"""Terminal entry point for cutline.

Commands
--------
``analyze``
    Load one or more exchange files, detect (and optionally resolve)
    conflicts, and print a JSON/text summary.  A single input prints a flat
    summary object; multiple inputs print ``{"results": [...]}`` in input
    order, and a per-file failure never stops the run.
convert and the OTIO commands follow in the next commit.

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
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Sequence

from . import __version__
from .conflicts import detect
from .exchange import ExchangeError, LoadedExchange, load_exchange
from .model import ModelError, Timeline
from .rational import TimeError
from .render import RenderPlan, build_plan
from .report import AnalyzeSummary, build_summary
from .resolve import Strategy, resolve

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
# path guards
# ---------------------------------------------------------------------------


def _ensure_unique_inputs(inputs: Sequence[Path]) -> None:
    seen: set[Path] = set()
    for path in inputs:
        resolved = path.resolve()
        if resolved in seen:
            raise CLIError(f"duplicate input path: {path}", EXIT_USAGE)
        seen.add(resolved)


# ---------------------------------------------------------------------------
# core analysis
# ---------------------------------------------------------------------------


@dataclass
class AnalyzeOutcome:
    resolved: Timeline
    summary: AnalyzeSummary
    plan: RenderPlan


def analyze_loaded(
    loaded: LoadedExchange,
    *,
    strategy: str,
    tolerance: int,
    plan_rate: Any = None,
) -> AnalyzeOutcome:
    """Resolve, plan and summarise one loaded timeline."""
    if strategy == "none":
        resolved = loaded.timeline
        decisions: list[dict[str, Any]] = []
        # Nothing was resolved, so every remaining conflict is unresolved and
        # the run must report failure (exit 3) rather than pretend success.
        unresolved = list(detect(resolved).conflicts)
        removed: list[str] = []
        warnings: list[str] = []
    else:
        result = resolve(loaded.timeline, Strategy(strategy))
        resolved = result.timeline
        decisions = [decision.as_dict() for decision in result.decisions]
        unresolved = list(result.unresolved)
        removed = [decision.clip_id for decision in result.removed]
        warnings = []
        if strategy == "reject" and unresolved:
            warnings.append(f"reject strategy left {len(unresolved)} conflict(s) unresolved")
        elif unresolved:
            warnings.append(f"{len(unresolved)} conflict(s) could not be resolved by {strategy}")

    plan = build_plan(resolved, plan_rate)
    summary = build_summary(
        loaded.timeline,
        resolved,
        duration_after_frames=plan.duration_frames,
        exchange_version=loaded.version,
        strategy=strategy,
        tolerance_frames=tolerance,
        rate=plan.plan_rate,
        decisions=decisions,
        unresolved=unresolved,
        removed_clips=removed,
        warnings=loaded.warnings + warnings,
    )
    return AnalyzeOutcome(resolved=resolved, summary=summary, plan=plan)


# ---------------------------------------------------------------------------
# commands
# ---------------------------------------------------------------------------


def _cmd_analyze(args: argparse.Namespace) -> int:
    inputs = [Path(item) for item in args.inputs]
    _ensure_unique_inputs(inputs)
    if args.tolerance < 0:
        raise CLIError("--tolerance must be zero or greater", EXIT_USAGE)

    results: list[dict[str, Any]] = []
    overall = EXIT_OK
    for path in inputs:
        try:
            loaded = load_exchange(path, unknown_fields=args.unknown_fields)
            outcome = analyze_loaded(
                loaded,
                strategy=args.resolve,
                tolerance=args.tolerance,
                plan_rate=args.plan_rate,
            )
            payload = outcome.summary.as_dict()
            payload["input"] = str(path)
            payload["plan"] = outcome.plan.as_dict()
            results.append(payload)
            overall = max(overall, outcome.summary.exit_code)
        except (ExchangeError, ModelError, TimeError, CLIError) as exc:
            results.append(
                {
                    "input": str(path),
                    "status": "error",
                    "exit_code": EXIT_USAGE,
                    "error": str(exc),
                }
            )
            overall = max(overall, EXIT_USAGE)

    payload: Any = results[0] if len(inputs) == 1 else {"results": results}
    print(json.dumps(payload, indent=2))
    return overall


# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------


def main(argv: Sequence[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        if args.command == "analyze":
            return _cmd_analyze(args)
        if args.command in {"convert", "otio-export", "otio-import"}:
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
