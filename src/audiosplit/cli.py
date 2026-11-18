"""Command-line interface for audiosplit."""

from __future__ import annotations

import argparse
import sys
from pathlib import Path
from typing import Sequence

from .batch import BatchResult, discover_inputs, new_batch_id, process_file, run_batch
from .config import ConfigError, DetectionConfig
from .store import Store, StoreError


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="audiosplit",
        description="Deterministic local WAV event segmentation.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    def add_detection_args(target: argparse.ArgumentParser) -> None:
        target.add_argument("--frame-ms", type=float, default=10.0)
        target.add_argument("--noise-percentile", type=float, default=20.0)
        target.add_argument("--noise-window-ms", type=float, default=1000.0)
        target.add_argument("--margin-db", type=float, default=8.0)
        target.add_argument("--hysteresis-db", type=float, default=4.0)
        target.add_argument("--onset-frames", type=int, default=2)
        target.add_argument("--release-frames", type=int, default=5)
        target.add_argument("--merge-ms", type=float, default=35.0)
        target.add_argument("--min-segment-ms", type=float, default=25.0)
        target.add_argument("--fade-ms", type=float, default=5.0)
        target.add_argument("--min-peak-dbfs", type=float, default=-70.0)
        target.add_argument("--max-file-mb", type=float, default=512.0)
        target.add_argument("--min-sample-rate", type=int, default=8000)
        target.add_argument("--max-sample-rate", type=int, default=384000)

    def add_common(target: argparse.ArgumentParser) -> None:
        target.add_argument("--input", required=True, type=Path)
        target.add_argument("--db", required=True, type=Path)
        target.add_argument("--export", type=Path, default=None)
        target.add_argument(
            "--on-duplicate",
            choices=("skip", "new"),
            default="skip",
            help="skip an identical input+config, or create a new run",
        )

    single = sub.add_parser("detect", help="process a single WAV file")
    add_common(single)
    add_detection_args(single)

    batch = sub.add_parser("batch", help="process a WAV file or directory tree")
    add_common(batch)
    add_detection_args(batch)

    return parser


def _config_from_args(args: argparse.Namespace) -> DetectionConfig:
    config = DetectionConfig(
        frame_ms=args.frame_ms,
        noise_percentile=args.noise_percentile,
        noise_window_ms=args.noise_window_ms,
        margin_db=args.margin_db,
        hysteresis_db=args.hysteresis_db,
        onset_frames=args.onset_frames,
        release_frames=args.release_frames,
        merge_ms=args.merge_ms,
        min_segment_ms=args.min_segment_ms,
        fade_ms=args.fade_ms,
        min_peak_dbfs=args.min_peak_dbfs,
        max_file_mb=args.max_file_mb,
        min_sample_rate=args.min_sample_rate,
        max_sample_rate=args.max_sample_rate,
    )
    config.validate()
    return config


def _print_result(result: BatchResult) -> None:
    print(f"files_ok {result.files_ok}")
    print(f"files_failed {result.files_failed}")
    if result.files_skipped:
        print(f"files_skipped {result.files_skipped}")
    print(f"segments_total {result.segments_total}")
    for item in result.results:
        if item.status == "failed":
            print(f"ERROR {item.relative_path}: {item.error}")
            continue
        print(
            f"file {item.relative_path} run_id={item.run_id} "
            f"segments={len(item.segments)}"
        )
        for index, (start, end) in enumerate(item.segments, start=1):
            print(f"  segment {index} start_sample={start} end_sample={end}")
    print(f"silence_total_seconds {result.silence_seconds:.6f}")
    print(f"duration_total_seconds {result.duration_seconds:.6f}")


def main(argv: Sequence[str] | None = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv)
    try:
        config = _config_from_args(args)
        inputs = discover_inputs(args.input)
    except ConfigError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    if not inputs:
        print("error: no WAV inputs found", file=sys.stderr)
        return 2

    store = Store(args.db)
    try:
        store.open()
    except StoreError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2

    try:
        if args.command == "detect":
            if len(inputs) != 1:
                print("error: detect expects exactly one WAV file", file=sys.stderr)
                return 2
            batch_id = new_batch_id()
            index_entries: list[dict[str, object]] = []
            single = process_file(
                inputs[0],
                store=store,
                config=config,
                batch_id=batch_id,
                export_dir=args.export,
                on_duplicate=args.on_duplicate,
                index_entries=index_entries,
            )
            result = BatchResult()
            result.results.append(single)
            if single.status == "failed":
                result.files_failed = 1
                result.failures.append((single.relative_path, single.error))
            else:
                result.files_ok = 1
                if single.status == "skipped":
                    result.files_skipped = 1
                result.segments_total = len(single.segments)
                if single.sample_rate:
                    result.silence_seconds = single.silence_samples / single.sample_rate
                    result.duration_seconds = single.total_samples / single.sample_rate
        else:
            result = run_batch(
                args.input,
                store=store,
                config=config,
                export_dir=args.export,
                on_duplicate=args.on_duplicate,
            )
    except (ConfigError, StoreError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    except KeyboardInterrupt:
        print("interrupted", file=sys.stderr)
        return 2
    finally:
        store.close()

    _print_result(result)
    return result.exit_code


if __name__ == "__main__":
    raise SystemExit(main())
