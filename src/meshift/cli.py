"""Command-line entry point for the meshift remuxer."""

from __future__ import annotations

import argparse
import os
import sys
import tempfile
from pathlib import Path

from .errors import (EXIT_CORRUPT, EXIT_OK, EXIT_USAGE, MeshiftError,
                     TruncatedInputError, UsageError)
from .index import verify_index
from .matroska import parse_matroska
from .mp4 import build_mp4
from .report import build_report, render_text, write_json_report
from .timeline import normalize


def _parse_map(values: list[str]) -> dict[int, int]:
    mapping: dict[int, int] = {}
    for value in values:
        if "=" not in value:
            raise UsageError(f"--track-map expects IN=OUT, got {value!r}")
        left, right = value.split("=", 1)
        try:
            input_no, output_no = int(left), int(right)
        except ValueError as exc:
            raise UsageError(f"invalid track map {value!r}") from exc
        if input_no < 1 or output_no < 1:
            raise UsageError("track numbers must be positive integers")
        if output_no in mapping.values():
            raise UsageError(f"duplicate output track number {output_no}")
        mapping[input_no] = output_no
    return mapping


def _write_atomic(output: Path, payload: bytes) -> None:
    directory = output.parent or Path(".")
    fd, tmp_name = tempfile.mkstemp(prefix=f".{output.name}.", suffix=".tmp",
                                    dir=directory)
    try:
        with os.fdopen(fd, "wb") as handle:
            handle.write(payload)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(tmp_name, output)
    except BaseException:
        try:
            os.unlink(tmp_name)
        except FileNotFoundError:
            pass
        raise


def _run_convert(args: argparse.Namespace) -> dict:
    source = Path(args.input)
    target = Path(args.output)
    if target.exists() and not args.overwrite:
        raise UsageError(
            f"{target} already exists; pass --overwrite to replace it")
    try:
        data = source.read_bytes()
    except FileNotFoundError as exc:
        raise MeshiftError(f"input file not found: {source}") from exc
    except OSError as exc:
        raise MeshiftError(f"cannot read {source}: {exc}") from exc
    mkv = parse_matroska(data)
    tracks = normalize(mkv, _parse_map(args.track_map))

    if args.segment:
        # Segmented export shares the same parse layer; only serialized media
        # are re-selected here, never re-parsed or re-decoded.
        start_ns, end_ns = args.segment
        for track in tracks:
            track.samples = [s for s in track.samples
                             if start_ns <= s.dts_ns < end_ns]
        if not any(track.samples for track in tracks):
            raise UsageError("segment window contains no samples")

    layout = build_mp4(tracks)
    index_entries = verify_index(
        layout.payload, tracks, layout.mdat_start, layout.mdat_end)

    _write_atomic(target, layout.payload)
    report = build_report(mkv.roots, tracks, index_entries, str(target))
    return report


class _ArgumentParser(argparse.ArgumentParser):
    """Argument parser whose usage failures use the meshift exit code 3."""

    def error(self, message: str) -> None:
        self.print_usage(sys.stderr)
        raise UsageError(f"{self.prog}: {message}")


def build_parser() -> argparse.ArgumentParser:
    parser = _ArgumentParser(
        prog="meshift", description="Local Matroska -> MP4 remuxer")
    sub = parser.add_subparsers(dest="command", required=True)
    convert = sub.add_parser("convert", help="remux an MKV file to MP4")
    convert.add_argument("input")
    convert.add_argument("output")
    convert.add_argument("--report", help="write JSON report to this path")
    convert.add_argument("--overwrite", action="store_true",
                         help="replace an existing output file")
    convert.add_argument("--track-map", action="append", default=[],
                         metavar="IN=OUT",
                         help="override output track number, repeatable")
    convert.add_argument("--segment", nargs=2, type=int,
                         metavar=("START_NS", "END_NS"),
                         help="export only samples in the DTS window")
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        report = _run_convert(args)
    except TruncatedInputError as exc:
        print(f"error: truncated input: {exc}", file=sys.stderr)
        return EXIT_CORRUPT
    except MeshiftError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return exc.exit_code
    print(render_text(report))
    if args.report:
        write_json_report(report, args.report)
    return EXIT_OK


if __name__ == "__main__":
    raise SystemExit(main())
