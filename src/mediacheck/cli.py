"""Command line entry point: scan, compare, and pack."""

from __future__ import annotations

import argparse
import fnmatch
import json
import os
import sys
import tempfile
from pathlib import Path
from typing import Any, Sequence

from .db import Database, compute_batch_id
from .models import Container, Issue, issue_sort_key
from .pack import build_pack
from .probe import MAX_FILE_BYTES, probe_path
from .rules import RULES_VERSION, BatchReport, evaluate

MAX_DEPTH = 16
EXIT_OK, EXIT_INCONSISTENT, EXIT_ERROR = 0, 1, 2


class CliError(Exception):
    """A command-line or environment error (exit code 2)."""


def _is_excluded(relative: str, excludes: Sequence[str]) -> bool:
    name = os.path.basename(relative)
    return any(
        fnmatch.fnmatchcase(relative, pattern) or fnmatch.fnmatchcase(name, pattern)
        for pattern in excludes
    )


def collect_files(roots: list[str], excludes: Sequence[str] = (),
                  max_depth: int = MAX_DEPTH) -> list[str]:
    """Return a stable, sorted list of regular files below `roots`.

    Symbolic links are never followed and depth is bounded.  A file that is
    not below its root after resolution would be a path escape and is
    rejected.
    """

    collected: list[str] = []
    for raw_root in roots:
        root = os.path.abspath(raw_root)
        if not os.path.exists(root):
            raise CliError(f"path does not exist: {raw_root}")
        if os.path.islink(root):
            raise CliError(f"root is a symbolic link: {raw_root}")
        real_root = os.path.realpath(root)
        if os.path.isfile(root):
            collected.append(root)
            continue
        if not os.path.isdir(root):
            raise CliError(f"not a file or directory: {raw_root}")
        for dirpath, dirnames, filenames in os.walk(root, followlinks=False):
            relative_dir = os.path.relpath(dirpath, root)
            depth = 0 if relative_dir == "." else relative_dir.count(os.sep) + 1
            dirnames[:] = sorted(
                name for name in dirnames
                if not os.path.islink(os.path.join(dirpath, name))
                and depth < max_depth
                and not _is_excluded(
                    os.path.relpath(os.path.join(dirpath, name), root).replace(os.sep, "/"),
                    excludes,
                )
            )
            for name in sorted(filenames):
                full = os.path.join(dirpath, name)
                relative = os.path.relpath(full, root).replace(os.sep, "/")
                if _is_excluded(relative, excludes):
                    continue
                if os.path.islink(full):
                    continue
                real = os.path.realpath(full)
                if os.path.commonpath([real, real_root]) != real_root:
                    continue
                collected.append(os.path.abspath(full))
    return sorted(set(collected))


def _probe_all(paths: list[str]) -> list[Container]:
    """Probe every file; one failing file never aborts the rest."""

    return [probe_path(path, max_bytes=MAX_FILE_BYTES) for path in sorted(paths)]


def _atomic_write_json(target: str, payload: dict[str, Any]) -> None:
    directory = os.path.dirname(os.path.abspath(target))
    if not os.path.isdir(directory):
        raise CliError(f"output directory does not exist: {directory}")
    if not os.access(directory, os.W_OK):
        raise CliError(f"output directory is not writable: {directory}")
    text = json.dumps(payload, indent=2, sort_keys=True, ensure_ascii=False) + "\n"
    handle = None
    temporary = None
    try:
        descriptor, temporary = tempfile.mkstemp(
            prefix=".mediacheck-", suffix=".tmp", dir=directory
        )
        handle = os.fdopen(descriptor, "w", encoding="utf-8")
        handle.write(text)
        handle.flush()
        os.fsync(handle.fileno())
        handle.close()
        handle = None
        os.replace(temporary, target)
        temporary = None
    except OSError as exc:
        raise CliError(f"cannot write {target}: {exc.strerror or exc}") from exc
    finally:
        if handle is not None:
            handle.close()
        if temporary is not None and os.path.exists(temporary):
            os.unlink(temporary)


def _format_issue(issue: Issue) -> str:
    location = issue.path or "-"
    if issue.track_id is not None:
        location += f" track {issue.track_id}"
    deviation = (
        f" (deviation {issue.deviation_ms:+d} ms)"
        if issue.deviation_ms is not None else ""
    )
    return (f"[{issue.severity}] {issue.rule} {issue.code} {location}: "
            f"{issue.message}{deviation}")


def _print_report(report: dict[str, Any]) -> None:
    print(f"batch_id: {report['batch_id']}")
    print(f"files: {report['files']}")
    print(f"tracks: {report['tracks']}")
    print(f"duration_deviation_ms: {report['duration_deviation_ms']}")
    for kind, codecs in report["codec_summary"].items():
        print(f"codec {kind}: {', '.join(codecs)}")
    print(f"issues: {len(report['issues'])}")
    for issue in report["issues"]:
        print("  " + _format_issue(Issue.from_dict(issue)))


def _exit_for(report: BatchReport) -> int:
    """Any reported issue counts as an inconsistency (exit 1)."""
    return EXIT_INCONSISTENT if report.issues else EXIT_OK


def _options(args: argparse.Namespace) -> dict[str, Any]:
    return {
        "duration_tolerance_ms": args.duration_tolerance_ms,
        "strict": bool(args.strict),
        "exclude": sorted(args.exclude),
    }


def _scan_paths(args: argparse.Namespace) -> tuple[list[Container], BatchReport, str]:
    paths = collect_files(args.paths, args.exclude, args.max_depth)
    if not paths:
        raise CliError("no input files matched")
    containers = _probe_all(paths)
    options = _options(args)
    batch_id = compute_batch_id(containers, options, RULES_VERSION)
    report = evaluate(containers, os.path.abspath(args.paths[0]),
                      args.duration_tolerance_ms, args.strict)
    return containers, report, batch_id


def cmd_scan(args: argparse.Namespace) -> int:
    containers, report, batch_id = _scan_paths(args)
    payload = report.scan_dict(batch_id)
    _print_report(payload)
    with Database(args.db) as database:
        database.save_batch(containers, report, _options(args), batch_id)
    if args.json:
        _atomic_write_json(args.json, payload)
    return _exit_for(report)


def _baseline_issues(baseline: list[Container],
                     current: list[Container]) -> list[Issue]:
    previous = {c.path: c for c in baseline}
    issues: list[Issue] = []
    for container in current:
        old = previous.get(container.path)
        if old is None:
            issues.append(Issue(
                severity="warning", code="input-added", rule="R010",
                message="path was not present in the previous batch",
                path=container.path,
            ))
            continue
        if old.sha256 != container.sha256:
            issues.append(Issue(
                severity="warning", code="input-changed", rule="R010",
                message=f"content changed since batch {old.sha256[:12]}",
                path=container.path, expected=old.sha256, actual=container.sha256,
            ))
    return issues


def cmd_compare(args: argparse.Namespace) -> int:
    containers, report, batch_id = _scan_paths(args)
    with Database(args.db) as database:
        latest = database.latest_batch()
        baseline_id = None
        baseline: list[Container] = []
        if latest is not None and latest["batch_id"] != batch_id:
            baseline_id = latest["batch_id"]
            loaded = database.load_batch(baseline_id)
            baseline = loaded[0] if loaded else []
        if baseline:
            report.issues.extend(_baseline_issues(baseline, containers))
            report.issues.sort(key=issue_sort_key)
        database.save_batch(containers, report, _options(args), batch_id)
    payload = report.scan_dict(batch_id)
    payload["baseline_batch_id"] = baseline_id
    _print_report(payload)
    if args.json:
        _atomic_write_json(args.json, payload)
    return _exit_for(report)


def cmd_pack(args: argparse.Namespace) -> int:
    missing = [p for p in args.files if not os.path.isfile(p)]
    if missing:
        raise CliError(f"missing input file: {missing[0]}")
    result = build_pack(args.files, max_bytes=MAX_FILE_BYTES)
    manifest = result["manifest"]
    print(f"inputs: {len(manifest['inputs'])}")
    print(f"tracks: {len(manifest['tracks'])}")
    for row in manifest["tracks"]:
        print(f"  {row['path']} track {row['track_id']}: "
              f"{row['kind']} {row['codec']} {row['duration_ms']} ms")
    for error in manifest["errors"]:
        print(f"[error] {error['path']}: {error['message']} at offset "
              f"{error['offset']}", file=sys.stderr)
    _atomic_write_json(args.out, manifest)
    print(f"manifest: {args.out}")
    return int(result["exit_code"])


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="mediacheck",
        description="Probe local media containers and check version consistency.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    def common(name: str, help_text: str) -> argparse.ArgumentParser:
        cmd = sub.add_parser(name, help=help_text)
        cmd.add_argument("paths", nargs="+", help="files or directories to scan")
        cmd.add_argument("--db", required=True, help="SQLite database path")
        cmd.add_argument("--json", help="write the report to this JSON file")
        cmd.add_argument("--exclude", action="append", default=[],
                         metavar="PATTERN", help="exclude paths matching a glob")
        cmd.add_argument("--strict", action="store_true",
                         help="treat unknown metadata as an error")
        cmd.add_argument("--duration-tolerance-ms", type=int, default=0,
                         metavar="MS", help="allowed duration deviation in ms")
        cmd.add_argument("--max-depth", type=int, default=MAX_DEPTH,
                         help=f"maximum recursion depth (default {MAX_DEPTH})")
        return cmd

    common("scan", "probe files, apply rules, and record a batch")
    common("compare", "re-check files and compare against the previous batch")
    pack = sub.add_parser("pack", help="build a packaging manifest")
    pack.add_argument("files", nargs="+", help="files to include")
    pack.add_argument("--out", required=True, help="manifest JSON output path")
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    try:
        args = parser.parse_args(argv)
    except SystemExit as exc:  # argparse already printed usage
        return int(exc.code)
    if getattr(args, "duration_tolerance_ms", 0) < 0:
        print("mediacheck: error: --duration-tolerance-ms must be >= 0",
              file=sys.stderr)
        return EXIT_ERROR
    try:
        if args.command == "scan":
            return cmd_scan(args)
        if args.command == "compare":
            return cmd_compare(args)
        return cmd_pack(args)
    except CliError as exc:
        print(f"mediacheck: error: {exc}", file=sys.stderr)
        return EXIT_ERROR
    except (OSError, ValueError) as exc:
        print(f"mediacheck: error: {exc}", file=sys.stderr)
        return EXIT_ERROR


if __name__ == "__main__":
    raise SystemExit(main())
