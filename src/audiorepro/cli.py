"""Command line interface: arguments, terminal output and exit codes.

Exit codes
    0  every file processed (and verified) successfully
    2  invalid command line or parameters
    3  at least one input file failed processing, or verification found
       differences beyond tolerance
    4  the report, output, or verification artifact could not be
       written or read
"""

from __future__ import annotations

import argparse
import json
import math
import sys
from pathlib import Path
from typing import Any, Sequence

from . import __version__
from .errors import AudioReproError, OutputError, UsageError, VerifyError
from .fixtures import generate
from .params import PipelineParams
from .pipeline import run_pipeline
from .report import write_json_atomic
from .resample import ResamplerConfig
from .verify import verify as run_verify

EXIT_OK = 0
EXIT_USAGE = 2
EXIT_TASK_FAILED = 3
EXIT_WRITE_FAILED = 4


class _Parser(argparse.ArgumentParser):
    """argparse that reports usage problems with exit code 2, not SystemExit(0)."""

    def error(self, message: str) -> None:  # type: ignore[override]
        raise UsageError(message)


def build_parser() -> argparse.ArgumentParser:
    parser = _Parser(prog="audiorepro", description=__doc__.splitlines()[0])
    parser.add_argument("--version", action="version", version=f"audiorepro {__version__}")
    sub = parser.add_subparsers(dest="command", required=True, parser_class=_Parser)

    fixture = sub.add_parser("fixture", help="generate deterministic sample audio")
    fixture.add_argument("--out", type=Path, required=True, help="destination directory")
    fixture.add_argument("--seed", type=int, default=17, help="fixture seed (default: 17)")
    fixture.set_defaults(handler=_cmd_fixture)

    process = sub.add_parser(
        "process", help="resample and normalize a batch",
        description="Recursively process WAV/FLAC/AIFF files and write a JSON report.",
    )
    process.add_argument("--input", type=Path, required=True, help="source directory")
    process.add_argument("--output", type=Path, required=True, help="product directory")
    process.add_argument("--report", type=Path, required=True, help="JSON report path")
    process.add_argument("--rate", type=int, required=True, help="output sample rate (Hz)")
    process.add_argument(
        "--target-lufs", type=float, default=-23.0, help="integrated loudness target"
    )
    process.add_argument(
        "--peak-dbfs", type=float, default=-1.0, help="output sample-peak ceiling"
    )
    process.add_argument("--jobs", type=int, default=1, help="parallel workers (default: 1)")
    process.add_argument(
        "--resume", action="store_true", help="reuse unchanged products keyed by SHA-256"
    )
    process.add_argument(
        "--chunk-frames", type=int, default=16384, help="frames per working block"
    )
    process.set_defaults(handler=_cmd_process)

    verify = sub.add_parser(
        "verify", help="independently recompute a report",
        description="Re-read sources and products and compare every reported value.",
    )
    verify.add_argument("--input", type=Path, required=True, help="source directory")
    verify.add_argument("--output", type=Path, required=True, help="product directory")
    verify.add_argument("--report", type=Path, required=True, help="JSON report to verify")
    verify.add_argument(
        "--tolerance", type=float, default=0.15, help="allowed dB / LU difference"
    )
    verify.add_argument("--json-out", type=Path, default=None, help="optional result JSON")
    verify.set_defaults(handler=_cmd_verify)
    return parser


def _validate_process(args: argparse.Namespace) -> None:
    if not 8000 <= args.rate <= 192000:
        raise UsageError(f"--rate must be between 8000 and 192000, got {args.rate}")
    if not math.isfinite(args.target_lufs) or not -70.0 <= args.target_lufs <= 5.0:
        raise UsageError("--target-lufs must be a finite value between -70 and 5")
    if not math.isfinite(args.peak_dbfs) or not -20.0 <= args.peak_dbfs <= 0.0:
        raise UsageError("--peak-dbfs must be between -20 and 0")
    if args.jobs < 1:
        raise UsageError("--jobs must be at least 1")
    if args.chunk_frames < 256:
        raise UsageError("--chunk-frames must be at least 256")
    if not args.input.is_dir():
        raise UsageError(f"--input is not a directory: {args.input}")
    try:
        output = args.output.resolve()
        source = args.input.resolve()
    except OSError as exc:
        raise UsageError(f"cannot resolve input/output paths: {exc}") from exc
    if output == source or source in output.parents:
        # Otherwise the next run would rediscover its own products as input.
        raise UsageError("--output must not be the input directory or nested inside it")


def _cmd_fixture(args: argparse.Namespace) -> int:
    if args.seed < 0:
        raise UsageError("--seed must be non-negative")
    created = generate(args.out, args.seed)
    for path in created:
        print(f"wrote {path}")
    print(f"fixtures={len(created)} out={args.out} seed={args.seed}")
    return EXIT_OK


def _print_error(error: Any) -> None:
    print(f"  error: {error['path']} [{error['stage']}] {error['reason']}", file=sys.stderr)


def _print_summary(report: dict[str, Any]) -> None:
    summary = report["summary"]
    print(
        f"files={summary['files']} failed={summary['failed']} "
        f"sample_rate={summary['sample_rate']} "
        f"channels={json.dumps(summary['channels'], separators=(',', ':'))}"
    )
    print(f"resumed={summary['resumed']}")
    for item in report["files"]:
        print(
            f"{item['relative_path']}: {item['input_sample_rate']}Hz->{item['sample_rate']}Hz "
            f"{item['channels']}ch frames={item['frames_in']}->{item['frames_out']} "
            f"rms={_fmt(item['rms_before_dbfs'])}->{_fmt(item['rms_after_dbfs'])} dBFS "
            f"lufs={_fmt(item['lufs_before'])}->{_fmt(item['lufs_after'])} LUFS "
            f"gain={item['gain_db']:+.2f}dB "
            f"clip={item['clip_samples_before']}->{item['clip_samples_after']}"
        )


def _fmt(value: float | None) -> str:
    return "null" if value is None else f"{value:.2f}"


def _cmd_process(args: argparse.Namespace) -> int:
    _validate_process(args)
    params = PipelineParams(
        input_dir=args.input,
        output_dir=args.output,
        sample_rate=args.rate,
        target_lufs=args.target_lufs,
        peak_dbfs=args.peak_dbfs,
        jobs=args.jobs,
        resume=bool(args.resume),
        chunk_frames=args.chunk_frames,
        resampler=ResamplerConfig(),
    )
    result = run_pipeline(params, args.report)
    _print_summary(result.report)
    for error in result.report["errors"]:
        _print_error(error)
    print(f"report={args.report}")
    return result.exit_code


def _cmd_verify(args: argparse.Namespace) -> int:
    if args.tolerance < 0:
        raise UsageError("--tolerance must be non-negative")
    if not args.input.is_dir():
        raise UsageError(f"--input is not a directory: {args.input}")
    outcome = run_verify(args.input, args.output, args.report, args.tolerance)
    if args.json_out is not None:
        write_json_atomic(args.json_out, outcome)
    print(
        f"verified={outcome['files_checked']} "
        f"differences={outcome['files_with_differences']} "
        f"tolerance={outcome['tolerance']} ok={str(outcome['ok']).lower()}"
    )
    for item in outcome["files"]:
        for diff in item["diffs"]:
            print(
                f"  {item['relative_path']} {diff['field']}: "
                f"recorded={diff['recorded']} recomputed={diff['recomputed']}",
                file=sys.stderr,
            )
    return EXIT_OK if outcome["ok"] else EXIT_TASK_FAILED


def main(argv: Sequence[str] | None = None) -> int:
    parser = build_parser()
    try:
        args = parser.parse_args(argv)
        return int(args.handler(args))
    except UsageError as exc:
        print(f"audiorepro: {exc}", file=sys.stderr)
        print("run 'audiorepro --help' for usage", file=sys.stderr)
        return EXIT_USAGE
    except (OutputError, VerifyError) as exc:
        print(f"audiorepro: {exc}", file=sys.stderr)
        return EXIT_WRITE_FAILED
    except AudioReproError as exc:
        print(f"audiorepro: {exc}", file=sys.stderr)
        return EXIT_USAGE
    except KeyboardInterrupt:
        print("audiorepro: interrupted", file=sys.stderr)
        return EXIT_TASK_FAILED
    except OSError as exc:
        print(f"audiorepro: {exc}", file=sys.stderr)
        return EXIT_WRITE_FAILED


def entrypoint() -> None:  # pragma: no cover - console_script wrapper
    raise SystemExit(main())
