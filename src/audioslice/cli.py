"""Command line interface for the audioslice batch pipeline.

Exit codes::

    0 - processing and export succeeded (possibly with skipped duplicates)
    1 - one or more input files failed to process
    2 - invalid arguments or input path
    3 - database or export failure

Usage::

    python -m audioslice.cli process <input-dir> --db out.sqlite3 \
        [--export results.jsonl] [--frame-ms N] ... [--continue-on-error]
"""

from __future__ import annotations

import argparse
import sys
from dataclasses import dataclass
from pathlib import Path

from .batch import BatchProcessor, discover_wavs
from .detector import DetectorConfig
from .export import ExportError, export_jsonl, render_summary
from .store import Store, StoreError

EXIT_OK = 0
EXIT_FILE_FAILURE = 1
EXIT_USAGE = 2
EXIT_IO_FAILURE = 3


@dataclass(frozen=True)
class CliOptions:
    input_dir: str
    db_path: str
    export_path: str | None
    config: DetectorConfig
    continue_on_error: bool


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="audioslice",
        description="Detect activity and silence regions in WAV soundscapes.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("process", help="process a directory of WAV files")
    p.add_argument("input_dir", help="directory containing .wav files (searched recursively)")
    p.add_argument("--db", required=True, help="sqlite3 database path to write results to")
    p.add_argument("--export", help="optional JSONL export path")
    p.add_argument("--frame-ms", type=float, default=10.0)
    p.add_argument("--noise-window-ms", type=float, default=500.0)
    p.add_argument("--noise-quantile", type=float, default=0.25)
    p.add_argument("--high-factor", type=float, default=3.0)
    p.add_argument("--low-factor", type=float, default=1.8)
    p.add_argument("--slope-factor", type=float, default=2.0)
    p.add_argument("--abs-activation-rms", type=float, default=40.0)
    p.add_argument("--min-event-ms", type=float, default=40.0)
    p.add_argument("--merge-gap-ms", type=float, default=70.0)
    p.add_argument("--max-slope-frames", type=int, default=12)
    p.add_argument("--continue-on-error", action="store_true",
                   help="keep processing remaining files when one fails")
    return parser


def parse_options(argv: list[str]) -> CliOptions:
    parser = build_parser()
    ns = parser.parse_args(argv)
    if ns.command != "process":  # pragma: no cover - argparse enforces choices
        parser.error("unknown command")

    config = DetectorConfig(
        frame_ms=ns.frame_ms,
        noise_window_ms=ns.noise_window_ms,
        noise_quantile=ns.noise_quantile,
        high_factor=ns.high_factor,
        low_factor=ns.low_factor,
        slope_factor=ns.slope_factor,
        abs_activation_rms=ns.abs_activation_rms,
        min_event_ms=ns.min_event_ms,
        merge_gap_ms=ns.merge_gap_ms,
        max_slope_frames=ns.max_slope_frames,
    )
    # Validate parameters before touching the filesystem or the database.
    config.validate()
    _validate_paths(ns.input_dir, ns.db, ns.export)
    return CliOptions(
        input_dir=ns.input_dir, db_path=ns.db, export_path=ns.export,
        config=config, continue_on_error=ns.continue_on_error,
    )


def _validate_paths(input_dir: str, db_path: str, export_path: str | None) -> None:
    root = Path(input_dir)
    if not root.exists() or not root.is_dir():
        raise UsageError(f"input directory does not exist or is not a directory: {input_dir}")
    for label, path in (("database", db_path),):
        parent = Path(path).resolve(strict=False).parent
        if not parent.is_dir() or not _writable(parent):
            raise UsageError(f"{label} directory is not writable: {parent}")
    if export_path is not None:
        target = Path(export_path).resolve(strict=False)
        if target.parent.exists() and not _writable(target.parent):
            raise UsageError(f"export directory is not writable: {target.parent}")


def _writable(path: Path) -> bool:
    import os
    return os.access(path, os.W_OK)


class UsageError(Exception):
    """Raised for invalid CLI arguments/inputs; maps to exit code 2."""


def run(options: CliOptions) -> int:
    try:
        paths = discover_wavs(options.input_dir)
    except (FileNotFoundError, NotADirectoryError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_USAGE

    if not paths:
        print(f"error: no .wav files found under {options.input_dir}", file=sys.stderr)
        return EXIT_USAGE

    try:
        store = Store(options.db_path)
    except StoreError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_IO_FAILURE

    with store:
        store.begin()
        processor = BatchProcessor(
            options.config, store, continue_on_error=options.continue_on_error,
            progress=lambda msg: print(msg),
        )
        try:
            result = processor.run(paths)
        except StoreError as exc:
            print(f"error: database write failed, batch rolled back: {exc}", file=sys.stderr)
            return EXIT_IO_FAILURE

        exports = result.to_exports()
        if options.export_path:
            try:
                export_jsonl(options.export_path, exports, paths)
            except ExportError as exc:
                print(f"error: {exc}", file=sys.stderr)
                return EXIT_IO_FAILURE

    print(render_summary(exports, options.db_path, options.export_path))
    return EXIT_FILE_FAILURE if result.failed else EXIT_OK


def main(argv: list[str] | None = None) -> int:
    argv = sys.argv[1:] if argv is None else argv
    try:
        options = parse_options(argv)
    except UsageError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_USAGE
    except argparse.ArgumentError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_USAGE
    except (ValueError, OverflowError) as exc:
        # Illegal numeric detector parameters surface from DetectorConfig.
        print(f"error: invalid parameter: {exc}", file=sys.stderr)
        return EXIT_USAGE
    return run(options)


if __name__ == "__main__":
    raise SystemExit(main())
