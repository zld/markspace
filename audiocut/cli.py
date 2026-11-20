"""Command-line interface.

Exit codes: 0 = success, 1 = file failures or storage error,
2 = parameter/usage error, 130 = interrupted (SIGINT).
"""
from __future__ import annotations

import argparse
import json
import os
import sys

from . import (
    DEFAULT_FRAME_MS,
    DEFAULT_HOP_MS,
    DEFAULT_MIN_SILENCE_MS,
    DEFAULT_NOISE_MARGIN_DB,
    DEFAULT_SILENCE_DBFS,
    DetectorParams,
    ParamsError,
)
from .batch import run_batch, scan_wavs
from .detector import detect_file
from .exporter import export_jsonl, resolve_run_id
from .samplegen import generate
from .store import Store, StoreError
from .wav_reader import WavError

EXIT_OK = 0
EXIT_FAILURE = 1
EXIT_PARAMS = 2
EXIT_INTERRUPTED = 130


def _add_detector_args(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--frame-ms", type=float, default=DEFAULT_FRAME_MS,
                        help="analysis window length in ms")
    parser.add_argument("--hop-ms", type=float, default=DEFAULT_HOP_MS,
                        help="analysis hop in ms (must be <= frame-ms)")
    parser.add_argument("--silence-dbfs", type=float, default=DEFAULT_SILENCE_DBFS,
                        help="silence threshold in dBFS")
    parser.add_argument("--noise-margin-db", type=float,
                        default=DEFAULT_NOISE_MARGIN_DB,
                        help="margin above the estimated noise floor")
    parser.add_argument("--min-silence-ms", type=float,
                        default=DEFAULT_MIN_SILENCE_MS,
                        help="minimum continuous silence that forms a cut")


def _params_from(args: argparse.Namespace) -> DetectorParams:
    return DetectorParams(
        frame_ms=args.frame_ms,
        hop_ms=args.hop_ms,
        silence_dbfs=args.silence_dbfs,
        noise_margin_db=args.noise_margin_db,
        min_silence_ms=args.min_silence_ms,
    ).validate()


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="audiocut",
        description="Slice PCM WAV files into sounding clips at silence "
                    "boundaries; store and export the results locally.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    p_gen = sub.add_parser("samplegen", help="write deterministic sample WAVs")
    p_gen.add_argument("--out", required=True, help="output directory")

    p_batch = sub.add_parser("batch", help="process a directory of WAV files")
    p_batch.add_argument("--input", required=True, help="input directory")
    p_batch.add_argument("--db", required=True, help="SQLite database path")
    p_batch.add_argument("--workers", type=int, default=2,
                         help="concurrent decode/detect workers")
    p_batch.add_argument("--resume", type=int, metavar="RUN_ID",
                         help="resume an unfinished run")
    _add_detector_args(p_batch)

    p_export = sub.add_parser("export", help="export a run as JSONL")
    p_export.add_argument("--db", required=True, help="SQLite database path")
    p_export.add_argument("--run-id", default="latest",
                          help="run id or 'latest' (default)")
    p_export.add_argument("--format", default="jsonl", choices=["jsonl"])
    p_export.add_argument("--out", help="output file (default: stdout)")

    p_detect = sub.add_parser("detect", help="detect segments of one WAV file")
    p_detect.add_argument("file", help="WAV file path")
    _add_detector_args(p_detect)
    return parser


def _cmd_samplegen(args: argparse.Namespace) -> int:
    paths = generate(args.out)
    for path in paths:
        print(f"wrote {path}")
    return EXIT_OK


def _cmd_batch(args: argparse.Namespace) -> int:
    params = _params_from(args)
    if args.workers < 1:
        raise ParamsError(f"workers must be >= 1, got {args.workers}")
    input_dir = os.path.abspath(args.input)
    try:
        scan_wavs(input_dir)  # fail fast on a missing/empty directory
    except (FileNotFoundError, ValueError) as exc:
        raise ParamsError(str(exc)) from exc

    store = Store(args.db)
    try:
        if args.resume is not None:
            run = store.get_run(args.resume)
            if run is None:
                raise StoreError(f"run {args.resume} not found")
            if run["status"] == "completed":
                raise ParamsError(f"run {args.resume} is already completed")
            if run["params_digest"] != params.digest():
                raise ParamsError(
                    "detector parameters differ from the run being resumed"
                )
            if os.path.abspath(run["input_dir"]) != input_dir:
                raise ParamsError(
                    "input directory differs from the run being resumed"
                )
            run_id = args.resume
        else:
            run_id = store.create_run(input_dir, params.summary_json(),
                                      params.digest())
        try:
            summary = run_batch(store, run_id, input_dir, params,
                                workers=args.workers,
                                resume=args.resume is not None)
        except KeyboardInterrupt:
            store.finish_run(run_id, status="interrupted")
            print(f"interrupted; resume with: batch --resume {run_id} ...",
                  file=sys.stderr)
            return EXIT_INTERRUPTED
        store.finish_run(run_id)
        for err in summary.errors:
            print(f"error: {err}", file=sys.stderr)
        print(summary.line())
        return EXIT_OK if summary.failed == 0 else EXIT_FAILURE
    finally:
        store.close()


def _cmd_export(args: argparse.Namespace) -> int:
    store = Store(args.db)
    try:
        resolve_run_id(store, args.run_id)  # validate before opening --out
        if args.out:
            with open(args.out, "w", encoding="utf-8") as fh:
                count = export_jsonl(store, args.run_id, fh)
        else:
            count = export_jsonl(store, args.run_id, sys.stdout)
        print(f"exported {count} file(s)", file=sys.stderr)
        return EXIT_OK
    finally:
        store.close()


def _cmd_detect(args: argparse.Namespace) -> int:
    params = _params_from(args)
    try:
        result = detect_file(args.file, params)
    except WavError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_FAILURE
    obj = {
        "path": args.file,
        "status": "ok",
        "sample_rate": result.sample_rate,
        "channels": result.channels,
        "sample_frames": result.sample_frames,
        "clip_count": result.clip_count,
        "segments": [
            {"start_sample": s.start_sample, "end_sample": s.end_sample}
            for s in result.segments
        ],
        "silence_total_seconds": round(result.silence_total_seconds, 6),
        "threshold_dbfs": round(result.threshold_dbfs, 3),
    }
    print(json.dumps(obj, sort_keys=True))
    return EXIT_OK


def main(argv=None) -> int:
    args = build_parser().parse_args(argv)
    try:
        if args.command == "samplegen":
            return _cmd_samplegen(args)
        if args.command == "batch":
            return _cmd_batch(args)
        if args.command == "export":
            return _cmd_export(args)
        if args.command == "detect":
            return _cmd_detect(args)
    except ParamsError as exc:
        print(f"parameter error: {exc}", file=sys.stderr)
        return EXIT_PARAMS
    except StoreError as exc:
        print(f"storage error: {exc}", file=sys.stderr)
        return EXIT_FAILURE
    except KeyboardInterrupt:
        return EXIT_INTERRUPTED
    return EXIT_PARAMS  # pragma: no cover - argparse enforces a command
