"""ladderlab command-line interface.

Usage:
    python -m ladderlab.cli run --db PATH --targets-bps N,N,... [options]

Exit codes:
    0  ladder complete and validated
    2  parameter or input incompatibility (bad args, fingerprint mismatch)
    3  scan incomplete; resume with --resume
    4  persisted data corrupt or ladder validation failed
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import sys
import tempfile

from . import codec, fixture, ladder, rd, scheduler, store

EXIT_OK = 0
EXIT_INCOMPATIBLE = 2
EXIT_INCOMPLETE = 3
EXIT_CORRUPT = 4


def _parse_targets(text: str) -> list[int]:
    try:
        targets = [int(x) for x in text.split(",") if x.strip()]
    except ValueError:
        raise argparse.ArgumentTypeError("targets-bps must be comma-separated ints")
    if not targets or any(t <= 0 for t in targets):
        raise argparse.ArgumentTypeError("targets-bps must be positive integers")
    return targets


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="ladderlab", description=__doc__)
    sub = p.add_subparsers(dest="command", required=True)
    run = sub.add_parser("run", help="run/continue a parameter scan")
    run.add_argument("--db", required=True, help="SQLite database path")
    run.add_argument("--targets-bps", required=True, type=_parse_targets)
    run.add_argument("--seed", type=int, default=41)
    run.add_argument("--workers", type=int, default=2)
    run.add_argument("--stop-after", type=int, default=None,
                     help="complete at most N tasks this run")
    run.add_argument("--resume", action="store_true",
                     help="reclaim interrupted tasks and continue")
    run.add_argument("--json-out", default=None,
                     help="also write the report atomically to this file")
    return p


def _emit(report: dict, json_out: str | None) -> None:
    text = json.dumps(report, indent=2, sort_keys=True)
    print(text)
    if json_out:
        directory = os.path.dirname(os.path.abspath(json_out))
        fd, tmp = tempfile.mkstemp(dir=directory, prefix=".ladderlab-", suffix=".tmp")
        try:
            with os.fdopen(fd, "w") as fh:
                fh.write(text + "\n")
            os.replace(tmp, json_out)
        except BaseException:
            os.unlink(tmp)
            raise


def _combined_fingerprint(clips: list, seed: int) -> str:
    h = hashlib.sha256()
    h.update(fixture.input_fingerprint(clips, seed).encode())
    h.update(codec.grid_fingerprint().encode())
    return h.hexdigest()


def cmd_run(args: argparse.Namespace) -> int:
    if not 1 <= args.workers <= scheduler.MAX_WORKERS:
        print(json.dumps({"error": f"workers must be 1..{scheduler.MAX_WORKERS}"}))
        return EXIT_INCOMPATIBLE
    if args.stop_after is not None and args.stop_after < 1:
        print(json.dumps({"error": "stop-after must be >= 1"}))
        return EXIT_INCOMPATIBLE

    clips = fixture.generate_clips(args.seed)
    fingerprint = _combined_fingerprint(clips, args.seed)
    params = codec.iter_params()

    try:
        conn = store.connect(args.db)
        store.init_schema(conn)
    except store.CorruptDatabaseError as exc:
        _emit({"status": "corrupt", "error": str(exc)}, args.json_out)
        return EXIT_CORRUPT

    try:
        store.check_fingerprint(conn, fingerprint)
    except store.FingerprintMismatchError as exc:
        _emit({"status": "incompatible", "error": str(exc)}, args.json_out)
        conn.close()
        return EXIT_INCOMPATIBLE

    store.seed_tasks(conn, [p.key for p in params])
    reclaimed = store.reclaim_running(conn)
    before = store.task_counts(conn)
    resumed = before["total"] - before["done"]
    conn.close()

    stats = scheduler.run_scan(
        args.db, clips, params,
        workers=args.workers, stop_after=args.stop_after,
    )
    stats.reclaimed = reclaimed

    conn = store.connect(args.db)
    counts = store.task_counts(conn)
    base = {
        "planned": counts["total"],
        "done": counts["done"],
        "failed": counts["failed"],
        "resumed": resumed,
        "completed_this_run": stats.completed_this_run,
    }
    if counts["done"] != counts["total"] or counts["failed"]:
        base["status"] = "incomplete"
        base["remaining"] = counts["total"] - counts["done"]
        if stats.errors:
            base["errors"] = stats.errors
        _emit(base, args.json_out)
        conn.close()
        return EXIT_INCOMPLETE

    results = store.load_results(conn)
    conn.close()
    candidates, dropped = rd.dedupe_candidates(results)
    rungs, violations = ladder.select_ladder(candidates, args.targets_bps)
    checks = ladder.validate_ladder(rungs)
    uniq, alias_count = ladder.unique_targets(args.targets_bps)
    report = {
        **base,
        "status": "ok" if violations == 0 and checks["violations"] == 0 else "invalid",
        "unique_rungs": len(uniq),
        "aliases": alias_count,
        "duplicate_streams_dropped": dropped,
        "rungs": [r.to_dict() for r in rungs],
        "checks": checks,
    }
    _emit(report, args.json_out)
    ok = (
        violations == 0
        and checks["violations"] == 0
        and checks["bitrate_monotonic"]
        and checks["quality_monotonic"]
    )
    return EXIT_OK if ok else EXIT_CORRUPT


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    if args.command == "run":
        return cmd_run(args)
    return EXIT_INCOMPATIBLE


if __name__ == "__main__":
    sys.exit(main())
