# mkvmp4

Offline, auditable Matroska (.mkv / WebM-family EBML) to MP4 (ISO BMFF)
remuxer. It re-wraps H.264/H.265 video and AAC/Opus audio without any
external transcoder or network access, and reports the exact track mapping
and timestamp corrections applied to every track.

## Install

```
python -m pip install -e .
```

Requires Python 3.11 and NumPy 2.1.x. No other third-party runtime deps.

## Commands

```
python -m mkvmp4.cli inspect --input clip.mkv [--json]
python -m mkvmp4.cli remux   --input clip.mkv --output clip.mp4 \
                             --mode single|segmented [--json]
python -m mkvmp4.cli verify  --input clip.mp4 [--json]
```

Exit codes: `0` success, `2` invalid arguments / input format error,
`3` conversion or verification failure, `1` unexpected internal error.

## Demo

```
python tests/make_fixtures.py --output /tmp/demo.mkv
python -m mkvmp4.cli remux --input /tmp/demo.mkv --output /tmp/demo.mp4 \
    --mode segmented --json
python -m mkvmp4.cli verify --input /tmp/demo.mp4 --json
```

## Tests

The test suite needs no third-party runner; each file is an executable
script that exits non-zero on the first failed check:

```
python tests/make_fixtures.py --output /tmp/demo.mkv   # synthetic media
python tests/test_core.py                              # unit checks
python tests/test_pipeline.py                          # end-to-end CLI
```

`test_pipeline.py` generates its own fixtures in a temporary directory and
covers both export modes, exit codes, track selection, codec rejection,
atomic output replacement and the no-source-overwrite guarantee.

## Safety limits

`--max-bytes`, `--max-elements` and `--max-sample-bytes` bound every parse.
Outputs are always written to a hidden same-directory temp file and only
atomically replace the destination after the read-back verify pass; the
source file is never modified and failures leave no temp files behind.

## Design notes

* `ebml.py` - hand-written EBML VINT/element parser, unknown-length support.
* `matroska.py` - Segment/Info/Tracks/Cluster/Cues model and block parsing.
* `timeline.py` - per-track timescale normalization with NumPy.
* `codec.py` - codec-specific MP4 sample-entry construction and rejection.
* `bmff.py` - ISO BMFF box writer, sample tables, stco/co64 selection.
* `export.py` - single-file and segmented export orchestration.
* `report.py` - stable, JSON-serializable inspection / remux summaries.
* `cli.py` - argument validation, subcommands and exit codes.
