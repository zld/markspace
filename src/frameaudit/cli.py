"""Command-line interface and exit-code handling.

Exit codes: 0 ok, 1 media failure, 2 parameter error, 3 output error.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from . import EXIT_MEDIA, EXIT_OK, EXIT_OUTPUT, EXIT_USAGE
from .errors import FrameAuditError
from .fixture import generate_fixture
from .order import DEFAULT_REORDER_LIMIT
from .pipeline import process_file
from .report import format_text_summary, write_frames_jsonl

VIDEO_SUFFIXES = {".mp4", ".mkv", ".mov", ".m4v", ".avi", ".webm", ".ts", ".nut"}


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="frameaudit",
        description="Offline decode-frame audit and PTS repair tool.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    fx = sub.add_parser("fixture", help="generate a synthetic faulty clip")
    fx.add_argument("--out", required=True, help="output media path (.mp4)")
    fx.add_argument("--frames", type=int, default=120)
    fx.add_argument("--fps", type=int, default=30)
    fx.add_argument("--faults", type=int, default=4)
    fx.add_argument("--overwrite", action="store_true")

    scan = sub.add_parser("scan", help="audit one file or a directory")
    scan.add_argument("--input", required=True, help="media file or directory")
    scan.add_argument("--stream", type=int, default=0, help="video stream index")
    scan.add_argument("--fps", type=float, default=None, help="explicit frame rate")
    scan.add_argument(
        "--reorder-limit", type=int, default=DEFAULT_REORDER_LIMIT
    )
    scan.add_argument(
        "--format", choices=("json", "text"), default="json"
    )
    scan.add_argument("--frames-out", default=None, help="per-frame JSONL path")
    scan.add_argument(
        "--faults-manifest",
        default=None,
        help="fixture manifest applying PTS faults before repair",
    )
    scan.add_argument("--overwrite", action="store_true")
    return parser


def _iter_inputs(input_path: Path) -> list[Path]:
    if input_path.is_file():
        return [input_path]
    if not input_path.is_dir():
        raise FrameAuditError(f"input path not found: {input_path}")
    entries = sorted(
        p
        for p in input_path.iterdir()
        if p.is_file() and p.suffix.lower() in VIDEO_SUFFIXES
    )
    if not entries:
        raise FrameAuditError(f"no supported media files in directory: {input_path}")
    return entries


def _run_fixture(args: argparse.Namespace) -> int:
    media, manifest = generate_fixture(
        args.out,
        frame_count=args.frames,
        fps=args.fps,
        faults=args.faults,
        overwrite=args.overwrite,
    )
    print(json.dumps({"media": str(media), "manifest": str(manifest)}, indent=2))
    return EXIT_OK


def _run_scan(args: argparse.Namespace) -> int:
    input_path = Path(args.input)
    files = _iter_inputs(input_path)
    batch = len(files) > 1

    if args.frames_out is not None and batch:
        # One JSONL path cannot represent several files without collision.
        raise FrameAuditError(
            "--frames-out is only valid for a single input file; "
            "run directory batches without it or scan files individually"
        )

    summaries: list[dict] = []
    failures: list[dict] = []
    for file_path in files:
        try:
            result = process_file(
                file_path,
                stream_index=args.stream,
                explicit_fps=args.fps,
                reorder_limit=args.reorder_limit,
                faults_manifest=args.faults_manifest,
            )
            if args.frames_out is not None:
                write_frames_jsonl(
                    args.frames_out, result.repair, overwrite=args.overwrite
                )
            summaries.append(result.summary)
            if not batch and args.format == "text":
                print(format_text_summary(result.summary))
        except FrameAuditError as exc:
            # Per-file isolation: record and continue with the next file.
            failures.append(
                {"source": str(file_path), "error": str(exc),
                 "exit_code": exc.exit_code}
            )
            print(f"error: {file_path}: {exc}", file=sys.stderr)

    if not batch and args.format == "json":
        payload = summaries[0] if summaries else {"error": failures[0]}
        print(json.dumps(payload, indent=2))
    elif batch:
        print(json.dumps(
            {"results": summaries, "failures": failures,
             "succeeded": len(summaries), "failed": len(failures)},
            indent=2,
        ))

    if not summaries:
        # Every file failed: surface the most specific exit code.
        if failures:
            return max(item["exit_code"] for item in failures)
        return EXIT_MEDIA
    return EXIT_OK


def main(argv: list[str] | None = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv)
    try:
        if args.command == "fixture":
            return _run_fixture(args)
        return _run_scan(args)
    except FrameAuditError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return exc.exit_code
    except KeyboardInterrupt:
        print("interrupted", file=sys.stderr)
        return EXIT_MEDIA


if __name__ == "__main__":
    raise SystemExit(main())
