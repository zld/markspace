# cutline

Deterministic editorial timeline model for a small game studio: exact frame
rates, overlap scanning, deterministic resolution, strict JSON exchange,
OpenTimelineIO export and offline render sampling all run off **one** model.

## Setup

```sh
python3.11 -m venv .venv
.venv/bin/python -m pip install opentimelineio==0.17.0 pytest==8.3.5
.venv/bin/python scripts/make_fixture.py --output /tmp/cutline-v2.json
.venv/bin/python -m cutline.cli resolve --input /tmp/cutline-v2.json --strategy priority --json
.venv/bin/python -m pytest -q
```

`cutline/__init__.py` at the repository root shims the `src/` layout so
`python -m cutline.cli` works from a fresh checkout; `pip install -e .` also
works and exposes the `cutline` console script.

## Commands

| command | purpose |
| --- | --- |
| `demo` | resolve the built-in fixture and print a summary |
| `validate` | load a document, list conflicts and model facts |
| `resolve` | resolve overlaps and report intervals, duration delta and render samples |
| `export-otio` | write an OTIO JSON timeline (add `--resolve` to resolve first) |

Exit codes: `0` ok, `2` argument or exchange-format error, `3` model,
resolution, render or export-feasibility error. Diagnostics go to stderr.

## Exchange formats

`{"version": 1, "sequence": {...}}` uses integer frame coordinates and one
constant integer `speed` per clip. `version: 2` uses a rational `frame_rate`
(`numerator`/`denominator`) and a contiguous `rate_segments` list with rational
`speed` objects. Unknown versions, missing fields, duplicate ids, wrong types
and any field outside the documented set are rejected.

Upgrade defaults from v1 to v2: frame rate `r` becomes `r/1`, speed becomes a
single segment, and `name`, `kind`, `level`, `fade_in`, `fade_out`, `priority`
default to `"sequence"`, `"video"`, `0, 0, 0, 0`. `speed` has no default in v1
and must be present.

Output files are written to a same-directory temporary file and atomically
renamed; the CLI refuses to write over its own `--input`.

## Resolution strategies

* `priority` — higher `priority` wins; losing clips are trimmed and a clip
  covered on both sides is split into fragments (`id`, then `id#2`, ...).
  Ties go to original order, then stable id.
* `preserve-duration` — never shortens a clip; clips are pushed right in
  original order, and the growth is reported as `duration_delta_frames`.

Both strategies re-validate every produced clip (duration, source mapping,
fades), rescan for conflicts and fail with a track/clip-located error if the
result is infeasible.

## Known limits

* The structural OTIO exporter rejects segmented rate mappings (a single OTIO
  clip cannot express them); `serialize_clip_json` keeps them losslessly in
  `metadata["cutline"]`.
* OTIO stores rates as doubles; frame counts stay exact and the exact rational
  rate is echoed in `timeline.metadata["cutline"]`.
* The renderer samples the topmost video track (by `level`, then id) and
  refuses to run on an unresolved model rather than picking a winner.
