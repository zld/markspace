"""Command line entry point for mediacheck.

Exit codes:
  0  success, no hard inconsistencies
  1  hard inconsistencies found
  2  usage, argument or policy error
  3  the batch had read or write failures
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

from . import __version__
from .compare import Comparator, PolicyError, load_policy
from .extract import ExtractError, discover_media, summarize_file
from .pack import PackError, build_manifest, load_manifest, write_manifest
from .report import (ReportError, build_scan_summary, render_compare,
                     render_scan, write_json)
from .store import MediaStore, StoreError, batch_manifest

EXIT_OK = 0
EXIT_INCONSISTENT = 1
EXIT_USAGE = 2
EXIT_BATCH_FAILED = 3


def _err(message: str) -> None:
    print(message, file=sys.stderr)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="mediacheck",
        description="Inspect local media containers and compare delivery packages.")
    parser.add_argument("--version", action="version",
                        version=f"mediacheck {__version__}")
    sub = parser.add_subparsers(dest="command", required=True)

    scan = sub.add_parser("scan", help="inspect a file or directory tree")
    scan.add_argument("target", type=Path, help="file or directory to scan")
    scan.add_argument("--db", type=Path, help="sqlite database to persist into")
    scan.add_argument("--json", type=Path, dest="json_out",
                      help="write the full scan result as JSON")
    scan.add_argument("--no-recursive", action="store_true",
                      help="only scan the top-level directory")
    scan.add_argument("--av-tolerance", type=float, metavar="SECONDS",
                      default=0.5,
                      help="audio/video duration drift allowed within one file "
                           "(default 0.5)")
    scan.set_defaults(func=cmd_scan)

    pack = sub.add_parser("pack", help="build a delivery manifest")
    pack.add_argument("target", type=Path, help="file or directory to package")
    pack.add_argument("--out", type=Path, required=True,
                      help="manifest JSON output path")
    pack.add_argument("--label", help="label recorded in the manifest")
    pack.add_argument("--no-recursive", action="store_true",
                      help="only package the top-level directory")
    pack.set_defaults(func=cmd_pack)

    compare = sub.add_parser("compare", help="compare reference and target packages")
    compare.add_argument("reference", help="reference manifest or its directory")
    compare.add_argument("target", help="target manifest or its directory")
    compare.add_argument("--policy", type=Path, help="JSON policy file")
    compare.add_argument("--duration-tolerance", type=float, metavar="SECONDS",
                         help="absolute duration tolerance override")
    compare.add_argument("--ref-db", type=Path,
                         help="use the latest batch in this database as reference")
    compare.add_argument("--target-db", type=Path,
                         help="use the latest batch in this database as target")
    compare.add_argument("--json", type=Path, dest="json_out",
                         help="write the comparison result as JSON")
    compare.set_defaults(func=cmd_compare)

    report = sub.add_parser("report", help="print a stored scan batch")
    report.add_argument("--db", type=Path, required=True,
                        help="sqlite database produced by scan")
    report.add_argument("--batch", type=int,
                        help="batch id (default: latest complete batch)")
    report.add_argument("--av-tolerance", type=float, metavar="SECONDS",
                        default=0.5,
                        help="audio/video duration drift allowed within one file")
    report.add_argument("--json", type=Path, dest="json_out",
                        help="write the report as JSON")
    report.set_defaults(func=cmd_report)
    return parser


# ---------------------------------------------------------------------------
# scan
# ---------------------------------------------------------------------------

def collect_media(target: Path, *, recursive: bool,
                  failures: list[dict] | None = None) -> list[Path]:
    return discover_media(target, recursive=recursive, failures=failures)


def cmd_scan(args: argparse.Namespace) -> int:
    if args.av_tolerance < 0:
        _err("scan: --av-tolerance must be >= 0")
        return EXIT_USAGE
    summaries: list[dict] = []
    failures: list[dict] = []
    try:
        files = collect_media(args.target, recursive=not args.no_recursive,
                              failures=failures)
    except ExtractError as exc:
        _err(f"scan: {exc.message}")
        return EXIT_USAGE
    except OSError as exc:
        _err(f"scan: cannot list {args.target}: {exc}")
        return EXIT_USAGE
    for failure in failures:
        _err(f"scan: cannot list {failure['path']}: {failure['message']}")

    for path in files:
        try:
            summaries.append(summarize_file(path))
        except ExtractError as exc:
            failures.append(exc.to_dict())
            _err(f"scan: failed {exc.path}: {exc.code}: {exc.message}")

    batch_info = None
    if args.db is not None:
        try:
            with MediaStore(args.db) as store:
                batch_id = store.begin_batch(args.target)
                stats = store.commit_batch(batch_id, summaries=summaries,
                                           failures=failures,
                                           root=args.target)
                batch_info = {"id": batch_id, **stats,
                              "db": str(args.db)}
        except StoreError as exc:
            _err(f"scan: {exc.message}")
            summary = build_scan_summary(str(args.target), summaries, failures)
            print(f"files_scanned: {summary['files_scanned']} "
                  f"files_ok: {summary['files_ok']} "
                  f"files_failed: {summary['files_failed']}")
            for failure in failures:
                _err(f"  failed: {failure['path']}: {failure['message']}")
            return EXIT_BATCH_FAILED

    summary = build_scan_summary(str(args.target), summaries, failures,
                                 batch=batch_info,
                                 av_tolerance_seconds=args.av_tolerance)
    render_scan(summary, sys.stdout)
    if args.json_out is not None:
        try:
            write_json(summary, args.json_out)
        except ReportError as exc:
            _err(f"scan: {exc.message}")
            return EXIT_BATCH_FAILED
    if failures:
        return EXIT_BATCH_FAILED
    return EXIT_INCONSISTENT if summary["hard_inconsistency_count"] else EXIT_OK


# ---------------------------------------------------------------------------
# pack
# ---------------------------------------------------------------------------

def cmd_pack(args: argparse.Namespace) -> int:
    try:
        manifest = build_manifest(args.target,
                                  recursive=not args.no_recursive,
                                  label=args.label)
        write_manifest(manifest, args.out)
    except PackError as exc:
        _err(f"pack: {exc.message}")
        return EXIT_BATCH_FAILED if exc.path else EXIT_USAGE
    except OSError as exc:
        _err(f"pack: {exc}")
        return EXIT_BATCH_FAILED
    print(f"packed: {manifest['files_ok']} file(s), "
          f"{manifest['tracks_total']} track(s), "
          f"{manifest['files_failed']} failure(s) -> {args.out}")
    for failure in manifest["failures"]:
        _err(f"pack: failed {failure['path']}: {failure['code']}: "
             f"{failure['message']}")
    return EXIT_BATCH_FAILED if manifest["failures"] else EXIT_OK


# ---------------------------------------------------------------------------
# compare / report
# ---------------------------------------------------------------------------

def _load_side(spec: str, db: Path | None) -> dict:
    if db is not None:
        with MediaStore(db) as store:
            return batch_manifest(store)
    path = Path(spec)
    if path.is_dir():
        return build_manifest(path)
    return load_manifest(path)


def cmd_compare(args: argparse.Namespace) -> int:
    if args.duration_tolerance is not None and args.duration_tolerance < 0:
        _err("compare: --duration-tolerance must be >= 0")
        return EXIT_USAGE
    try:
        policy = load_policy(args.policy,
                             tolerance_seconds=args.duration_tolerance)
    except PolicyError as exc:
        location = ""
        if exc.line is not None:
            location = f" (line {exc.line} column {exc.column})"
        elif exc.field_path:
            location = f" (field {exc.field_path})"
        _err(f"compare: policy error: {exc.message}{location}")
        return EXIT_USAGE

    try:
        reference = _load_side(args.reference, args.ref_db)
        target = _load_side(args.target, args.target_db)
    except (PackError, StoreError, PolicyError) as exc:
        _err(f"compare: {exc.message}")
        return EXIT_USAGE
    except OSError as exc:
        _err(f"compare: {exc}")
        return EXIT_BATCH_FAILED

    try:
        result = Comparator(policy).compare(reference, target)
    except PolicyError as exc:
        _err(f"compare: {exc.message}")
        return EXIT_USAGE

    render_compare(result, sys.stdout)
    if args.json_out is not None:
        try:
            write_json(result, args.json_out)
        except ReportError as exc:
            _err(f"compare: {exc.message}")
            return EXIT_BATCH_FAILED
    if result["hard_inconsistency_count"]:
        return EXIT_INCONSISTENT
    return EXIT_OK


def cmd_report(args: argparse.Namespace) -> int:
    try:
        with MediaStore(args.db) as store:
            document = batch_manifest(store, args.batch)
    except StoreError as exc:
        _err(f"report: {exc.message}")
        return EXIT_USAGE
    summaries = document["media"]
    batch = document["batch"]
    summary = build_scan_summary(document["root"], summaries,
                                 document["failures"], batch=batch,
                                 av_tolerance_seconds=args.av_tolerance)
    summary["files_scanned"] = document["files_scanned"]
    render_scan(summary, sys.stdout)
    if args.json_out is not None:
        try:
            write_json(summary, args.json_out)
        except ReportError as exc:
            _err(f"report: {exc.message}")
            return EXIT_BATCH_FAILED
    if document["failures"]:
        return EXIT_BATCH_FAILED
    return EXIT_INCONSISTENT if summary["hard_inconsistency_count"] else EXIT_OK


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    try:
        args = parser.parse_args(argv)
    except SystemExit as exc:  # argparse already printed the usage message
        return int(exc.code or 0)
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
