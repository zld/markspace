"""Command-line interface: demo, detect, batch, list and summary subcommands.

Exit codes:
  0 - all files succeeded
  1 - batch completed but one or more files failed
  2 - invalid arguments or local environment error
  3 - database unavailable, corrupt or schema-incompatible
"""
from __future__ import annotations

import argparse
import sys
from pathlib import Path

from .batch import process_directory, process_file
from .detector import DetectParams
from .report import (
    build_batch_report,
    build_list_report,
    build_summary_report,
    render_json,
)
from .signal import AudioError, generate_fixtures
from .storage import Storage, StorageError


def add_common_params(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--db", required=True, help="SQLite database path")
    parser.add_argument("--frame-ms", type=float, default=20.0)
    parser.add_argument("--hop-ms", type=float, default=10.0)
    parser.add_argument("--threshold-db", type=float, default=-38.0)
    parser.add_argument(
        "--adaptive", dest="adaptive", action="store_true", default=True
    )
    parser.add_argument(
        "--no-adaptive", dest="adaptive", action="store_false",
        help="use the absolute --threshold-db instead of noise-floor adaptation",
    )
    parser.add_argument("--noise-percentile", type=float, default=10.0)
    parser.add_argument("--noise-margin-db", type=float, default=8.0)
    parser.add_argument("--hysteresis-db", type=float, default=3.0)
    parser.add_argument("--confirm-ms", type=float, default=60.0)
    parser.add_argument("--min-silence-ms", type=float, default=180.0)
    parser.add_argument("--merge-gap-ms", type=float, default=90.0)
    parser.add_argument("--smooth-window", type=int, default=1)
    parser.add_argument("--json", action="store_true", help="emit JSON output")


def params_from_args(args: argparse.Namespace) -> DetectParams:
    return DetectParams(
        frame_ms=args.frame_ms,
        hop_ms=args.hop_ms,
        threshold_db=args.threshold_db,
        adaptive=args.adaptive,
        noise_percentile=args.noise_percentile,
        noise_margin_db=args.noise_margin_db,
        hysteresis_db=args.hysteresis_db,
        confirm_ms=args.confirm_ms,
        min_silence_ms=args.min_silence_ms,
        merge_gap_ms=args.merge_gap_ms,
        smooth_window=args.smooth_window,
    )


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="audioslice", description="Silence-aware mono PCM audio slicer"
    )
    sub = parser.add_subparsers(dest="command", required=True)

    demo = sub.add_parser("demo", help="generate deterministic sample WAV files")
    demo.add_argument("--db", required=True)
    demo.add_argument("--out", required=True, help="fixture output directory")
    demo.add_argument("--seed", type=int, default=42)
    demo.add_argument("--json", action="store_true")

    detect = sub.add_parser("detect", help="analyse a single WAV file")
    detect.add_argument("wav")
    add_common_params(detect)

    batch = sub.add_parser("batch", help="analyse every WAV in a directory")
    batch.add_argument("input_dir")
    add_common_params(batch)

    list_p = sub.add_parser("list", help="list stored results as JSON")
    list_p.add_argument("--db", required=True)
    list_p.add_argument("--task", help="filter by task id or exact file path")
    list_p.add_argument("--json", action="store_true", default=True)

    summary = sub.add_parser("summary", help="aggregate stored results as JSON")
    summary.add_argument("--db", required=True)
    summary.add_argument("--task", help="filter by task id or exact file path")
    summary.add_argument("--json", action="store_true", default=True)
    return parser


def cmd_demo(args: argparse.Namespace) -> int:
    try:
        paths = generate_fixtures(args.out, seed=args.seed)
        with Storage(args.db):  # initialise / validate the database
            pass
    except StorageError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 3
    except (OSError, AudioError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    payload = {
        "fixture_dir": str(Path(args.out)),
        "seed": args.seed,
        "files": [str(p) for p in paths],
        "db": str(Path(args.db)),
    }
    print(render_json(payload) if args.json else _human_demo(payload))
    return 0


def cmd_detect(args: argparse.Namespace) -> int:
    try:
        params = params_from_args(args)
        with Storage(args.db) as storage:
            outcome = process_file(args.wav, storage, params)
            if outcome.status in ("ok", "cached") and outcome.result_id is not None:
                result = storage.get_result(outcome.result_id)
            else:
                result = None
    except StorageError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 3
    except (ValueError, FileNotFoundError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    if outcome.status == "error":
        print(f"error: {outcome.error}", file=sys.stderr)
        return 1
    payload = {"file": outcome.to_dict(), "params": params.to_dict()}
    if result is not None:
        payload["segments"] = result["segments"]
        payload["silence_regions"] = result["silence_regions"]
        payload["thresholds"] = {
            "close_threshold_db": result["close_threshold_db"],
            "open_threshold_db": result["open_threshold_db"],
            "noise_floor_db": result["noise_floor_db"],
        }
    print(render_json(payload))
    return 0


def cmd_batch(args: argparse.Namespace) -> int:
    try:
        params = params_from_args(args)
        with Storage(args.db) as storage:
            summary = process_directory(args.input_dir, storage, params)
    except StorageError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 3
    except (ValueError, FileNotFoundError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    print(render_json(build_batch_report(summary)))
    return summary.exit_code()


def cmd_list(args: argparse.Namespace) -> int:
    try:
        with Storage(args.db) as storage:
            print(render_json(build_list_report(storage, args.task)))
        return 0
    except StorageError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 3


def cmd_summary(args: argparse.Namespace) -> int:
    try:
        with Storage(args.db) as storage:
            print(render_json(build_summary_report(storage, args.task)))
        return 0
    except StorageError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 3


def _human_demo(payload: dict) -> str:
    lines = [f"generated {len(payload['files'])} fixtures in {payload['fixture_dir']}:"]
    lines.extend(f"  - {p}" for p in payload["files"])
    lines.append(f"database initialised at {payload['db']}")
    return "\n".join(lines)


_HANDLERS = {
    "demo": cmd_demo,
    "detect": cmd_detect,
    "batch": cmd_batch,
    "list": cmd_list,
    "summary": cmd_summary,
}


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        return _HANDLERS[args.command](args)
    except StorageError as exc:  # defensive: handlers normally map these
        print(f"error: {exc}", file=sys.stderr)
        return 3
    except (ValueError, FileNotFoundError, AudioError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
