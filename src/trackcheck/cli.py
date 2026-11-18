"""Command line interface: scan, compare and package subcommands.

Exit codes:
  0  success, no mismatches
  1  consistency mismatches detected
  2  invalid arguments or unreadable/malformed input
  3  storage or report write failure
"""
from __future__ import annotations

import argparse
import sys
from pathlib import Path

from .consistency import compare_media
from .model import Media
from .package import PackageError, PackageOutputError, package_media
from .probe import FileAccessError, probe_file
from .report import (
    ReportWriteError,
    build_report,
    render_summary,
    write_json_report,
)
from .store import SnapshotStore, StoreError

EXIT_OK = 0
EXIT_MISMATCH = 1
EXIT_INPUT = 2
EXIT_STORAGE = 3


def _probe_cached(store: SnapshotStore | None, path: Path) -> Media:
    """Probe directly, or reuse a cached snapshot when the hash matches."""
    media = probe_file(path)
    if store is None:
        return media
    cached = store.get(path, media.content_hash)
    if cached is not None:
        return cached
    stat = path.stat()
    store.put(media, stat.st_mtime_ns, stat.st_size)
    return media


def _open_store(args: argparse.Namespace) -> SnapshotStore | None:
    if not args.db:
        return None
    return SnapshotStore(args.db, rebuild=getattr(args, "rebuild", False))


def _cmd_scan(args: argparse.Namespace) -> int:
    try:
        store = _open_store(args)
    except StoreError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_STORAGE
    failures = 0
    for raw_path in args.inputs:
        path = Path(raw_path)
        try:
            media = _probe_cached(store, path)
        except FileAccessError as exc:
            print(f"error: {exc}", file=sys.stderr)
            failures += 1
            continue
        status = "ok" if media.valid else "invalid"
        print(
            f"{path} status={status} tracks={media.track_count} "
            f"duration_ms={round(media.duration_ms, 3):g} "
            f"errors={len(media.errors)} hash={media.content_hash[:12]}"
        )
        for error in media.errors:
            loc = f"@offset {error.offset}" if error.offset is not None else ""
            print(f"  - [{error.code}] {error.message} {loc}".rstrip())
        if not media.valid:
            failures += 1
    if store is not None:
        store.close()
    return EXIT_INPUT if failures else EXIT_OK


def _cmd_compare(args: argparse.Namespace) -> int:
    try:
        store = _open_store(args)
        try:
            baseline = _probe_cached(store, Path(args.baseline))
            candidate = _probe_cached(store, Path(args.input))
        finally:
            if store is not None:
                store.close()
    except FileAccessError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_INPUT
    except StoreError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_STORAGE

    result = compare_media(baseline, candidate, args.tolerance_ms)
    print(render_summary(result))

    if args.json:
        payload = build_report(result, baseline, candidate)
        try:
            write_json_report(Path(args.json), payload)
        except ReportWriteError as exc:
            print(f"error: {exc}", file=sys.stderr)
            return EXIT_STORAGE

    if result.errors:
        return EXIT_INPUT
    return EXIT_MISMATCH if result.mismatches else EXIT_OK


def _cmd_package(args: argparse.Namespace) -> int:
    try:
        media = probe_file(Path(args.input))
        manifest = package_media(media, args.output)
    except FileAccessError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_INPUT
    except PackageError as exc:
        for error in exc.errors:
            loc = f"@offset {error.offset}" if error.offset is not None else ""
            print(f"error: [{error.code}] {error.message} {loc}".rstrip(),
                  file=sys.stderr)
        return EXIT_INPUT
    except PackageOutputError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_STORAGE
    print(
        f"packaged {manifest.source_path} -> {manifest.output_path} "
        f"tracks={manifest.track_count} hash={manifest.content_hash[:12]}"
    )
    return EXIT_OK


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="trackcheck",
        description="Local media container metadata and consistency checker",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    scan = sub.add_parser("scan", help="probe one or more containers")
    scan.add_argument("inputs", nargs="+")
    scan.add_argument("--db")
    scan.add_argument("--rebuild", action="store_true")
    scan.set_defaults(func=_cmd_scan)

    compare = sub.add_parser("compare", help="compare baseline against input")
    compare.add_argument("--baseline", required=True)
    compare.add_argument("--input", required=True)
    compare.add_argument("--db")
    compare.add_argument("--rebuild", action="store_true")
    compare.add_argument("--json")
    compare.add_argument("--tolerance-ms", type=float, default=0.0)
    compare.set_defaults(func=_cmd_compare)

    package = sub.add_parser("package", help="validate inputs and write manifest")
    package.add_argument("--input", required=True)
    package.add_argument("--output", required=True)
    package.set_defaults(func=_cmd_package)
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        return args.func(args)
    except BrokenPipeError:  # piping into head, etc.
        return EXIT_OK


if __name__ == "__main__":
    raise SystemExit(main())
