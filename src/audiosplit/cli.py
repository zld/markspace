"""Command-line interface for audiosplit.

Exit codes: 0 all files succeeded, 1 at least one file failed,
2 parameter or global input error (bad flags, missing paths, bad database).
"""
from __future__ import annotations

import argparse
import json
import sqlite3
import sys
from pathlib import Path

from .batch import process_file, run_batch
from .export import export
from .models import DetectParams, ParamError
from .store import Store


def _add_detector_flags(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--frame-ms", type=float, default=10.0)
    parser.add_argument("--noise-margin-db", type=float, default=8.0)
    parser.add_argument("--min-silence-ms", type=float, default=120.0)
    parser.add_argument("--min-segment-ms", type=float, default=30.0)
    parser.add_argument("--pad-ms", type=float, default=20.0)


def _params(args: argparse.Namespace) -> DetectParams:
    params = DetectParams(
        frame_ms=args.frame_ms,
        noise_margin_db=args.noise_margin_db,
        min_silence_ms=args.min_silence_ms,
        min_segment_ms=args.min_segment_ms,
        pad_ms=args.pad_ms,
    )
    params.validate()  # raises ParamError -> exit code 2
    return params


def _print_summary(summary) -> None:
    print(
        json.dumps(
            {
                "run_id": summary.run_id,
                "files_ok": summary.files_ok,
                "files_failed": summary.files_failed,
                "segments": summary.segment_count,
                "silence_total_ms": round(summary.silence_total_ms(), 1),
            },
            indent=2,
        )
    )
    for outcome in summary.outcomes:
        if outcome.status == "failed":
            print(f"FAILED {outcome.rel_path}: {outcome.error}", file=sys.stderr)


def _cmd_batch(args: argparse.Namespace) -> int:
    params = _params(args)
    input_path = Path(args.input)
    if not input_path.exists():
        print(f"error: input path does not exist: {input_path}", file=sys.stderr)
        return 2
    try:
        with Store(args.db) as store:
            summary = run_batch(input_path, store, params)
            if args.export:
                export(store, summary.run_id, args.export)
    except (OSError, sqlite3.Error, ParamError, KeyError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    _print_summary(summary)
    return 1 if summary.files_failed else 0


def _cmd_detect(args: argparse.Namespace) -> int:
    """Single-file mode: same detector, still persisted for traceability."""
    params = _params(args)
    path = Path(args.input)
    if not path.is_file():
        print(f"error: not a file: {path}", file=sys.stderr)
        return 2
    try:
        with Store(args.db) as store:
            run_id = store.start_run(params)
            outcome = process_file(path, params)
            store.conn.execute("SAVEPOINT file_write")
            if outcome.status == "ok" and outcome.audio and outcome.result:
                store.record_result(
                    run_id, outcome.rel_path, outcome.content_sha256,
                    outcome.audio, outcome.result,
                )
            else:
                store.record_failure(
                    run_id, outcome.rel_path, outcome.content_sha256,
                    outcome.error or "unknown error",
                )
            store.conn.execute("RELEASE SAVEPOINT file_write")
            store.commit()
            if args.export:
                export(store, run_id, args.export)
    except (OSError, sqlite3.Error, ParamError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2

    if outcome.status == "failed":
        print(f"FAILED {outcome.rel_path}: {outcome.error}", file=sys.stderr)
        return 1
    result = outcome.result
    print(
        json.dumps(
            {
                "run_id": run_id,
                "file": outcome.rel_path,
                "segments": result.segment_count(),
                "silence_total_ms": round(result.silence_total_ms(), 1),
                "noise_floor_db": round(result.noise_floor_db, 2),
            },
            indent=2,
        )
    )
    return 0


def _cmd_export(args: argparse.Namespace) -> int:
    try:
        with Store(args.db) as store:
            run_id = args.run_id if args.run_id is not None else store.latest_run_id()
            target = export(store, run_id, args.output)
    except (OSError, sqlite3.Error, KeyError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    print(json.dumps({"run_id": run_id, "exported": str(target)}, indent=2))
    return 0


def _cmd_init_samples(args: argparse.Namespace) -> int:
    # Imported lazily so the core package never requires the scripts folder.
    scripts_dir = Path(__file__).resolve().parents[2] / "scripts"
    sys.path.insert(0, str(scripts_dir))
    from make_samples import build

    try:
        created = build(Path(args.out))
    except OSError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    for name in created:
        print(f"created {name}")
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="audiosplit", description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    p_batch = sub.add_parser("batch", help="process a directory of WAV files")
    p_batch.add_argument("--input", required=True)
    p_batch.add_argument("--db", required=True)
    p_batch.add_argument("--export", default=None, help="optional .json/.csv output")
    _add_detector_flags(p_batch)
    p_batch.set_defaults(func=_cmd_batch)

    p_detect = sub.add_parser("detect", help="process one WAV file")
    p_detect.add_argument("--input", required=True)
    p_detect.add_argument("--db", required=True)
    p_detect.add_argument("--export", default=None)
    _add_detector_flags(p_detect)
    p_detect.set_defaults(func=_cmd_detect)

    p_export = sub.add_parser("export", help="export a stored run")
    p_export.add_argument("--db", required=True)
    p_export.add_argument("--run-id", type=int, default=None)
    p_export.add_argument("--output", required=True)
    p_export.set_defaults(func=_cmd_export)

    p_init = sub.add_parser("init-samples", help="generate deterministic fixtures")
    p_init.add_argument("--out", required=True)
    p_init.set_defaults(func=_cmd_init_samples)
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        return args.func(args)
    except ParamError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
