"""Command-line entry points: ``scan``, ``check`` and ``pack``."""

from __future__ import annotations

import argparse
import os
import sys
from typing import Any

from . import compare, report, scan, store
from .compare import BaselineError
from .errors import ERR_BASELINE_MISSING, ERR_INTERNAL, ERR_IO, ScanFailure
from .limits import Limits

EXIT_OK = 0
EXIT_MISMATCH = 1
EXIT_USAGE = 2


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="mediascan",
        description="Index and consistency-check local media containers.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    def common(p: argparse.ArgumentParser) -> None:
        p.add_argument("paths", nargs="+", help="file or directory roots")
        p.add_argument("--db", help="SQLite index path")
        p.add_argument("--json", dest="json_path", help="JSON report path")
        p.add_argument("--no-recursive", action="store_true",
                       help="do not descend into subdirectories")
        p.add_argument("--max-file-size", type=int, default=None,
                       help="skip files larger than this many bytes")
        p.add_argument("--max-files", type=int, default=10_000,
                       help="abort discovery beyond this many files")

    scan_p = sub.add_parser("scan", help="index media metadata")
    common(scan_p)

    check_p = sub.add_parser("check", help="compare media against a baseline")
    common(check_p)
    check_p.add_argument("--baseline", required=True,
                         help="baseline JSON produced by make_samples.py")
    check_p.add_argument("--duration-tolerance-ms", type=int, default=25,
                         help="absolute duration deviation allowed")
    check_p.add_argument("--relative-tolerance-pct", type=float, default=None,
                         help="optional relative deviation allowed, in percent")
    check_p.add_argument("--base-dir", default=None,
                         help="directory baseline paths are relative to")

    pack_p = sub.add_parser("pack", help="verify files before packaging")
    common(pack_p)
    pack_p.add_argument("--require-known", action="store_true",
                        help="fail files that have unknown metadata fields")

    return parser


def exit_code_for(failures: int, mismatches: int) -> int:
    return EXIT_MISMATCH if (failures or mismatches) else EXIT_OK


def _limits(args: argparse.Namespace) -> Limits:
    if args.max_file_size is not None and args.max_file_size <= 0:
        raise ScanFailure(ERR_IO, "--max-file-size must be positive", 0)
    return Limits(
        max_file_size=(
            args.max_file_size if args.max_file_size is not None
            else Limits.max_file_size
        )
    )


def _discover(args: argparse.Namespace, limits: Limits,
              exclude: list[str]) -> scan.Discovery:
    base = getattr(args, "base_dir", None) or _default_base(args.paths)
    return scan.discover(
        args.paths,
        recursive=not args.no_recursive,
        exclude=exclude,
        base=base,
        max_files=args.max_files,
    )


def _open_store(args: argparse.Namespace) -> store.Store | None:
    if not args.db:
        return None
    try:
        return store.Store(args.db)
    except store.StoreError as exc:
        raise ScanFailure("ERR_DB", str(exc), 0) from exc


def _default_base(paths: list[str]) -> str:
    """Relative paths key off the single scan root, else the current directory."""
    if len(paths) == 1 and os.path.isdir(paths[0]):
        return os.path.abspath(paths[0])
    return os.getcwd()


def _write_json(path: str | None, payload: dict[str, Any]) -> str | None:
    if not path:
        return None
    try:
        report.write_json_atomic(path, payload)
    except OSError as exc:
        return str(exc)
    return None


# -- commands ---------------------------------------------------------------


def _failure_counts(failures: list[dict]) -> dict[str, int]:
    counts: dict[str, int] = {}
    for item in failures:
        code = item.get("error_code", ERR_INTERNAL)
        counts[code] = counts.get(code, 0) + 1
    return dict(sorted(counts.items()))


def _record_failure_safely(
    st: store.Store | None, outcome: scan.ScanOutcome
) -> dict | None:
    """Record a failed scan without letting index trouble hide the failure."""
    failure = outcome.failure
    code = failure.code if failure else ERR_INTERNAL
    if st is not None:
        try:
            st.record_failure(outcome.task.rel_path, outcome.task.rel_path, code)
        except store.StoreError:
            pass
    return _failure_entry_from(outcome)


def cmd_check(args: argparse.Namespace) -> int:
    try:
        limits = _limits(args)
    except ScanFailure as exc:
        report.print_failure(exc)
        return EXIT_USAGE
    try:
        baseline = compare.load_baseline(args.baseline)
    except BaselineError as exc:
        print(f"[ERROR] {exc}", file=sys.stderr)
        return EXIT_USAGE
    exclude = [args.baseline, args.json_path] if args.json_path else [args.baseline]
    if args.db:
        exclude.append(args.db)
    try:
        discovery = _discover(args, limits, exclude)
    except ScanFailure as exc:
        report.print_failure(exc)
        return EXIT_USAGE
    outcomes = scan.scan_tasks(discovery.tasks, limits)

    try:
        st = _open_store(args)
    except ScanFailure as exc:
        report.print_failure(exc)
        return EXIT_USAGE

    files: list[dict] = []
    mismatches: list[dict] = []
    failures: list[dict] = []
    tracks_total = 0
    max_delta: int | None = None
    aggregate = {
        "duration": 0,
        "timebase": 0,
        "param": 0,
        "track_missing": 0,
        "track_extra": 0,
        "track_type": 0,
    }
    try:
        for outcome in outcomes:
            if outcome.ok:
                record = outcome.record
                assert record is not None
                tracks_total += len(record.tracks)
                comparison = compare.compare_record(
                    record,
                    baseline,
                    duration_tolerance_ms=args.duration_tolerance_ms,
                    relative_tolerance_pct=args.relative_tolerance_pct,
                )
                entry = record.to_dict()
                entry["mismatch_count"] = len(comparison.mismatches)
                entry["max_duration_delta_ms"] = comparison.max_duration_delta_ms
                files.append(entry)
                mismatches.extend(m.to_dict() for m in comparison.mismatches)
                for key, count in comparison.categories.items():
                    aggregate[key] += count
                if comparison.max_duration_delta_ms is not None:
                    max_delta = (
                        comparison.max_duration_delta_ms
                        if max_delta is None
                        else max(max_delta, comparison.max_duration_delta_ms)
                    )
                if st is not None:
                    try:
                        st.record_scan(outcome.task.rel_path, record)
                    except store.StoreError as exc:
                        failures.append(_failure_entry(outcome.task.rel_path, exc))
            else:
                failures.append(_record_failure_safely(st, outcome))
    finally:
        if st is not None:
            st.close()

    summary = report.build_summary(
        files_scanned=len(files),
        tracks_total=tracks_total,
        mismatches=len(mismatches),
        failures=len(failures),
        max_duration_delta_ms=max_delta,
        skipped=len(discovery.skipped),
        counts={
            "mismatches_by_category": aggregate,
            "failures_by_code": _failure_counts(failures),
        },
    )
    payload = report.build_report(summary, files, mismatches, failures)
    if discovery.skipped:
        payload["skipped"] = discovery.skipped
    report.print_summary(summary, failures)
    write_error = _write_json(args.json_path, payload)
    if write_error:
        print(f"[WARN] report not written: {write_error}", file=sys.stderr)
    return exit_code_for(len(failures), len(mismatches))


def cmd_scan(args: argparse.Namespace) -> int:
    try:
        limits = _limits(args)
    except ScanFailure as exc:
        report.print_failure(exc)
        return EXIT_USAGE
    exclude = [p for p in (args.json_path, args.db) if p]
    try:
        discovery = _discover(args, limits, exclude)
    except ScanFailure as exc:
        report.print_failure(exc)
        return EXIT_USAGE
    outcomes = scan.scan_tasks(discovery.tasks, limits)
    try:
        st = _open_store(args)
    except ScanFailure as exc:
        report.print_failure(exc)
        return EXIT_USAGE
    files: list[dict] = []
    failures: list[dict] = []
    tracks_total = 0
    try:
        for outcome in outcomes:
            if outcome.ok:
                assert outcome.record is not None
                tracks_total += len(outcome.record.tracks)
                files.append(outcome.record.to_dict())
                if st is not None:
                    try:
                        st.record_scan(outcome.task.rel_path, outcome.record)
                    except store.StoreError as exc:
                        failures.append(_failure_entry(outcome.task.rel_path, exc))
            else:
                failures.append(_record_failure_safely(st, outcome))
    finally:
        if st is not None:
            st.close()
    summary = report.build_summary(
        files_scanned=len(files),
        tracks_total=tracks_total,
        mismatches=0,
        failures=len(failures),
        max_duration_delta_ms=None,
        skipped=len(discovery.skipped),
        counts={"mismatches_by_category": {}, "failures_by_code":
                _failure_counts(failures)},
    )
    payload = report.build_report(summary, files, [], failures)
    if discovery.skipped:
        payload["skipped"] = discovery.skipped
    report.print_summary(summary, failures)
    for entry in files:
        print(f"{entry['path']} {entry['container']['container']} "
              f"tracks={entry['track_count']} "
              f"duration_ms={_fmt(entry['duration_ms'])} sha256={entry['sha256'][:12]}")
    write_error = _write_json(args.json_path, payload)
    if write_error:
        print(f"[WARN] report not written: {write_error}", file=sys.stderr)
    return exit_code_for(len(failures), 0)


def cmd_pack(args: argparse.Namespace) -> int:
    """Re-extract every pending file with the shared extractor and verify it."""
    try:
        limits = _limits(args)
    except ScanFailure as exc:
        report.print_failure(exc)
        return EXIT_USAGE
    exclude = [p for p in (args.json_path, args.db) if p]
    try:
        discovery = _discover(args, limits, exclude)
    except ScanFailure as exc:
        report.print_failure(exc)
        return EXIT_USAGE
    outcomes = scan.scan_tasks(discovery.tasks, limits)
    try:
        st = _open_store(args)
    except ScanFailure as exc:
        report.print_failure(exc)
        return EXIT_USAGE
    files: list[dict] = []
    failures: list[dict] = []
    tracks_total = 0
    try:
        for outcome in outcomes:
            if not outcome.ok:
                failures.append(_failure_entry_from(outcome))
                continue
            assert outcome.record is not None
            record = outcome.record
            if len(record.tracks) == 0:
                failures.append({
                    "path": outcome.task.rel_path,
                    "error_code": ERR_BASELINE_MISSING,
                    "error_detail": "container has no tracks",
                    "error_offset": None,
                })
                continue
            if args.require_known:
                missing = record.unknown_fields() + [
                    f"{t.index}:{name}"
                    for t in record.tracks
                    for name in t.unknown_fields()
                ]
                if missing:
                    failures.append({
                        "path": outcome.task.rel_path,
                        "error_code": "ERR_PARAM_UNKNOWN",
                        "error_detail": "unknown metadata: " + ", ".join(missing),
                        "error_offset": None,
                    })
                    continue
            tracks_total += len(record.tracks)
            entry = record.to_dict()
            files.append(entry)
            print(
                f"[PACK] {record.path} container={record.container['container']}"
                f" tracks={len(record.tracks)}"
                f" duration_ms={_fmt(record.duration_ms)}"
                f" sha256={record.sha256}"
            )
            if st is not None:
                try:
                    st.record_scan(outcome.task.rel_path, record)
                except store.StoreError as exc:
                    failures.append(_failure_entry(outcome.task.rel_path, exc))
    finally:
        if st is not None:
            st.close()
    summary = report.build_summary(
        files_scanned=len(files),
        tracks_total=tracks_total,
        mismatches=0,
        failures=len(failures),
        max_duration_delta_ms=None,
        skipped=len(discovery.skipped),
        counts={"mismatches_by_category": {}, "failures_by_code":
                _failure_counts(failures)},
    )
    payload = report.build_report(summary, files, [], failures)
    report.print_summary(summary, failures)
    write_error = _write_json(args.json_path, payload)
    if write_error:
        print(f"[WARN] report not written: {write_error}", file=sys.stderr)
    return exit_code_for(len(failures), 0)


def _failure_entry_from(outcome: scan.ScanOutcome) -> dict:
    failure = outcome.failure
    code = failure.code if failure else ERR_INTERNAL
    detail = failure.detail if failure else "unknown failure"
    offset = failure.offset if failure else None
    return {
        "path": outcome.task.rel_path,
        "error_code": code,
        "error_detail": detail,
        "error_offset": offset,
    }


def _failure_entry(path: str, exc: Exception) -> dict:
    return {
        "path": path,
        "error_code": getattr(exc, "code", "ERR_STORE"),
        "error_detail": str(exc),
        "error_offset": None,
    }


def _fmt(value: Any) -> str:
    return "unknown" if value is None else str(value)


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    try:
        args = parser.parse_args(argv)
    except SystemExit as exc:
        return int(exc.code) if isinstance(exc.code, int) else EXIT_USAGE
    handlers = {"scan": cmd_scan, "check": cmd_check, "pack": cmd_pack}
    return handlers[args.command](args)


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
