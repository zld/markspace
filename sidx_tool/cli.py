"""Command line interface: fixture, rebuild, verify.

Terminal only; exit codes follow the package contract (0 ok, 2 format,
3 validation, 4 IO).  ``verify --json`` emits one JSON object per segment,
each carrying start_ms/end_ms/start_offset/end_offset.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from .errors import SidxToolError
from .fixture import FixtureConfig, write_fixture
from .rebuild import rebuild
from .validate import verify_bytes


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="sidx_tool", description="Local BMFF SIDX rebuild and validation tool")
    sub = parser.add_subparsers(dest="command", required=True)

    fixture = sub.add_parser("fixture", help="generate a deterministic MP4 fixture")
    fixture.add_argument("--out", required=True)
    fixture.add_argument("--seed", type=int, default=41)
    fixture.add_argument("--samples-per-track", type=int, default=60)
    fixture.add_argument("--sample-bytes", type=int, default=1000)
    fixture.add_argument("--multi-mdat", action="store_true")
    fixture.add_argument("--index-state", choices=["fresh", "stale", "missing", "damaged"], default="stale")

    reb = sub.add_parser("rebuild", help="rearrange media and rebuild the segment index")
    reb.add_argument("--input", required=True)
    reb.add_argument("--output", required=True)
    reb.add_argument("--segment-ms", type=int, default=500)
    reb.add_argument("--max-drift-ms", type=int, default=80)

    ver = sub.add_parser("verify", help="independently re-read and cross-check an MP4")
    ver.add_argument("--input", required=True)
    ver.add_argument("--json", action="store_true", help="print one JSON object per segment")
    return parser


def _cmd_fixture(args: argparse.Namespace) -> int:
    config = FixtureConfig(
        seed=args.seed,
        samples_per_track=args.samples_per_track,
        sample_bytes=args.sample_bytes,
        multi_mdat=args.multi_mdat,
        index_state=args.index_state,
    )
    path = write_fixture(config, args.out)
    print(f"fixture written: {path} ({path.stat().st_size} bytes, index_state={args.index_state})")
    return 0


def _cmd_rebuild(args: argparse.Namespace) -> int:
    result = rebuild(args.input, args.output, args.segment_ms, args.max_drift_ms)
    print(f"rebuilt: {result.output} ({result.total_bytes} bytes)")
    summary = {
        "sample_entries": sum(
            end - start for seg in result.layout.segments for start, end in seg.track_samples.values()
        ),
        "segment_count": len(result.layout.segments),
        "sidx_refs": len(result.sidx.references),
        "drift_ms": float(result.layout.drift_ms),
    }
    print("summary " + json.dumps(summary, sort_keys=True))
    for index, (start, end) in enumerate(result.segment_ranges):
        seg = result.layout.segments[index]
        print(
            "segment "
            + json.dumps(
                {
                    "index": index,
                    "start_ms": seg.start_ms,
                    "end_ms": seg.end_ms,
                    "start_offset": start,
                    "end_offset": end,
                },
                sort_keys=True,
            )
        )
    return 0


def _cmd_verify(args: argparse.Namespace) -> int:
    verification = verify_bytes(args.input)
    for segment in verification.segments:
        payload = {key: segment[key] for key in ("start_ms", "end_ms", "start_offset", "end_offset")}
        if args.json:
            print(json.dumps(payload, sort_keys=True))
        else:
            print(
                f"segment {segment['index']}: {payload['start_ms']}-{payload['end_ms']}ms "
                f"bytes [{payload['start_offset']},{payload['end_offset']})"
            )
    print("summary " + json.dumps(verification.as_summary(), sort_keys=True))
    return 0


_HANDLERS = {"fixture": _cmd_fixture, "rebuild": _cmd_rebuild, "verify": _cmd_verify}


def main(argv: list[str] | None = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv)
    try:
        return _HANDLERS[args.command](args)
    except SidxToolError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return exc.exit_code
    except FileNotFoundError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 4


if __name__ == "__main__":
    raise SystemExit(main())
