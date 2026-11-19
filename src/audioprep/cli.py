"""Command-line interface: process and verify subcommands.

Exit codes: 0 = whole batch succeeded / verification passed, 1 = some files
failed, 2 = argument or directory configuration error, 3 = verification
recomputation disagreed with the report.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from . import batch, loudness, report as report_mod, wavio

EXIT_OK = 0
EXIT_PARTIAL = 1
EXIT_CONFIG = 2
EXIT_MISMATCH = 3

_LINE_FIELDS = (
    "input_rate", "output_rate", "channels",
    "input_rms_dbfs", "output_rms_dbfs", "input_lufs", "output_lufs",
    "input_clipped_samples", "output_clipped_samples",
    "input_frames", "output_frames", "padded_frames", "status",
)


def _print_file_line(entry: dict) -> None:
    line = {k: entry.get(k) for k in _LINE_FIELDS}
    line["relpath"] = entry["relpath"]
    if entry["status"] != "ok":
        line["stage"] = entry["stage"]
        line["error_type"] = entry["error_type"]
        line["error"] = entry["error"]
    print(json.dumps(line, allow_nan=False), flush=True)


def cmd_process(args: argparse.Namespace) -> int:
    cfg = batch.BatchConfig(
        input_dir=Path(args.input), output_dir=Path(args.output),
        rate=args.rate, target_lufs=args.target_lufs,
        max_boost_db=args.max_boost_db, workers=args.workers,
        max_channels=args.max_channels, max_seconds=args.max_seconds)
    runner = batch.BatchRunner(cfg)
    try:
        entries, warnings = runner.run(on_file=_print_file_line)
    except batch.BatchError as exc:
        print(f"audioprep: {exc}", file=sys.stderr)
        return EXIT_CONFIG
    rep = report_mod.build_report(cfg.params_dict(), entries, warnings)
    try:
        report_mod.write_report_atomic(rep, args.report)
    except OSError as exc:
        print(f"audioprep: cannot write report: {exc}", file=sys.stderr)
        return EXIT_CONFIG
    for w in warnings:
        print(f"audioprep: warning: {w['reason']}: {w['path']}",
              file=sys.stderr)
    failed = rep["summary"]["failed"]
    print(json.dumps({"total": rep["summary"]["total"],
                      "succeeded": rep["summary"]["succeeded"],
                      "failed": failed}), flush=True)
    return EXIT_OK if failed == 0 else EXIT_PARTIAL


def _close(a, b, tol: float) -> bool:
    if a is None or b is None:
        return a is b
    return abs(a - b) <= tol


def _verify_file(entry: dict, out_path: Path, cfg: dict,
                 tol: float) -> list[dict]:
    """Recompute one output file and list field-level differences."""
    data, rate = wavio.read_wav(out_path, cfg["max_channels"],
                                cfg["max_seconds"])
    diffs = []

    def check(name, expected, actual, exact=False):
        ok = (expected == actual) if exact else _close(expected, actual, tol)
        if not ok:
            diffs.append({"field": name, "report": expected,
                          "recomputed": actual})

    check("output_rate", entry["output_rate"], rate, exact=True)
    check("channels", entry["channels"], data.shape[1], exact=True)
    check("output_frames", entry["output_frames"], data.shape[0], exact=True)
    m = loudness.measure(data, rate)
    check("output_rms_dbfs", entry["output_rms_dbfs"], m["rms_dbfs"])
    check("output_lufs", entry["output_lufs"], m["lufs"])
    check("output_true_peak_dbtp", entry["output_true_peak_dbtp"],
          m["true_peak_dbtp"])
    check("output_clipped_samples", entry["output_clipped_samples"],
          m["clipped_samples"], exact=True)
    return diffs


def cmd_verify(args: argparse.Namespace) -> int:
    try:
        with open(args.report, encoding="utf-8") as fh:
            rep = json.load(fh)
    except (OSError, json.JSONDecodeError) as exc:
        print(f"audioprep: cannot read report: {exc}", file=sys.stderr)
        return EXIT_CONFIG
    out_root = Path(args.output)
    cfg = rep.get("params", {})
    cfg.setdefault("max_channels", batch.DEFAULT_MAX_CHANNELS)
    cfg.setdefault("max_seconds", batch.DEFAULT_MAX_SECONDS)
    checked = mismatch = failed = 0
    for entry in rep.get("files", []):
        if entry.get("status") != "ok":
            failed += 1
            print(json.dumps({"relpath": entry.get("relpath"),
                              "error": "file failed during process",
                              "stage": entry.get("stage")}), flush=True)
            continue
        out_path = out_root / entry["relpath"]
        try:
            diffs = _verify_file(entry, out_path, cfg, args.tolerance)
        except Exception as exc:
            failed += 1
            print(json.dumps({"relpath": entry["relpath"],
                              "error": f"{type(exc).__name__}: {exc}"}),
                  flush=True)
            continue
        checked += 1
        if diffs:
            mismatch += 1
            print(json.dumps({"relpath": entry["relpath"],
                              "diffs": diffs}), flush=True)
    print(json.dumps({"checked": checked, "mismatch": mismatch,
                      "failed": failed}), flush=True)
    return EXIT_OK if mismatch == 0 and failed == 0 else EXIT_MISMATCH


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="audioprep",
        description="Offline batch WAV resampling and loudness normalization")
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("process", help="resample and normalize a batch")
    p.add_argument("--input", required=True, help="input directory (recursive)")
    p.add_argument("--output", required=True, help="output directory")
    p.add_argument("--report", required=True, help="JSON report path")
    p.add_argument("--rate", type=int, default=48000,
                   help="target sample rate (positive integer)")
    p.add_argument("--target-lufs", type=float, default=-18.0)
    p.add_argument("--max-boost-db", type=float, default=12.0)
    p.add_argument("--workers", type=int, default=4)
    p.add_argument("--max-channels", type=int,
                   default=batch.DEFAULT_MAX_CHANNELS)
    p.add_argument("--max-seconds", type=float,
                   default=batch.DEFAULT_MAX_SECONDS)
    p.set_defaults(func=cmd_process)

    v = sub.add_parser("verify", help="recompute outputs and compare to report")
    v.add_argument("--report", required=True, help="JSON report path")
    v.add_argument("--output", required=True, help="output directory")
    v.add_argument("--tolerance", type=float, default=0.1,
                   help="max abs difference for dB-domain metrics")
    v.set_defaults(func=cmd_verify)
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    if args.command == "process":
        if args.rate <= 0 or args.workers < 1 or args.max_channels < 1 or \
                args.max_seconds <= 0:
            print("audioprep: rate/workers/max-channels/max-seconds must be "
                  "positive", file=sys.stderr)
            return EXIT_CONFIG
    if args.command == "verify" and args.tolerance < 0:
        print("audioprep: tolerance must be >= 0", file=sys.stderr)
        return EXIT_CONFIG
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
