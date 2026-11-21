"""Command line entry point: validate / resolve / convert / demo.

Exit codes: 0 success, 2 argument error (argparse), 3 invalid input or
conversion data, 4 file read/write failure.
"""

from __future__ import annotations

import argparse
import json
import sys
from fractions import Fraction
from pathlib import Path
from typing import Any, Optional

from .formats import v1, v2
from .formats.common import FormatError, read_document
from .model import Timeline, validate
from .otio_adapter import from_otio, to_otio
from .render import render_to_file
from .resolve import ResolveResult, find_conflicts, resolve
from .sample import make_sample

EXIT_OK = 0
EXIT_DATA = 3
EXIT_IO = 4


def _fmt_time(value: Fraction) -> str:
    return str(value.numerator) if value.denominator == 1 else f"{float(value):.6f}"


def _intervals_lines(timeline: Timeline) -> list[str]:
    lines = []
    for track in timeline.tracks:
        parts = []
        for clip in sorted(track.clips(), key=lambda c: (c.start, c.clip_id)):
            parts.append(f"{clip.name} [{_fmt_time(clip.start)},{_fmt_time(clip.end)})")
        lines.append(f"{track.name}: " + ", ".join(parts))
    return lines


def _detect_version(path: str) -> int:
    doc = read_document(path)
    version = doc.get("version")
    if version not in (1, 2):
        raise FormatError(f"{path}: cannot detect exchange version from {version!r}")
    return version


def _load(path: str) -> Timeline:
    version = _detect_version(path)
    timeline = (v1 if version == 1 else v2).load(path)
    return validate(timeline)


def _write_report(path: Optional[str], report: dict[str, Any]) -> None:
    if path:
        Path(path).write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")


def _report_dict(result: ResolveResult, render_sha: Optional[str]) -> dict[str, Any]:
    tl = result.timeline
    delta_frames = result.duration_delta_frames()
    return {
        "timeline": tl.name,
        "rate": str(tl.rate),
        "conflict_pairs": len(result.conflicts),
        "conflicts": [
            {"track": c.track, "clip_a": c.clip_a, "clip_b": c.clip_b,
             "overlap": [_fmt_time(c.overlap_start), _fmt_time(c.overlap_end)]}
            for c in result.conflicts
        ],
        "resolved_intervals": sum(len(t.clips()) for t in tl.tracks),
        "moves": [
            {"track": m.track, "clip_id": m.clip_id,
             "old_start": _fmt_time(m.old_start), "new_start": _fmt_time(m.new_start)}
            for m in result.moves
        ],
        "duration_before_seconds": f"{float(result.duration_before):.6f}",
        "duration_after_seconds": f"{float(result.duration_after):.6f}",
        "duration_delta_frames": (delta_frames.numerator
                                  if delta_frames.denominator == 1
                                  else float(delta_frames)),
        "duration_delta_seconds": f"{float(result.duration_delta_seconds):.6f}",
        "render_sha256": render_sha,
    }


def _print_summary(result: ResolveResult, render_sha: Optional[str]) -> None:
    report = _report_dict(result, render_sha)
    print(f"conflict_pairs={report['conflict_pairs']}")
    print(f"resolved_intervals={report['resolved_intervals']}")
    print(f"duration_delta_frames={report['duration_delta_frames']}")
    print(f"duration_delta_seconds={report['duration_delta_seconds']}")
    for line in _intervals_lines(result.timeline):
        print(line)
    if render_sha:
        print(f"render_sha256={render_sha}")


def _maybe_render(timeline: Timeline, args: argparse.Namespace) -> Optional[str]:
    if not getattr(args, "render", None):
        return None
    width, height, sha = render_to_file(timeline, args.render,
                                        width=getattr(args, "width", 640))
    print(f"render: {args.render} {width}x{height}", file=sys.stderr)
    return sha


def cmd_validate(args: argparse.Namespace) -> int:
    timeline = _load(args.input)
    conflicts = find_conflicts(timeline)
    print(f"input={args.input}")
    print(f"rate={timeline.rate} tracks={len(timeline.tracks)} "
          f"clips={timeline.clip_count()}")
    print(f"duration_seconds={float(timeline.duration_seconds):.6f}")
    print(f"conflict_pairs={len(conflicts)}")
    for c in conflicts:
        print(f"conflict: {c.track} {c.clip_a} vs {c.clip_b} "
              f"[{_fmt_time(c.overlap_start)},{_fmt_time(c.overlap_end)})")
    return EXIT_OK


def cmd_resolve(args: argparse.Namespace) -> int:
    timeline = _load(args.input)
    result = resolve(timeline)
    if args.output:
        v2.dump(result.timeline, args.output)
    render_sha = _maybe_render(result.timeline, args)
    _print_summary(result, render_sha)
    _write_report(args.report, _report_dict(result, render_sha))
    return EXIT_OK


def cmd_convert(args: argparse.Namespace) -> int:
    timeline = _load(args.input)
    if args.to == 2:
        v2.dump(timeline, args.output)
        print(f"converted {args.input} -> {args.output} (v2)")
    else:
        doc, max_error = v1.dumps(timeline, quantize=args.quantize)
        from .formats.common import write_document
        write_document(args.output, doc)
        print(f"converted {args.input} -> {args.output} (v1)")
        if args.quantize:
            print(f"quantize_max_error_frames={float(max_error):.6f}")
    return EXIT_OK


def cmd_demo(args: argparse.Namespace) -> int:
    timeline = make_sample(seed=args.seed)
    # Round-trip through the requested input format before processing.
    if args.input_version == 1:
        doc, _ = v1.dumps(timeline)
        timeline = validate(v1.loads(doc))
    else:
        timeline = validate(v2.loads(v2.dumps(timeline)))
    result = resolve(timeline)
    # Round-trip the resolved result through the output format and OTIO.
    if args.output_version == 1:
        doc, _ = v1.dumps(result.timeline)
        out_timeline = validate(v1.loads(doc))
    else:
        out_timeline = validate(v2.loads(v2.dumps(result.timeline)))
    out_timeline = validate(from_otio(to_otio(out_timeline)))
    final = ResolveResult(
        timeline=out_timeline,
        conflicts=result.conflicts,
        moves=result.moves,
        duration_before=result.duration_before,
        duration_after=out_timeline.duration_seconds,
    )
    render_sha = _maybe_render(out_timeline, args)
    _print_summary(final, render_sha)
    _write_report(args.report, _report_dict(final, render_sha))
    return EXIT_OK


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="cutline",
        description="Timeline checker and resolver for editorial tracks.")
    sub = parser.add_subparsers(dest="command", required=True)

    def add_common(p: argparse.ArgumentParser) -> None:
        p.add_argument("--report", help="write JSON report to this path")
        p.add_argument("--render", help="write PPM preview to this path")
        p.add_argument("--width", type=int, default=640, help="render width in px")

    p = sub.add_parser("validate", help="check an exchange file")
    p.add_argument("--input", required=True)
    p.set_defaults(func=cmd_validate)

    p = sub.add_parser("resolve", help="resolve conflicts on exclusive tracks")
    p.add_argument("--input", required=True)
    p.add_argument("--output", help="write resolved timeline as v2 JSON")
    add_common(p)
    p.set_defaults(func=cmd_resolve)

    p = sub.add_parser("convert", help="convert between exchange versions")
    p.add_argument("--input", required=True)
    p.add_argument("--output", required=True)
    p.add_argument("--to", type=int, choices=(1, 2), required=True,
                   help="target exchange version")
    p.add_argument("--quantize", action="store_true",
                   help="round non-integer v1 values, reporting max error")
    p.set_defaults(func=cmd_convert)

    p = sub.add_parser("demo", help="run the deterministic sample pipeline")
    p.add_argument("--input-version", type=int, choices=(1, 2), default=1)
    p.add_argument("--output-version", type=int, choices=(1, 2), default=2)
    p.add_argument("--seed", type=int, default=41)
    add_common(p)
    p.set_defaults(func=cmd_demo)
    return parser


def main(argv: Optional[list[str]] = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        return args.func(args)
    except OSError as exc:
        print(f"error: file operation failed: {exc}", file=sys.stderr)
        return EXIT_IO
    except (FormatError, ValueError) as exc:
        print(f"error: invalid data: {exc}", file=sys.stderr)
        return EXIT_DATA


if __name__ == "__main__":
    sys.exit(main())
