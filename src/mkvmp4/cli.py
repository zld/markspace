"""Command line interface: inspect / remux / make-fixture.

Exit codes: 0 success, 2 argument/limit error, 3 corrupt input,
4 unsupported tracks, 5 write or validation failure.
"""
from __future__ import annotations

import argparse
import json
import os
import sys

from . import __version__, ebml, elements, mapping, matroska, mp4inspect, pipeline, timestamps

EXIT_OK = 0
EXIT_USAGE = 2
EXIT_CORRUPT = 3
EXIT_UNSUPPORTED = 4
EXIT_WRITE_FAILED = 5


def _inspect_mkv(path: str) -> dict:
    with open(path, "rb") as fh:
        data = fh.read()
    tops = elements.parse_file(data)
    summary = elements.summarize(tops)
    seg = matroska.parse_segment(data, tops)
    tmap = mapping.build_track_map(seg.tracks)
    tracks = []
    for entry in tmap["tracks"]:
        track = seg.track_by_number(entry["track_number"])
        ts_units = [b.timestamp for b in seg.blocks
                    if b.track_number == track.number for _ in b.frames]
        timescale = timestamps.choose_timescale(track.type, track.sampling_freq)
        pts, _ = timestamps.expand_timestamps(
            ts_units, seg.timestamp_scale, timescale, track.default_duration_ns)
        tracks.append({
            **entry,
            "timescale": timescale,
            "sample_count": len(ts_units),
            "first_pts_ms": timestamps.pts_ms(int(pts[0]), timescale),
            "last_pts_ms": timestamps.pts_ms(int(pts[-1]), timescale),
        })
    return {
        "format": "matroska",
        "file_size": len(data),
        "element_count": summary["element_count"],
        "block_elements": summary["block_elements"],
        "timestamp_scale_ns": seg.timestamp_scale,
        "track_map": tmap["tracks"],
        "skipped_tracks": tmap["skipped"],
        "sample_count": sum(t["sample_count"] for t in tracks),
        "tracks": tracks,
    }


def _cmd_inspect(args) -> dict:
    if not os.path.isfile(args.input):
        raise pipeline.UsageError(f"input file not found: {args.input}")
    with open(args.input, "rb") as fh:
        head = fh.read(12)
    if head[:4] == b"\x1a\x45\xdf\xa3":
        return _inspect_mkv(args.input)
    if head[4:8] == b"ftyp":
        return mp4inspect.inspect_mp4(args.input)
    raise matroska.MatroskaError("unrecognized container format", 0)


def _cmd_remux(args) -> dict:
    return pipeline.remux(args.input, args.output)


def _cmd_make_fixture(args) -> dict:
    from .fixture import build_fixture
    data = build_fixture()
    with open(args.output, "wb") as fh:
        fh.write(data)
    return {"output": os.path.abspath(args.output), "file_size": len(data),
            "format": "matroska"}


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="mkvmp4",
        description="Offline Matroska -> MP4 remuxer (stream copy, no re-encode)")
    parser.add_argument("--version", action="version",
                        version=f"%(prog)s {__version__}")
    sub = parser.add_subparsers(dest="command", required=True)

    p_inspect = sub.add_parser("inspect", help="inspect an MKV or MP4 file")
    p_inspect.add_argument("input")
    p_inspect.add_argument("--json", action="store_true",
                           help="emit machine-readable JSON")
    p_inspect.set_defaults(func=_cmd_inspect)

    p_remux = sub.add_parser("remux", help="remux MKV to MP4 without re-encoding")
    p_remux.add_argument("input")
    p_remux.add_argument("--output", required=True)
    p_remux.add_argument("--json", action="store_true",
                         help="emit machine-readable JSON")
    p_remux.set_defaults(func=_cmd_remux)

    p_fixture = sub.add_parser("make-fixture",
                               help="generate a deterministic multi-track MKV")
    p_fixture.add_argument("--output", required=True)
    p_fixture.add_argument("--json", action="store_true")
    p_fixture.set_defaults(func=_cmd_make_fixture)
    return parser


def _print(result: dict, as_json: bool) -> None:
    if as_json:
        print(json.dumps(result, indent=2))
        return
    for key, value in result.items():
        if isinstance(value, (list, dict)):
            print(f"{key}: {json.dumps(value)}")
        else:
            print(f"{key}: {value}")


def _fail(args, code: int, kind: str, message: str) -> int:
    payload = {"error": kind, "message": message, "exit_code": code}
    if getattr(args, "json", False):
        print(json.dumps(payload), file=sys.stderr)
    else:
        print(f"error[{kind}]: {message}", file=sys.stderr)
    return code


def main(argv=None) -> int:
    args = _build_parser().parse_args(argv)
    try:
        result = args.func(args)
    except pipeline.UsageError as exc:
        return _fail(args, EXIT_USAGE, "usage", str(exc))
    except (ebml.EbmlError, matroska.MatroskaError, mp4inspect.Mp4Error) as exc:
        return _fail(args, EXIT_CORRUPT, "corrupt_input", str(exc))
    except pipeline.UnsupportedTrackError as exc:
        return _fail(args, EXIT_UNSUPPORTED, "unsupported_track", str(exc))
    except (pipeline.WriteFailedError, pipeline.ValidationError) as exc:
        return _fail(args, EXIT_WRITE_FAILED, "write_failed", str(exc))
    except OSError as exc:
        return _fail(args, EXIT_WRITE_FAILED, "io_error", str(exc))
    _print(result, getattr(args, "json", False))
    return EXIT_OK


if __name__ == "__main__":
    sys.exit(main())
