"""Terminal entry point for audioslice.

Exit codes (part of the stable CLI contract):

* 0 — every input processed successfully;
* 1 — parameter or input error (bad arguments, missing/unreadable input);
* 2 — batch completed but at least one file failed;
* 3 — SQLite could not be read or written.
"""

from __future__ import annotations

import argparse
import json
import sys
from dataclasses import dataclass

from .batch import BatchSummary, collect_wav_paths, run_batch
from .detector import DetectorConfig
from .errors import AudioSliceError, ConfigError, StoreError
from .store import Store

EXIT_OK = 0
EXIT_INPUT_ERROR = 1
EXIT_BATCH_FAILURE = 2
EXIT_STORE_FAILURE = 3


@dataclass(frozen=True)
class CliOptions:
    inputs: list[str]
    db_path: str | None
    window_ms: float
    noise_margin_db: float
    hysteresis_db: float
    min_silence_ms: float
    min_segment_ms: float
    max_pop_gap_ms: float | None
    block_frames: int
    recursive: bool
    as_json: bool


def _positive(value: float, name: str) -> float:
    value = float(value)
    if value <= 0:
        raise ConfigError(f"{name} must be positive, got {value}")
    return value


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="audioslice",
        description="Detect silence and segment local PCM WAV recordings.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    analyze = sub.add_parser("analyze", help="analyze one or more WAV files / directories")
    analyze.add_argument("inputs", nargs="+", help="WAV files or directories to scan")
    analyze.add_argument("--db", help="SQLite cache path (default: audioslice.sqlite3)")
    analyze.add_argument("--no-db", action="store_true", help="run without writing any cache")
    analyze.add_argument("--window-ms", type=float, default=20.0)
    analyze.add_argument("--noise-margin-db", type=float, default=8.0)
    analyze.add_argument("--hysteresis-db", type=float, default=3.0)
    analyze.add_argument("--min-silence-ms", type=float, default=120.0)
    analyze.add_argument("--min-segment-ms", type=float, default=40.0)
    analyze.add_argument("--max-pop-gap-ms", type=float, default=None)
    analyze.add_argument("--block-frames", type=int, default=65536)
    analyze.add_argument("--no-recursive", action="store_true",
                         help="do not descend into input directories")
    analyze.add_argument("--json", action="store_true", help="emit machine-readable JSON")
    return parser


def parse_options(argv: list[str]) -> CliOptions:
    args = build_parser().parse_args(argv)
    window_ms = _positive(args.window_ms, "window-ms")
    _positive(args.noise_margin_db, "noise-margin-db")
    if args.hysteresis_db < 0:
        raise ConfigError("hysteresis-db must not be negative")
    _positive(args.min_silence_ms, "min-silence-ms")
    _positive(args.min_segment_ms, "min-segment-ms")
    if args.max_pop_gap_ms is not None:
        _positive(args.max_pop_gap_ms, "max-pop-gap-ms")
    if args.block_frames <= 0:
        raise ConfigError("block-frames must be positive")
    return CliOptions(
        inputs=args.inputs,
        db_path=None if args.no_db else (args.db or "audioslice.sqlite3"),
        window_ms=window_ms,
        noise_margin_db=args.noise_margin_db,
        hysteresis_db=args.hysteresis_db,
        min_silence_ms=args.min_silence_ms,
        min_segment_ms=args.min_segment_ms,
        max_pop_gap_ms=args.max_pop_gap_ms,
        block_frames=args.block_frames,
        recursive=not args.no_recursive,
        as_json=args.json,
    )


def _base_config(options: CliOptions) -> DetectorConfig:
    # Sample rate is per-file; this value is replaced from each WAV header.
    return DetectorConfig(
        sample_rate=48000,
        window_ms=options.window_ms,
        noise_margin_db=options.noise_margin_db,
        hysteresis_db=options.hysteresis_db,
        min_silence_ms=options.min_silence_ms,
        min_segment_ms=options.min_segment_ms,
        max_pop_gap_ms=options.max_pop_gap_ms,
        block_frames=options.block_frames,
    )


def _json_payload(summary: BatchSummary) -> dict[str, object]:
    payload = summary.as_dict()
    successful = [o for o in summary.outcomes if o.result is not None]
    if len(successful) == 1:
        payload["segments"] = [s.as_dict() for s in successful[0].result.segments]
    else:
        payload["segments"] = [
            {**s.as_dict(), "path": o.path}
            for o in successful
            for s in o.result.segments
        ]
    return payload


def render_human(summary: BatchSummary) -> str:
    lines: list[str] = []
    for outcome in summary.outcomes:
        if outcome.result is None:
            lines.append(f"{outcome.status.upper():7} {outcome.path}"
                         + (f"  ({outcome.reason})" if outcome.reason else ""))
            continue
        result = outcome.result
        lines.append(
            f"{outcome.status.upper():7} {outcome.path}  "
            f"segments={len(result.segments)} "
            f"silence_total_seconds={result.silence_total_seconds:.3f}"
        )
        for segment in result.segments:
            lines.append(f"        segment [{segment.start_sample}, {segment.end_sample})")
    lines.append(
        f"files_ok={summary.files_ok} files_failed={summary.files_failed} "
        f"files_skipped={summary.files_skipped} segment_count={summary.total_segment_count} "
        f"silence_total_seconds={summary.total_silence_seconds}"
    )
    return "\n".join(lines)


def main(argv: list[str] | None = None) -> int:
    argv = sys.argv[1:] if argv is None else argv
    try:
        options = parse_options(argv)
        paths = collect_wav_paths(options.inputs, recursive=options.recursive)
        cfg = _base_config(options)
        store = Store(options.db_path) if options.db_path else None
        try:
            summary = run_batch(paths, cfg, store)
        finally:
            if store is not None:
                store.close()
    except StoreError as exc:
        print(f"database error: {exc.path}: {exc.reason}", file=sys.stderr)
        return EXIT_STORE_FAILURE
    except AudioSliceError as exc:
        print(f"input error: {exc.path}: {exc.reason}", file=sys.stderr)
        return EXIT_INPUT_ERROR
    except KeyboardInterrupt:
        print("interrupted", file=sys.stderr)
        return EXIT_INPUT_ERROR

    if options.as_json:
        print(json.dumps(_json_payload(summary), indent=2, sort_keys=True))
    else:
        print(render_human(summary))
    return EXIT_BATCH_FAILURE if summary.files_failed else EXIT_OK


if __name__ == "__main__":
    raise SystemExit(main())
