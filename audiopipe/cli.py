"""Command line interface for the audiopipe batch tool."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from . import EXIT_FILE_FAILURE, EXIT_OK, EXIT_USAGE, EXIT_VERIFY_MISMATCH
from .discovery import DiscoveryError, discover_sources
from .fixtures import generate_fixtures
from .pipeline import BatchParams, run_batch
from .report import verify_report, write_report


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="audiopipe",
        description="Batch polyphase resampling and BS.1770-4 loudness "
                    "normalization.")
    sub = parser.add_subparsers(dest="command", required=True)

    p_fix = sub.add_parser("fixtures", help="synthesize demo audio files")
    p_fix.add_argument("--dir", required=True, type=Path,
                       help="output directory for synthetic fixtures")
    p_fix.add_argument("--seed", type=int, default=14630)

    p_batch = sub.add_parser("batch", help="run a resampling/normalization "
                                            "batch")
    p_batch.add_argument("--input", required=True, type=Path)
    p_batch.add_argument("--output", required=True, type=Path)
    p_batch.add_argument("--rate", required=True, type=int,
                         help="target sample rate in Hz")
    p_batch.add_argument("--target-lufs", type=float, default=None,
                         dest="target_lufs")
    p_batch.add_argument("--chunk-frames", type=int, default=4096,
                         dest="chunk_frames")
    p_batch.add_argument("--taps", type=int, default=32)
    p_batch.add_argument("--workers", type=int, default=1)
    p_batch.add_argument("--report", required=True, type=Path)

    p_verify = sub.add_parser("verify", help="re-verify a batch report")
    p_verify.add_argument("--report", required=True, type=Path)
    return parser


def _validate_batch_args(args: argparse.Namespace) -> None:
    if args.rate <= 0:
        raise ValueError("--rate must be positive")
    if args.chunk_frames <= 0:
        raise ValueError("--chunk-frames must be positive")
    if args.taps <= 0:
        raise ValueError("--taps must be positive")
    if args.workers <= 0:
        raise ValueError("--workers must be positive")
    if args.target_lufs is not None and not (
            -100.0 <= args.target_lufs <= 0.0):
        raise ValueError("--target-lufs must lie in [-100, 0] LUFS")


def _run_fixtures(args: argparse.Namespace) -> int:
    paths = generate_fixtures(args.dir, seed=args.seed)
    for path in paths:
        print(json.dumps({"event": "fixture", "path": str(path)},
                         sort_keys=True))
    print(json.dumps({"event": "fixtures_done", "count": len(paths)},
                     sort_keys=True))
    return EXIT_OK


def _run_batch(args: argparse.Namespace) -> int:
    _validate_batch_args(args)
    try:
        sources, pre_errors = discover_sources(args.input, args.output)
    except DiscoveryError as exc:
        print(json.dumps({"event": "usage_error", "error": str(exc)},
                         sort_keys=True))
        return EXIT_USAGE
    params = BatchParams(
        target_rate=args.rate, target_lufs=args.target_lufs,
        chunk_frames=args.chunk_frames, taps=args.taps,
        workers=args.workers)
    records = run_batch(sources, pre_errors, params)
    for record in records:
        summary = {k: record.get(k) for k in (
            "source", "status", "output_sample_rate", "output_channels",
            "rms_before", "rms_after", "lufs_before", "lufs_after",
            "clipped_samples_before", "clipped_samples_after", "error")
            if k in record}
        print(json.dumps(summary, sort_keys=True))
    ok = [r for r in records if r.get("status") == "ok"]
    failed = [r for r in records if r.get("status") == "error"]
    report = {
        "tool": "audiopipe",
        "version": 1,
        "parameters": {
            "target_rate": args.rate,
            "target_lufs": args.target_lufs,
            "chunk_frames": args.chunk_frames,
            "taps": args.taps,
            "workers": args.workers,
            "input_dir": str(args.input),
            "output_dir": str(args.output),
        },
        "summary": {"total": len(records), "ok": len(ok),
                    "failed": len(failed)},
        "files": records,
    }
    write_report(report, args.report)
    print(json.dumps({"event": "report_written", "path": str(args.report),
                      "ok": len(ok), "failed": len(failed)}, sort_keys=True))
    return EXIT_FILE_FAILURE if failed else EXIT_OK


def _run_verify(args: argparse.Namespace) -> int:
    if not args.report.is_file():
        print(json.dumps({"event": "usage_error",
                          "error": f"report not found: {args.report}"},
                         sort_keys=True))
        return EXIT_USAGE
    ok, discrepancies = verify_report(args.report)
    for item in discrepancies:
        print(json.dumps({"event": "mismatch", **item}, sort_keys=True))
    print(json.dumps({"event": "verify_done", "ok": ok,
                      "mismatches": len(discrepancies)}, sort_keys=True))
    return EXIT_OK if ok else EXIT_VERIFY_MISMATCH


def main(argv: list[str] | None = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv)
    try:
        if args.command == "fixtures":
            return _run_fixtures(args)
        if args.command == "batch":
            return _run_batch(args)
        if args.command == "verify":
            return _run_verify(args)
    except (ValueError, OSError) as exc:
        print(json.dumps({"event": "usage_error",
                          "error": f"{type(exc).__name__}: {exc}"},
                         sort_keys=True))
        return EXIT_USAGE
    parser.error(f"unknown command: {args.command}")
    return EXIT_USAGE


if __name__ == "__main__":
    raise SystemExit(main())
