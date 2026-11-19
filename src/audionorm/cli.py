"""Terminal-only CLI: argument parsing, validation, exit codes.

Exit codes: 0 = batch completed with no failures, 1 = at least one
file failed, 2 = argument or directory configuration error.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from .batch import OVERWRITE_POLICIES, Config, run_batch
from .report import write_report
from .wavio import MAX_RATE, MIN_RATE

EXIT_OK = 0
EXIT_FAILURES = 1
EXIT_CONFIG = 2


def _parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="audionorm",
                                description="Batch WAV resample + loudness normalization")
    sub = p.add_subparsers(dest="command", required=True)

    run = sub.add_parser("run", help="process a directory of WAV files")
    run.add_argument("--input-dir", required=True, type=Path)
    run.add_argument("--output-dir", required=True, type=Path)
    run.add_argument("--report", type=Path, default=None,
                     help="report path (default: <output-dir>/report.json)")
    _common(run)

    demo = sub.add_parser("demo", help="generate fixtures and run a batch")
    demo.add_argument("--workdir", required=True, type=Path)
    _common(demo)
    return p


def _common(p: argparse.ArgumentParser) -> None:
    p.add_argument("--target-rate", type=int, default=48000)
    p.add_argument("--target-lufs", type=float, default=-18.0)
    p.add_argument("--jobs", type=int, default=1)
    p.add_argument("--overwrite", choices=OVERWRITE_POLICIES, default="skip")


def _validate(args: argparse.Namespace) -> str | None:
    if not (MIN_RATE <= args.target_rate <= MAX_RATE):
        return f"target rate {args.target_rate} out of range [{MIN_RATE}, {MAX_RATE}]"
    if args.jobs < 1:
        return "jobs must be >= 1"
    if args.command == "run":
        if not args.input_dir.is_dir():
            return f"input directory does not exist: {args.input_dir}"
        in_res = args.input_dir.resolve()
        out_res = args.output_dir.resolve()
        if out_res == in_res or in_res in out_res.parents:
            return "output directory must not equal or be inside the input directory"
    return None


def _execute(input_dir: Path, output_dir: Path, report_path: Path,
             cfg: Config) -> int:
    rep = run_batch(input_dir, output_dir, cfg)
    write_report(report_path, rep)
    json.dump({"summary": rep["summary"], "report_path": str(report_path),
               "files": rep["files"]}, sys.stdout, indent=2)
    sys.stdout.write("\n")
    return EXIT_FAILURES if rep["summary"]["failed"] else EXIT_OK


def main(argv: list[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    error = _validate(args)
    if error:
        print(f"configuration error: {error}", file=sys.stderr)
        return EXIT_CONFIG
    cfg = Config(target_rate=args.target_rate, target_lufs=args.target_lufs,
                 jobs=args.jobs, overwrite=args.overwrite)
    if args.command == "demo":
        from .fixtures import generate_all
        workdir = args.workdir.resolve()
        input_dir, output_dir = workdir / "input", workdir / "output"
        written = generate_all(input_dir)
        print(f"generated {len(written)} fixture files under {input_dir}")
        cfg = Config(target_rate=cfg.target_rate, target_lufs=cfg.target_lufs,
                     jobs=cfg.jobs, overwrite="replace")
        return _execute(input_dir, output_dir, workdir / "report.json", cfg)
    report_path = args.report or (args.output_dir / "report.json")
    return _execute(args.input_dir.resolve(), args.output_dir.resolve(),
                    report_path, cfg)


if __name__ == "__main__":
    raise SystemExit(main())
