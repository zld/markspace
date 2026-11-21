"""Terminal CLI for abrscan.

Exit codes
----------
0    scan complete (or verify passed)
2    invalid parameters
3    no feasible ladder, database error, or incomplete measurements
75   intentionally interrupted by ``--stop-after``
130  interrupted by SIGINT
"""

from __future__ import annotations

import argparse
import sqlite3
import sys
from pathlib import Path
from typing import Sequence

from .fixture import DEFAULT_FPS, DEFAULT_FRAMES, DEFAULT_HEIGHT, DEFAULT_WIDTH
from .fixture import render_fixture
from .ladder import LadderResult, build_ladder
from .params import (
    ParamError,
    parse_scan_spec,
    parse_stop_after,
    parse_workers,
    validate_fixture_geometry,
)
from .scheduler import ScanOutcome, execute_scan
from .store import Store, StoreError, check_db_path

EXIT_OK = 0
EXIT_PARAMS = 2
EXIT_LADDER = 3
EXIT_INTERRUPTED = 75
EXIT_SIGINT = 130


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="abrscan",
        description="Deterministic local ABR ladder scanner (block-transform proxy).",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    run = sub.add_parser("run", help="scan encoding parameters into a SQLite database")
    run.add_argument("--db", required=True, help="SQLite database inside the workspace")
    run.add_argument(
        "--fixture",
        default="moving-texture",
        choices=("moving-texture", "pan-texture", "static-texture"),
    )
    run.add_argument("--targets-kbps", required=True, help="comma-separated targets")
    run.add_argument("--quant-steps", default=None, help="comma-separated quant steps")
    run.add_argument("--block-sizes", default=None, help="comma-separated block sizes")
    run.add_argument("--workers", default=None, help="parallel workers (1..16)")
    run.add_argument(
        "--stop-after",
        default=None,
        help="finish after N measurements and exit 75 (forces one worker)",
    )
    run.add_argument("--width", type=int, default=DEFAULT_WIDTH)
    run.add_argument("--height", type=int, default=DEFAULT_HEIGHT)
    run.add_argument("--frames", type=int, default=DEFAULT_FRAMES)
    run.add_argument("--fps", type=int, default=DEFAULT_FPS)

    verify = sub.add_parser("verify", help="print the ladder for a completed run")
    verify.add_argument("--db", required=True, help="SQLite database inside the workspace")
    return parser


def _print_run_summary(outcome: ScanOutcome) -> None:
    counts = outcome.counts
    duplicate = ",".join(str(value) for value in outcome.run.duplicate_targets)
    print(
        f"run_id={outcome.run.run_id} fixture={outcome.run.fixture}"
        f" content_hash={outcome.run.content_hash[:16]}"
    )
    print(
        f"planned={outcome.planned} inserted={outcome.inserted}"
        f" completed={counts.get('done', 0)} failed={counts.get('failed', 0)}"
        f" pending={counts.get('pending', 0)} running={counts.get('running', 0)}"
    )
    print(
        f"resumed={'true' if outcome.resumed else 'false'}"
        f" duplicate_targets={duplicate or 'none'}"
        f" completed_now={outcome.completed_now}"
        f" interrupted={'true' if outcome.interrupted else 'false'}"
    )
    for failure in outcome.failures:
        print(
            f"failed_task key={failure.task_key} quant={failure.quant_step}"
            f" block={failure.block_size} error={failure.error}"
        )


def _run(args: argparse.Namespace) -> int:
    spec = parse_scan_spec(args.targets_kbps, args.quant_steps, args.block_sizes)
    workers = parse_workers(args.workers)
    stop_after = parse_stop_after(args.stop_after)
    validate_fixture_geometry(
        args.fixture, args.width, args.height, args.frames, spec.block_sizes
    )
    db_path = check_db_path(args.db)

    fixture = render_fixture(
        args.fixture, args.width, args.height, args.frames, args.fps
    )
    store = Store(db_path)
    run, _ = store.ensure_run(
        fixture=fixture.name,
        content_hash=fixture.content_hash,
        width=fixture.width,
        height=fixture.height,
        frame_count=fixture.frame_count,
        fps=fixture.fps,
        targets=spec.targets_kbps,
        duplicate_targets=spec.duplicate_targets,
    )
    outcome = execute_scan(
        store,
        run,
        fixture,
        spec.combos,
        workers=workers,
        stop_after=stop_after,
    )
    _print_run_summary(outcome)

    if outcome.interrupted:
        print("status=interrupted exit=75 (resume with the same --db)")
        return EXIT_INTERRUPTED
    if outcome.incomplete:
        print("status=incomplete exit=3 (failed tasks remain)")
        return EXIT_LADDER

    result = build_ladder(store.measurements(run.run_id), run.targets)
    if not result.rungs:
        print("status=completed error=no_feasible_rungs exit=3")
        return EXIT_LADDER
    if not result.passed:
        print("status=completed error=monotonicity_violation exit=3")
        return EXIT_LADDER
    print("status=completed")
    return EXIT_OK


def _print_ladder(run_id: int, result: LadderResult, targets: Sequence[int]) -> None:
    print(f"run_id={run_id}")
    if not result.rungs:
        print("rungs=0")
    for rung in result.rungs:
        print(
            f"rung={rung.index} quant={rung.quant_step} block={rung.block_size}"
            f" actual_kbps={rung.actual_kbps:.3f}"
            f" quality_db={rung.quality_db:.3f}"
            f" targets={','.join(str(t) for t in rung.targets)}"
        )
    infeasible = ",".join(str(value) for value in result.infeasible_targets)
    print(
        f"targets={','.join(str(t) for t in targets)}"
        f" infeasible_targets={infeasible or 'none'}"
    )
    for message in result.violations:
        print(f"violation={message}")
    bitrate = "PASS" if result.bitrate_strictly_increasing else "FAIL"
    quality = "PASS" if result.quality_non_decreasing else "FAIL"
    overall = "PASS" if result.passed else "FAIL"
    print(
        f"bitrate_strictly_increasing={bitrate}"
        f" quality_non_decreasing={quality}"
        f" monotonicity={overall}"
    )


def _verify(args: argparse.Namespace) -> int:
    db_path = check_db_path(args.db)
    if not db_path.exists():
        print(f"error=database_not_found path={db_path}", file=sys.stderr)
        return EXIT_LADDER
    store = Store(db_path)
    run = store.latest_run()
    if run is None:
        print("error=no_runs", file=sys.stderr)
        return EXIT_LADDER

    incomplete = store.incomplete_tasks(run.run_id)
    duplicate = ",".join(str(value) for value in run.duplicate_targets)
    print(
        f"run_id={run.run_id} fixture={run.fixture}"
        f" content_hash={run.content_hash[:16]}"
        f" duplicate_targets={duplicate or 'none'}"
    )
    if incomplete:
        print(f"error=incomplete_scan incomplete_tasks={incomplete}", file=sys.stderr)
        return EXIT_LADDER

    measurements = store.measurements(run.run_id)
    if not measurements:
        print("error=no_measurements", file=sys.stderr)
        return EXIT_LADDER
    result = build_ladder(measurements, run.targets)
    _print_ladder(run.run_id, result, run.targets)
    if not result.rungs:
        print("error=no_feasible_rungs", file=sys.stderr)
        return EXIT_LADDER
    if not result.passed:
        print("error=monotonicity_violation", file=sys.stderr)
        return EXIT_LADDER
    return EXIT_OK


def main(argv: Sequence[str] | None = None) -> int:
    parser = _build_parser()
    try:
        args = parser.parse_args(argv)
    except SystemExit as exc:  # argparse uses 2 for usage errors
        return int(exc.code) if exc.code is not None else EXIT_PARAMS

    try:
        if args.command == "run":
            return _run(args)
        return _verify(args)
    except ParamError as exc:
        print(f"error=invalid_argument detail={exc}", file=sys.stderr)
        return EXIT_PARAMS
    except StoreError as exc:
        print(f"error=database detail={exc}", file=sys.stderr)
        return EXIT_LADDER
    except sqlite3.Error as exc:
        print(f"error=database detail={exc}", file=sys.stderr)
        return EXIT_LADDER
    except KeyboardInterrupt:
        print("status=interrupted_signal exit=130", file=sys.stderr)
        return EXIT_SIGINT


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
