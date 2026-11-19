"""Command-line interface: argument parsing and scan orchestration.

Exit codes: 0 = no inconsistencies, 1 = inconsistencies found (in strict
mode also any file-level error), 2 = argument or runtime error.
"""

from __future__ import annotations

import argparse
import hashlib
import os
import sys

from . import MAX_FILE_BYTES, PARSER_VERSION
from .compare import DEFAULT_TOLERANCE_MS, check_file
from .probe import probe_file
from .report import build_report, codec_summary_of, print_summary, write_json
from .store import Store

_DEFAULT_EXTS = (".mov", ".mp4", ".m4v", ".wav", ".wave")


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="medcheck",
        description="Offline media container metadata extractor and "
                    "consistency checker (WAV, MP4/MOV).")
    sub = parser.add_subparsers(dest="command", required=True)
    scan = sub.add_parser("scan", help="scan files or directories")
    scan.add_argument("paths", nargs="+",
                      help="input files and/or directories (directories are "
                           "walked recursively, symlinks are not followed)")
    scan.add_argument("--db", help="sqlite3 database path for batch results")
    scan.add_argument("--json", dest="json_path",
                      help="write the JSON report to this path")
    scan.add_argument("--tolerance-ms", type=float,
                      default=DEFAULT_TOLERANCE_MS,
                      help="duration tolerance in milliseconds "
                           f"(default: {DEFAULT_TOLERANCE_MS:g})")
    scan.add_argument("--ext", action="append", default=None,
                      help="extension filter for directory walks, repeatable "
                           f"(default: {' '.join(_DEFAULT_EXTS)}); explicit "
                           "file paths are always probed regardless of "
                           "extension")
    scan.add_argument("--strict", action="store_true",
                      help="exit 1 when any file-level error occurred, in "
                           "addition to any inconsistency")
    return parser


def _collect_files(paths: list[str], exts: tuple[str, ...]) -> list[str]:
    """Expand paths into a deduplicated file list without following links."""
    seen: set[str] = set()
    files: list[str] = []

    def add(path: str) -> None:
        key = os.path.normcase(os.path.abspath(path))
        if key not in seen:
            seen.add(key)
            files.append(path)

    for raw in paths:
        if os.path.islink(raw):
            continue
        if os.path.isfile(raw):
            add(raw)  # explicit files are probed regardless of extension
        elif os.path.isdir(raw):
            for root, dirs, names in os.walk(raw, followlinks=False):
                dirs[:] = sorted(d for d in dirs
                                 if not os.path.islink(os.path.join(root, d)))
                for name in sorted(names):
                    full = os.path.join(root, name)
                    if os.path.islink(full) or not os.path.isfile(full):
                        continue
                    if exts and not name.lower().endswith(exts):
                        continue
                    add(full)
        else:
            raise FileNotFoundError(f"input path not found: {raw}")
    return files


def _sha256(path: str) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as fh:
        for block in iter(lambda: fh.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def _scan(args: argparse.Namespace) -> int:
    exts = tuple(e.lower() if e.startswith(".") else "." + e.lower()
                 for e in args.ext) if args.ext else _DEFAULT_EXTS
    try:
        files = _collect_files(args.paths, exts)
    except (FileNotFoundError, OSError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2

    store = Store(args.db, PARSER_VERSION) if args.db else None
    try:
        batch_id = store.begin_batch(args.tolerance_ms) if store else None
        results = []
        for path in files:
            size = os.path.getsize(path)
            record = None
            digest = None
            if size <= MAX_FILE_BYTES:
                try:
                    digest = _sha256(path)
                except OSError as exc:
                    record = {"path": path, "format": None,
                              "container_duration_ms": None, "tracks": [],
                              "errors": [f"cannot read file: {exc.strerror or exc}"]}
                if record is None and store is not None:
                    cached = store.cache_get(digest)
                    if cached is not None:
                        record = {**cached, "path": path}
                if record is None:
                    record = probe_file(path)
                    if store is not None and digest is not None:
                        store.cache_put(digest, record)
            else:
                record = {"path": path, "format": None,
                          "container_duration_ms": None, "tracks": [],
                          "errors": [f"file exceeds 2 GiB limit ({size} bytes)"]}
            mismatches, delta = check_file(record, args.tolerance_ms)
            results.append({"record": record, "mismatches": mismatches,
                            "duration_delta_ms": delta})
            if store is not None:
                store.record_file(batch_id, record, digest, size, mismatches,
                                  delta, codec_summary_of(record))
    finally:
        if store is not None:
            store.close()

    report = build_report(results)
    print_summary(report)
    if args.json_path:
        write_json(report, args.json_path)
    if report["mismatches"]:
        return 1
    if args.strict and report["errors"]:
        return 1
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = _build_parser()
    try:
        args = parser.parse_args(argv)
    except SystemExit as exc:  # argparse errors already printed usage
        return int(exc.code) if exc.code else 0
    if args.command == "scan":
        try:
            return _scan(args)
        except OSError as exc:
            print(f"error: {exc}", file=sys.stderr)
            return 2
    return 2


def entrypoint() -> None:
    sys.exit(main())


if __name__ == "__main__":
    entrypoint()
