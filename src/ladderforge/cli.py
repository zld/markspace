"""Command line interface: fixtures, scan, report.

Exit codes: 0 done, 10 resumable interruption, 2 invalid args/input,
3 encode failure, 4 no valid ladder, 5 database/artifact validation
failure. The scheduler enforces a worker cap and retries, persists every
state transition in the SQLite WAL store, and keeps completed results
plus pending tasks across SIGINT or --stop-after for --resume.
"""

from __future__ import annotations

import argparse
import signal
import sys
import threading
from concurrent.futures import FIRST_COMPLETED, ThreadPoolExecutor, wait
from pathlib import Path

from . import __version__, encoder, fixtures, metrics, planner, selector, store

EXIT_OK = 0
EXIT_INTERRUPTED = 10
EXIT_USAGE = 2
EXIT_ENCODE = 3
EXIT_LADDER = 4
EXIT_STORE = 5

MAX_ATTEMPTS = 2
INPUT_SUFFIXES = {".mp4", ".mov", ".y4m"}


def _err(msg: str) -> None:
    print(f"error: {msg}", file=sys.stderr)


def _run_task(db: store.Store, task, src: Path, out_dir: Path) -> str:
    """Claim and execute one task; record outcome in the store."""
    if not db.claim(task["id"], MAX_ATTEMPTS):
        return "skipped"
    key = task["params_key"]
    out_path = out_dir / f"{key.replace(':', '_')}.mp4"
    try:
        enc = encoder.encode(src, task, out_path)
        score, psnr, frames = metrics.quality_score(
            src, out_path, task["width"], task["height"]
        )
        db.mark_done(task["id"], {
            **enc, "quality_score": score, "psnr": psnr, "frames": frames,
        })
        print(f"done {key}: {enc['actual_kbps']:.1f} kbps, score {score:.2f}")
        return "done"
    except Exception as exc:  # encode, probe, or alignment failure
        db.finish_failure(task["id"], str(exc), MAX_ATTEMPTS)
        print(f"failed {key}: {exc}", file=sys.stderr)
        return "failed"


def _cmd_fixtures(args: argparse.Namespace) -> int:
    spec = fixtures.ClipSpec(
        width=args.width, height=args.height, fps=args.fps,
        duration_s=args.duration, seed=args.seed,
    )
    try:
        frames = fixtures.write_y4m(args.out, spec)
    except ValueError as exc:
        _err(str(exc))
        return EXIT_USAGE
    print(f"wrote {args.out}: {spec.width}x{spec.height} "
          f"{spec.fps}fps {frames} frames (seed {spec.seed})")
    return EXIT_OK


def _validate_input(src: Path) -> bool:
    if not src.is_file():
        return _err(f"input not found: {src}") or False
    if src.suffix.lower() not in INPUT_SUFFIXES:
        return _err(f"unsupported container {src.suffix!r}; "
                    f"expected one of {sorted(INPUT_SUFFIXES)}") or False
    try:
        info = encoder.validate_source(src)
    except encoder.ProbeError as exc:
        return _err(f"invalid input {src}: {exc}") or False
    if info.duration_s > fixtures.MAX_DURATION_S:
        return _err(f"input {src} exceeds {fixtures.MAX_DURATION_S:.0f}s limit") or False
    if info.width > fixtures.MAX_WIDTH or info.height > fixtures.MAX_HEIGHT:
        return _err(f"input {src} exceeds "
                    f"{fixtures.MAX_WIDTH}x{fixtures.MAX_HEIGHT} limit") or False
    return True


def _cmd_scan(args: argparse.Namespace) -> int:
    src = Path(args.input)
    if not _validate_input(src):
        return EXIT_USAGE
    try:
        tasks = planner.expand_grid(args.resolutions, args.targets_kbps, args.presets)
    except ValueError as exc:
        _err(str(exc))
        return EXIT_USAGE
    try:
        db = store.Store(args.db)
    except store.StoreError as exc:
        _err(str(exc))
        return EXIT_STORE
    if db.task_count() and not args.resume:
        _err(f"{args.db} already has scan state; pass --resume to continue")
        return EXIT_INTERRUPTED
    try:
        for key, reason in db.verify_artifacts():
            print(f"requeue {key}: {reason}")
    except store.StoreError as exc:
        _err(str(exc))
        return EXIT_STORE
    reclaimed = db.reclaim_stale()
    if reclaimed:
        print(f"reclaimed {reclaimed} interrupted task(s)")
    db.upsert_tasks(tasks, source=str(src))
    out_dir = Path(args.out)
    out_dir.mkdir(parents=True, exist_ok=True)

    stop = threading.Event()
    previous = signal.signal(signal.SIGINT, lambda *_: stop.set())
    completed = 0
    pool = ThreadPoolExecutor(max_workers=args.workers)
    try:
        due = db.due_tasks(MAX_ATTEMPTS)
        idx = 0
        in_flight = {}
        # Keep at most `workers` tasks in flight so --stop-after and
        # SIGINT leave the remaining tasks pending for --resume.
        while True:
            while idx < len(due) and len(in_flight) < args.workers \
                    and not stop.is_set():
                task = due[idx]
                idx += 1
                in_flight[pool.submit(_run_task, db, task, src, out_dir)] = task
            if not in_flight:
                if stop.is_set():
                    break
                due = db.due_tasks(MAX_ATTEMPTS)  # pick up retried failures
                idx = 0
                if not due:
                    break
                continue
            done, _ = wait(in_flight, return_when=FIRST_COMPLETED)
            for fut in done:
                in_flight.pop(fut)
                if fut.result() == "done":
                    completed += 1
                    if args.stop_after and completed >= args.stop_after:
                        stop.set()
        for fut in in_flight:  # let running tasks commit their results
            fut.result()
    finally:
        pool.shutdown(wait=True)
        signal.signal(signal.SIGINT, previous)
    counts = db.counts()
    print(f"tasks: {counts['done']} done, {counts['pending']} pending, "
          f"{counts['running']} running, {counts['failed']} failed")
    if counts["pending"] or counts["running"]:
        print("interrupted with work remaining; rerun with --resume to continue")
        return EXIT_INTERRUPTED
    if counts["failed"]:
        return EXIT_ENCODE
    return EXIT_OK


def _cmd_report(args: argparse.Namespace) -> int:
    try:
        db = store.Store(args.db)
        rows = db.ladder_rows()
    except store.StoreError as exc:
        _err(str(exc))
        return EXIT_STORE
    try:
        rungs = selector.select_ladder(rows)
    except selector.LadderError as exc:
        _err(f"ladder: FAIL ({exc})")
        return EXIT_LADDER
    if args.format == "json":
        import json
        print(json.dumps({"ladder": "PASS", "rungs": [
            {"tier": i + 1, "target_kbps": r.target_kbps,
             "actual_kbps": round(r.actual_kbps, 1),
             "quality_score": round(r.quality, 2),
             "alias_count": r.alias_count, "monotonicity": "ok",
             "params_key": r.params_key}
            for i, r in enumerate(rungs)]}, indent=2))
    else:
        print("tier\ttarget_kbps\tactual_kbps\tquality_score\talias_count\tmonotonicity")
        for i, r in enumerate(rungs, 1):
            print(f"{i}\t{r.target_kbps}\t{r.actual_kbps:.1f}\t"
                  f"{r.quality:.2f}\t{r.alias_count}\tok")
        print(f"ladder\tPASS\trungs={len(rungs)}")
    return EXIT_OK


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="ladderforge",
        description="Scan encode parameters and build a monotonic ABR ladder.")
    parser.add_argument("--version", action="version", version=__version__)
    sub = parser.add_subparsers(dest="command", required=True)

    fx = sub.add_parser("fixtures", help="generate a deterministic Y4M sample clip")
    fx.add_argument("--out", required=True, help="output .y4m path")
    fx.add_argument("--seed", type=int, default=0)
    fx.add_argument("--width", type=int, default=640)
    fx.add_argument("--height", type=int, default=360)
    fx.add_argument("--fps", type=int, default=30)
    fx.add_argument("--duration", type=float, default=2.0)
    fx.set_defaults(func=_cmd_fixtures)

    sc = sub.add_parser("scan", help="encode the parameter grid and score quality")
    sc.add_argument("--input", required=True, help="source .mp4/.mov/.y4m")
    sc.add_argument("--db", required=True, help="SQLite state database path")
    sc.add_argument("--out", required=True, help="directory for encoded artifacts")
    sc.add_argument("--targets-kbps", required=True, help="comma list, e.g. 180,300")
    sc.add_argument("--resolutions", required=True, help="comma list, e.g. 640x360")
    sc.add_argument("--presets", required=True, help="comma list of x264 presets")
    sc.add_argument("--workers", type=int, default=2, help="max concurrent encodes")
    sc.add_argument("--stop-after", type=int, default=0,
                    help="stop after N completions (resumable)")
    sc.add_argument("--resume", action="store_true",
                    help="continue an existing scan database")
    sc.set_defaults(func=_cmd_scan)

    rp = sub.add_parser("report", help="print the selected monotonic ladder")
    rp.add_argument("--db", required=True, help="SQLite state database path")
    rp.add_argument("--format", choices=("tsv", "json"), default="tsv")
    rp.set_defaults(func=_cmd_report)
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    if args.command == "scan" and args.workers < 1:
        _err("--workers must be >= 1")
        return EXIT_USAGE
    return args.func(args)
