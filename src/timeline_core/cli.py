"""Command-line entry point for the timeline resolution pipeline.

Exit codes:

* ``0`` - success;
* ``2`` - input or format error (:class:`ResolutionError`, bad CLI usage);
* ``1`` - internal/unexpected processing failure.

On any error the process exits non-zero *before* any manifest file is
written, so partial output can never appear.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any, Sequence

from . import format_v1, format_v2
from .model import ResolutionError, ResolutionResult, Timeline
from .render import RenderManifest, render_manifest
from .resolve import resolve_timeline

# Built-in demo delivery: clips may share time ranges across tracks.
DEMO_SPEC: tuple[tuple[str, str, float, float, int], ...] = (
    # (id, track, start_seconds, end_seconds, priority)
    ("A", "V1", 0.0, 5.0, 1),
    ("B", "V2", 3.0, 7.0, 2),
    ("C", "V1", 6.0, 9.0, 1),
)

FORMATS = ("v1", "v2")


def build_demo_document(fmt: str, fps: int, reversed_order: bool) -> dict[str, Any]:
    """Generate the self-contained demo exchange document (no external files)."""
    records = list(DEMO_SPEC)
    if reversed_order:
        records = list(reversed(records))
    if fmt == "v1":
        return {
            "version": format_v1.VERSION,
            "fps": fps,
            "tracks": _demo_tracks(
                records,
                lambda clip_id, start, end, priority: {
                    "id": clip_id,
                    "start_seconds": _demo_number(start),
                    "end_seconds": _demo_number(end),
                    "priority": priority,
                },
            ),
        }
    if fmt == "v2":
        return {
            "version": format_v2.VERSION,
            "rate": {"numerator": fps, "denominator": 1},
            "tracks": _demo_tracks(
                records,
                lambda clip_id, start, end, priority: {
                    "clip_id": clip_id,
                    "start_frame": round(start * fps),
                    "end_frame": round(end * fps),
                    "priority": priority,
                },
            ),
        }
    raise ResolutionError(f"unsupported format {fmt!r}; expected one of {FORMATS}")


def _demo_number(value: float) -> int | float:
    return int(value) if float(value).is_integer() else value


def _demo_tracks(records: Sequence[tuple[str, str, float, float, int]], clip_builder):
    track_names: list[str] = []
    for _, track_name, *_ in records:
        if track_name not in track_names:
            track_names.append(track_name)
    tracks = []
    for track_name in track_names:
        tracks.append(
            {
                "name": track_name,
                "clips": [
                    clip_builder(clip_id, start, end, priority)
                    for clip_id, owner, start, end, priority in records
                    if owner == track_name
                ],
            }
        )
    return tracks


def parse_document(document: dict[str, Any], fmt: str) -> Timeline:
    if fmt == "v1":
        return format_v1.loads(document)
    if fmt == "v2":
        return format_v2.loads(document)
    raise ResolutionError(f"unsupported format {fmt!r}; expected one of {FORMATS}")


def run_pipeline(document: dict[str, Any], fmt: str) -> tuple[
    ResolutionResult, RenderManifest
]:
    """Reader -> canonical model -> resolver -> per-frame manifest."""
    timeline = parse_document(document, fmt)
    result = resolve_timeline(timeline)
    manifest = render_manifest(result)
    return result, manifest


def build_summary(
    result: ResolutionResult,
    manifest: RenderManifest,
    fmt: str,
    reversed_order: bool,
) -> dict[str, Any]:
    return {
        "format": fmt,
        "fps": result.timeline.fps,
        "record_order": "reversed" if reversed_order else "as-delivered",
        "conflict_count": result.conflict_count,
        "conflicts": [
            {
                "first_id": conflict.first_id,
                "second_id": conflict.second_id,
                "overlap": {
                    "start": conflict.overlap.start,
                    "end": conflict.overlap.end,
                },
            }
            for conflict in result.conflicts
        ],
        "resolved_intervals": [
            {"start": interval.start, "end": interval.end, "id": interval.clip_id}
            for interval in result.intervals
        ],
        "duration_deviation_frames": result.duration_deviation_frames,
        "covered_frames_before": result.covered_frames_before,
        "covered_frames_after": result.covered_frames_after,
        "rendered_frames": manifest.rendered_frames,
    }


def _add_common_input_args(parser: argparse.ArgumentParser) -> None:
    parser.add_argument(
        "--format",
        choices=FORMATS,
        required=True,
        help="exchange format version of the input document",
    )
    parser.add_argument(
        "--json",
        dest="as_json",
        action="store_true",
        help="emit the machine-readable JSON summary",
    )
    parser.add_argument(
        "--render-out",
        type=Path,
        default=None,
        help="optional path for the full per-frame render manifest JSON",
    )


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="timeline_core.cli",
        description="Deterministic timeline overlap resolution pipeline",
    )
    subparsers = parser.add_subparsers(dest="command", required=True)

    demo = subparsers.add_parser(
        "demo", help="run the built-in reproducible demo timeline"
    )
    demo.add_argument("--fps", type=int, default=24, help="integer frame rate")
    demo.add_argument(
        "--reverse",
        action="store_true",
        help="reverse record order to prove order independence",
    )
    _add_common_input_args(demo)

    process = subparsers.add_parser(
        "run", help="resolve an exchange document read from a JSON file"
    )
    process.add_argument("input", type=Path, help="path to the input JSON document")
    _add_common_input_args(process)
    return parser


def _load_input(path: Path, fmt: str) -> dict[str, Any]:
    if fmt == "v1":
        return format_v1.load_json(path)
    return format_v2.load_json(path)


def main(argv: Sequence[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        if args.command == "demo":
            if args.fps <= 0:
                raise ResolutionError(f"fps must be positive, got {args.fps}")
            document = build_demo_document(args.format, args.fps, args.reverse)
            reversed_order = args.reverse
        else:
            document = _load_input(args.input, args.format)
            reversed_order = False

        result, manifest = run_pipeline(document, args.format)
        summary = build_summary(result, manifest, args.format, reversed_order)

        if args.render_out is not None:
            args.render_out.write_text(
                json.dumps(manifest.to_dict(), indent=2, sort_keys=True) + "\n",
                encoding="utf-8",
            )
        if args.as_json:
            print(json.dumps(summary, indent=2, sort_keys=True))
        else:
            _print_human_summary(summary)
        return 0
    except ResolutionError as exc:
        print(f"input error: {exc}", file=sys.stderr)
        return 2
    except Exception as exc:  # noqa: BLE001 - top-level safety net maps to code 1
        print(f"internal error: {type(exc).__name__}: {exc}", file=sys.stderr)
        return 1


def _print_human_summary(summary: dict[str, Any]) -> None:
    print(
        f"format={summary['format']} fps={summary['fps']} "
        f"order={summary['record_order']}"
    )
    print(f"conflict_count={summary['conflict_count']}")
    for conflict in summary["conflicts"]:
        overlap = conflict["overlap"]
        print(
            f"  conflict {conflict['first_id']} <-> {conflict['second_id']} "
            f"on frames [{overlap['start']}, {overlap['end']})"
        )
    print("resolved_intervals:")
    for interval in summary["resolved_intervals"]:
        print(f"  [{interval['start']}, {interval['end']}) -> {interval['id']}")
    print(
        f"duration_deviation_frames={summary['duration_deviation_frames']} "
        f"rendered_frames={summary['rendered_frames']}"
    )


if __name__ == "__main__":
    raise SystemExit(main())
