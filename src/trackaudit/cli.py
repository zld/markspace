"""Command line interface: ``inspect`` and ``pack`` subcommands.

Exit codes: 0 = checks completed, no errors or mismatches;
1 = inconsistencies found; 2 = input or container damaged;
3 = argument or output failure.
"""
from __future__ import annotations

import argparse
import os
import sys
from pathlib import Path

from . import pack as pack_mod
from .compare import check_internal, compare_to_reference
from .extract import EXTRACTOR_VERSION, FileInfo, extract, sha256_file
from .report import build_report, render_terminal, write_json
from .store import Store

EXIT_OK = 0
EXIT_MISMATCH = 1
EXIT_DAMAGED = 2
EXIT_USAGE = 3

MEDIA_SUFFIXES = {".mp4", ".m4v", ".mov"}


class _UsageError(Exception):
    pass


class _Parser(argparse.ArgumentParser):
    def error(self, message):
        self.print_usage(sys.stderr)
        self.exit(EXIT_USAGE, f"trackaudit: error: {message}\n")


def build_parser() -> argparse.ArgumentParser:
    parser = _Parser(
        prog="trackaudit",
        description="Audit multi-track MP4 masters: box structure, track "
                    "metadata, cross-file consistency and packaging.")
    sub = parser.add_subparsers(dest="command", required=True)
    for name, help_text in (("inspect", "check files and report"),
                            ("pack", "validate and package accepted media")):
        p = sub.add_parser(name, help=help_text)
        p.add_argument("inputs", nargs="+",
                       help="MP4 files or directories (recursed; symlinks "
                            "are not followed)")
        p.add_argument("--db", metavar="PATH",
                       help="SQLite cache of results keyed by content hash")
        p.add_argument("--max-duration-delta-ms", type=float, default=40.0,
                       metavar="MS",
                       help="tolerance for declared-vs-actual duration "
                            "differences (default: 40)")
        if name == "inspect":
            p.add_argument("--json", metavar="PATH",
                           help="write the full report as JSON")
            p.add_argument("--reference", metavar="PATH",
                           help="use this input file as the comparison "
                                "reference (default: first valid file)")
        else:
            p.add_argument("--out", required=True, metavar="DIR",
                           help="package output directory (must not exist)")
    return parser


def _collect_inputs(raw_inputs) -> tuple[list, list]:
    """Expand input paths into a sorted file list plus input errors."""
    files: list[Path] = []
    errors: list[dict] = []
    for raw in raw_inputs:
        path = Path(raw)
        if path.is_symlink():
            errors.append({"path": str(path),
                           "error": "symbolic links are not accepted"})
            continue
        if not path.exists():
            raise _UsageError(f"input not found: {raw}")
        if path.is_dir():
            for root, dirnames, filenames in os.walk(path,
                                                     followlinks=False):
                dirnames[:] = sorted(
                    d for d in dirnames
                    if not (Path(root) / d).is_symlink())
                for name in sorted(filenames):
                    candidate = Path(root) / name
                    if candidate.is_symlink():
                        continue
                    if candidate.suffix.lower() in MEDIA_SUFFIXES:
                        files.append(candidate)
        elif path.is_file():
            files.append(path)
        else:
            errors.append({"path": str(path),
                           "error": "not a regular file or directory"})
    unique = sorted({str(f) for f in files})
    return [Path(f) for f in unique], errors


def _process_files(files, store: Store | None) -> list:
    results = []
    for path in files:
        try:
            digest = sha256_file(path)
        except OSError as exc:
            results.append(FileInfo(
                path=str(path), ok=False,
                errors=[{"category": "io", "offset": 0,
                         "message": str(exc)}]))
            continue
        cached = store.lookup(digest, EXTRACTOR_VERSION) if store else None
        if cached is not None:
            info = FileInfo.from_dict(cached)
            info.path = str(path)
            info.cached = True
        else:
            info = extract(path, digest=digest)
            if store:
                store.save(digest, path, EXTRACTOR_VERSION, info.to_dict())
        results.append(info)
    return results


def _compute_mismatches(results, reference, tolerance_ms) -> dict:
    mismatches = {}
    for info in results:
        if not info.ok:
            continue
        found = check_internal(info, tolerance_ms)
        if reference is not None and info.path != reference.path:
            found += compare_to_reference(reference, info, tolerance_ms)
        mismatches[info.path] = found
    return mismatches


def _open_store(args):
    if not getattr(args, "db", None):
        return None
    try:
        return Store(args.db)
    except Exception as exc:
        print(f"trackaudit: cannot open database {args.db}: {exc}",
              file=sys.stderr)
        raise SystemExit(EXIT_USAGE)


def _pick_reference(results, requested):
    ok_results = [r for r in results if r.ok]
    if requested is not None:
        for info in ok_results:
            if info.path == str(Path(requested)):
                return info
        raise _UsageError(f"reference {requested} is not a valid input "
                          "file with parseable structure")
    if not ok_results:
        return None
    # Default reference: the valid file with the most tracks (ties break
    # by path order), so a stripped-down variant is not picked as the
    # baseline against which everything else looks "added".
    return sorted(ok_results, key=lambda r: (-len(r.tracks), r.path))[0]


def _exit_code(results, mismatches, input_errors) -> int:
    if input_errors or any(not r.ok for r in results):
        return EXIT_DAMAGED
    if any(mismatches.values()):
        return EXIT_MISMATCH
    return EXIT_OK


def _cmd_inspect(args) -> int:
    files, input_errors = _collect_inputs(args.inputs)
    if not files and not input_errors:
        raise _UsageError("no media files found in the given inputs")
    store = _open_store(args)
    try:
        results = _process_files(files, store)
    finally:
        if store:
            store.close()
    reference = _pick_reference(results, args.reference)
    mismatches = _compute_mismatches(results, reference,
                                     args.max_duration_delta_ms)
    report = build_report("inspect", results, mismatches, input_errors,
                          reference.path if reference else None)
    print(render_terminal(report))
    if args.json:
        try:
            write_json(report, args.json)
        except OSError as exc:
            print(f"trackaudit: cannot write JSON report: {exc}",
                  file=sys.stderr)
            return EXIT_USAGE
    return _exit_code(results, mismatches, input_errors)


def _cmd_pack(args) -> int:
    files, input_errors = _collect_inputs(args.inputs)
    if not files:
        raise _UsageError("no media files found in the given inputs")
    if input_errors:
        for error in input_errors:
            print(f"input error: {error['path']}: {error['error']}",
                  file=sys.stderr)
        return EXIT_DAMAGED
    store = _open_store(args)
    try:
        results = _process_files(files, store)
    finally:
        if store:
            store.close()
    reference = _pick_reference(results, None)
    mismatches = _compute_mismatches(results, reference,
                                     args.max_duration_delta_ms)
    report = build_report("pack", results, mismatches, [],
                          reference.path if reference else None)
    print(render_terminal(report))
    policy = pack_mod.PackPolicy(
        max_duration_delta_ms=args.max_duration_delta_ms)
    try:
        manifest = pack_mod.build_package(results, mismatches, args.out,
                                          policy)
    except pack_mod.PackError as exc:
        print(f"trackaudit: {exc}; no package written", file=sys.stderr)
        return exc.exit_code
    print(f"package={args.out} files={len(manifest['files'])} "
          "manifest=manifest.json")
    return EXIT_OK


def main(argv=None) -> int:
    args = build_parser().parse_args(argv)
    try:
        if args.command == "inspect":
            return _cmd_inspect(args)
        return _cmd_pack(args)
    except _UsageError as exc:
        print(f"trackaudit: error: {exc}", file=sys.stderr)
        return EXIT_USAGE


if __name__ == "__main__":
    sys.exit(main())
