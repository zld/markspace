"""Command line interface wiring WAV loading, detection, storage and export.

Exit codes follow the acceptance contract:
* ``0`` - success
* ``2`` - bad command-line arguments or rejected input (missing/corrupt WAV,
  unsupported format, empty audio, non-finite samples, unknown task id)
* ``1`` - any uncaught internal error
"""

from __future__ import annotations

import argparse
import sys

from .detector import detect
from .export import export_tasks
from .model import AnalysisParams
from .store import connect, latest_task_id, list_task_ids, load_task, save_task
from .wav import WavFormatError, load_wav


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="audioslice",
        description="Detect sound segments and silence regions in PCM WAV files.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    analyze = sub.add_parser("analyze", help="analyze a WAV file and persist the result as a task")
    analyze.add_argument("--input", required=True, help="path to a PCM WAV file")
    analyze.add_argument("--db", required=True, help="SQLite database path (created on first run)")
    analyze.add_argument("--frame-ms", type=float, default=10.0, help="analysis frame length in milliseconds")
    analyze.add_argument("--margin-db", type=float, default=9.0, help="threshold margin above the noise floor in dB")
    analyze.add_argument("--hysteresis-db", type=float, default=3.0, help="exit/enter hysteresis in dB")
    analyze.add_argument("--noise-percentile", type=float, default=20.0, help="low percentile used for the noise floor")
    analyze.add_argument("--min-segment-ms", type=float, default=90.0, help="minimum segment duration in milliseconds")
    analyze.add_argument("--min-silence-ms", type=float, default=180.0, help="minimum splitting silence in milliseconds")

    summary = sub.add_parser("summary", help="summarize stored tasks")
    summary.add_argument("--db", required=True, help="SQLite database path")
    group = summary.add_mutually_exclusive_group(required=True)
    group.add_argument("--task-id", type=int, help="summarize one specific task")
    group.add_argument("--latest", action="store_true", help="summarize the most recent task")
    group.add_argument("--all", action="store_true", help="summarize every stored task")

    export = sub.add_parser("export", help="batch-export stored regions to JSONL")
    export.add_argument("--db", required=True, help="SQLite database path")
    export.add_argument("--out", required=True, help="destination JSONL path")
    export.add_argument("--task-id", type=int, action="append", dest="task_ids", help="task id to export; repeatable")
    export.add_argument("--all", action="store_true", help="export every stored task")

    return parser


def _run_analyze(args: argparse.Namespace) -> int:
    audio = load_wav(args.input)
    params = AnalysisParams.from_milliseconds(
        sample_rate=audio.sample_rate,
        frame_ms=args.frame_ms,
        min_segment_ms=args.min_segment_ms,
        min_silence_ms=args.min_silence_ms,
        margin_db=args.margin_db,
        hysteresis_db=args.hysteresis_db,
        noise_percentile=args.noise_percentile,
    )
    result = detect(audio, params)
    conn = connect(args.db)
    try:
        task_id = save_task(conn, args.input, params, result, audio.channels, audio.sample_width_bytes)
    finally:
        conn.close()

    print(f"task_id={task_id}")
    print(f"input={args.input}")
    print(f"sample_rate={audio.sample_rate} channels={audio.channels} bit_depth={audio.sample_width_bytes * 8}")
    print(
        f"noise_floor_rms={result.noise_floor_rms:.6e} "
        f"enter_threshold_rms={result.enter_threshold_rms:.6e} "
        f"exit_threshold_rms={result.exit_threshold_rms:.6e}"
    )
    print(f"frame_count={result.frame_count}")
    print(f"segment_count={len(result.segments)}")
    print(f"silence_count={len(result.silences)}")
    for i, region in enumerate(result.segments):
        print(
            f"segment[{i}] start_sample={region.start_sample} end_sample={region.end_sample} "
            f"length_samples={region.length_samples} "
            f"start_seconds={region.start_sample / audio.sample_rate:.3f} "
            f"end_seconds={region.end_sample / audio.sample_rate:.3f}"
        )
    silence_seconds = sum(r.length_samples for r in result.silences) / audio.sample_rate
    print(f"silence_total_seconds={silence_seconds:.3f}")
    return 0


def _print_summary(task) -> None:
    print(f"task_id={task.task_id} input={task.input_path}")
    print(
        f"sample_rate={task.sample_rate} channels={task.channels} "
        f"bit_depth={task.sample_width_bytes * 8} created_at={task.created_at}"
    )
    print(f"segment_count={task.segment_count} silence_count={task.silence_count}")
    for i, region in enumerate(task.segments):
        print(
            f"segment[{i}] start_sample={region.start_sample} end_sample={region.end_sample} "
            f"length_samples={region.length_samples}"
        )
    print(f"silence_total_seconds={task.silence_total_seconds():.3f}")
    print(f"silence_total_samples={task.silence_total_samples()}")


def _run_summary(args: argparse.Namespace) -> int:
    conn = connect(args.db)
    try:
        if args.latest:
            task_id = latest_task_id(conn)
            if task_id is None:
                print("error: database contains no tasks", file=sys.stderr)
                return 2
            ids = [task_id]
        elif args.all:
            ids = list_task_ids(conn)
            if not ids:
                print("error: database contains no tasks", file=sys.stderr)
                return 2
        else:
            ids = [args.task_id]
        tasks = [load_task(conn, task_id) for task_id in ids]
    finally:
        conn.close()

    for task in tasks:
        if task is None:
            print(f"error: task id {ids[0]} does not exist", file=sys.stderr)
            return 2
        _print_summary(task)
    return 0


def _run_export(args: argparse.Namespace) -> int:
    conn = connect(args.db)
    try:
        if args.all:
            task_ids = list_task_ids(conn)
            if not task_ids:
                print("error: database contains no tasks", file=sys.stderr)
                return 2
        else:
            if not args.task_ids:
                print("error: export requires at least one --task-id or --all", file=sys.stderr)
                return 2
            task_ids = args.task_ids
        try:
            lines = export_tasks(conn, task_ids, args.out)
        except KeyError as exc:
            print(f"error: {exc.args[0]}", file=sys.stderr)
            return 2
    finally:
        conn.close()
    print(f"exported_tasks={len(task_ids)} exported_lines={lines} out={args.out}")
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv)
    try:
        if args.command == "analyze":
            return _run_analyze(args)
        if args.command == "summary":
            return _run_summary(args)
        if args.command == "export":
            return _run_export(args)
        parser.error(f"unknown command {args.command!r}")
    except (WavFormatError, FileNotFoundError, IsADirectoryError, ValueError) as exc:
        # Input/validation rejections are usage-level failures -> exit code 2.
        print(f"error: {exc}", file=sys.stderr)
        return 2
    except Exception as exc:  # pragma: no cover - defensive: internal failures -> 1
        print(f"internal error: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
