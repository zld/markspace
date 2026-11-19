"""Command line entry point.

Exit codes
----------
0  success, no blocking inconsistency
1  input or parse error (missing file, unsupported container, corrupt bytes)
2  inconsistencies found by the comparison
3  database or output write failure
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any, Sequence

from . import __version__
from .compare import blocking_mismatches, compare_media
from .model import MediaFile
from .packer import PackError, build_manifest, write_manifest
from .probe import probe_path
from .store import Store, StoreError, open_store

EXIT_OK = 0
EXIT_INPUT = 1
EXIT_MISMATCH = 2
EXIT_STORE = 3


class CliError(Exception):
    def __init__(self, message: str, code: int = EXIT_INPUT):
        self.message = message
        self.code = code
        super().__init__(message)


def _emit(payload: dict[str, Any], full_json: bool) -> None:
    """Print a terminal summary, or the full JSON document when requested."""
    if full_json:
        print(json.dumps(payload, indent=2, sort_keys=True))
        return
    for key, value in payload.items():
        if isinstance(value, (dict, list)):
            print(f"{key}: {json.dumps(value, sort_keys=True)}")
        else:
            print(f"{key}: {value}")


def _write_json(payload: dict[str, Any], path: Path | None) -> None:
    if path is None:
        return
    try:
        if path.parent and not path.parent.exists():
            raise CliError(
                f"output directory does not exist: {path.parent}", EXIT_STORE)
        path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n",
                        encoding="utf-8")
    except CliError:
        raise
    except OSError as exc:
        raise CliError(
            f"cannot write {path}: {exc.strerror or exc}", EXIT_STORE) from exc


def _load_media(path: str) -> MediaFile:
    return probe_path(path)


def _store_media(store: Store, media: MediaFile) -> None:
    if media.sha256:
        try:
            store.save_media(media)
        except StoreError as exc:
            raise CliError(str(exc), EXIT_STORE) from exc


# -- probe -----------------------------------------------------------------

def cmd_probe(args: argparse.Namespace) -> int:
    media = _load_media(args.input)
    payload = {
        "path": media.path,
        "container": media.container,
        "duration_ms": media.duration_ms,
        "track_count": media.track_count,
        "probe_ok": media.probe_ok,
        "sha256": media.sha256,
        "tracks": [
            {
                "track_id": t.track_id,
                "kind": t.kind,
                "codec": t.codec,
                "time_base": t.time_base,
                "duration_ms": t.duration_ms,
            }
            for t in media.tracks
        ],
        "issues": [i.to_dict() for i in media.issues],
        "json_path": str(args.json) if args.json else None,
    }
    if args.db:
        with open_store(args.db) as store:
            _store_media(store, media)
    _write_json(payload, args.json)
    _emit(payload, args.print_json)
    if media.fatal:
        print(f"error: {_fatal_reason(media)}", file=sys.stderr)
        return EXIT_INPUT
    return EXIT_OK


def _fatal_reason(media: MediaFile) -> str:
    for severity in ("fatal", "error"):
        for issue in media.issues:
            if issue.severity == severity:
                where = (f" at offset {issue.offset}"
                         if issue.offset is not None else "")
                return f"{issue.code}: {issue.message}{where}"
    return "probe failed without a diagnostic"


# -- compare ---------------------------------------------------------------

def _split_fields(values: Sequence[str]) -> list[str]:
    fields: list[str] = []
    for value in values:
        fields.extend(part.strip() for part in value.split(",") if part.strip())
    return fields


def cmd_compare(args: argparse.Namespace) -> int:
    baseline = _load_media(args.baseline)
    candidate = _load_media(args.candidate)
    result = compare_media(
        baseline, candidate,
        tolerance_ms=args.tolerance_ms,
        ignore_fields=_split_fields(args.ignore_field),
    )
    payload = {
        "baseline_tracks": result.baseline_tracks,
        "candidate_tracks": result.candidate_tracks,
        "duration_delta_ms": result.duration_delta_ms,
        "mismatches": [m.to_dict() for m in result.mismatches],
        "status": result.status,
        "json_path": str(args.json) if args.json else None,
        "baseline": baseline.to_dict(),
        "candidate": candidate.to_dict(),
    }

    if args.db:
        with open_store(args.db) as store:
            _store_media(store, baseline)
            _store_media(store, candidate)
            if not (baseline.fatal or candidate.fatal):
                try:
                    store.save_validation(result)
                except StoreError as exc:
                    raise CliError(str(exc), EXIT_STORE) from exc

    _write_json(payload, args.json)
    if args.print_json:
        _emit(payload, True)
    else:
        _emit({k: payload[k] for k in (
            "baseline_tracks", "candidate_tracks", "duration_delta_ms",
            "status", "json_path")}, False)
        for mismatch in result.mismatches:
            offset = (f" @{mismatch.offset}"
                      if mismatch.offset is not None else "")
            print(f"  - {mismatch.kind}{offset}: {mismatch.message}")

    if baseline.fatal or candidate.fatal:
        print(f"error: {_fatal_reason(baseline if baseline.fatal else candidate)}",
              file=sys.stderr)
        return EXIT_INPUT
    if blocking_mismatches(result):
        return EXIT_MISMATCH
    return EXIT_OK


# -- pack ------------------------------------------------------------------

def cmd_pack(args: argparse.Namespace) -> int:
    entries = []
    failed = False
    for raw in args.input:
        media = _load_media(raw)
        if media.fatal:
            failed = True
        entries.append((media, None))

    manifest = build_manifest(entries, allow_issues=args.allow_issues)
    payload = {
        "path": str(args.json) if args.json else None,
        "track_count": manifest["track_count"],
        "file_count": manifest["file_count"],
        "sha256": entries[0][0].sha256 if entries else None,
        "status": manifest["status"],
        "blocked": manifest["blocked"],
    }
    if args.db:
        with open_store(args.db) as store:
            for media, _ in entries:
                _store_media(store, media)
    if args.json:
        write_manifest(manifest, args.json)
        payload["json_path"] = str(args.json)
    _emit(payload, args.print_json)

    if failed:
        print("error: one or more inputs could not be probed", file=sys.stderr)
        return EXIT_INPUT
    if manifest["status"] == "blocked":
        print("error: manifest blocked by validation problems", file=sys.stderr)
        return EXIT_MISMATCH
    if manifest["status"] == "diagnostic":
        print("warning: diagnostic manifest includes blocking problems",
              file=sys.stderr)
    return EXIT_OK


# -- parser ----------------------------------------------------------------

def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="mediacheck",
        description="Read container metadata from bytes and verify that "
                    "different versions of the same asset stay consistent.")
    parser.add_argument("--version", action="version",
                        version=f"mediacheck {__version__}")
    parser.add_argument("--print-json", action="store_true",
                        help="print the full JSON document to stdout")
    sub = parser.add_subparsers(dest="command", required=True)

    probe = sub.add_parser("probe", help="probe one media file")
    probe.add_argument("--input", required=True)
    probe.add_argument("--db")
    probe.add_argument("--json", type=Path)
    probe.set_defaults(func=cmd_probe)

    compare = sub.add_parser("compare",
                             help="compare a candidate against a baseline")
    compare.add_argument("--baseline", required=True)
    compare.add_argument("--candidate", required=True)
    compare.add_argument("--db")
    compare.add_argument("--json", type=Path)
    compare.add_argument("--tolerance-ms", type=float, default=40.0)
    compare.add_argument("--ignore-field", action="append", default=[],
                         help="field name to ignore (repeatable or comma list)")
    compare.set_defaults(func=cmd_compare)

    pack = sub.add_parser("pack", help="build a packing manifest")
    pack.add_argument("--input", action="append", required=True,
                      help="media file to include (repeatable)")
    pack.add_argument("--db")
    pack.add_argument("--json", type=Path)
    pack.add_argument("--allow-issues", action="store_true",
                      help="emit a diagnostic manifest even when media fail")
    pack.set_defaults(func=cmd_pack)
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        return int(args.func(args))
    except CliError as exc:
        print(f"error: {exc.message}", file=sys.stderr)
        return exc.code
    except StoreError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_STORE
    except PackError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_STORE
    except BrokenPipeError:  # pragma: no cover - shell pipe closed early
        return EXIT_OK


if __name__ == "__main__":  # pragma: no cover
    sys.exit(main())
