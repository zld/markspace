# lutcheck

A local, offline toolkit for validating and converting 3D LUTs. It parses
`.cube` files strictly, converts samples between Rec.709, Display-P3 and the
untagged RGB working space, applies the LUT with trilinear interpolation, and
cross-checks a scalar reference path against a chunked parallel path. The
result is reported as a stable JSON document with per-channel deviation and
before/after histograms.

Everything is local: no network access, no external image libraries, no GUI.

## Build

```sh
brew install cmake ninja
cmake -S . -B build -G Ninja -DCMAKE_BUILD_TYPE=Release
cmake --build build
ctest --test-dir build --output-on-failure
```

Catch2 3.5+ is picked up from Homebrew when present; otherwise CMake fetches
it. Build products stay in `build/` and are not part of the source tree.

## Acceptance demo

```sh
./build/lutcheck demo --generate-fixtures --size 17 --space rec709 \
  --compare scalar,parallel --histogram-bins 16 --json
```

or `./scripts/demo.sh`, which runs the same command and verifies the JSON
contract (grid size, three-channel deviation ≤ 1e-6, 3×16 histogram counts,
~10⁴ pixels) plus the usage and input exit codes.

## Commands

| Command | Purpose |
| --- | --- |
| `demo` | Generate deterministic fixtures and analyse them |
| `render` | Apply a `.cube` to one PPM, optionally writing the result |
| `batch` | Apply a `.cube` to a list of PPMs, one output per input |
| `parse` | Print LUT metadata (title, grid size, node count, domain) |

Common flags: `--size N` (2–65), `--space S`, `--input-space S`,
`--lut-space S`, `--compare scalar,parallel`, `--threads N` (1–32),
`--histogram-bins N`, `--threshold T`, `--json`, `--input`, `--inputs`,
`--lut`, `--output`, `--out-dir`, `--generate-fixtures`.

Colour spaces are `rgb` (Rec.709 primaries), `rec709` and `display-p3`; all
share a D65 white point, so conversions are exact basis changes.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Completed, deviation within threshold |
| 2 | Usage error (bad flag, out-of-range value, missing command) |
| 3 | Input or LUT error (parse failure, unreadable/truncated PPM) |
| 4 | Scalar and parallel paths differ by more than the threshold |
| 5 | Output failure (cannot write render or report) |

## JSON report

Fields are emitted in a fixed order:

```json
{
  "conversion": "rec709->rec709",
  "grid_size": 17,
  "pixels": 12288,
  "input_space": "rec709",
  "lut_space": "rec709",
  "paths": ["scalar", "parallel"],
  "threads": 4,
  "workers": 3,
  "histogram_bins": 16,
  "max_deviation_rgb": {"r": 0, "g": 0, "b": 0},
  "deviation_threshold": 1e-06,
  "deviation_exceeded": false,
  "histogram_before": {"bins": 16, "r": [...], "g": [...], "b": [...]},
  "histogram_after": {"bins": 16, "r": [...], "g": [...], "b": [...]}
}
```

`workers` is the number of threads actually started, bounded by the number of
fixed-size chunks, so it never exceeds `threads`. Every floating point value
is checked finite before serialization; a non-finite value aborts the report
with exit code 5 rather than emitting invalid JSON.

## Design notes

- **Axis order.** `.cube` rows are red-fastest, then green, then blue. The
  parser stores them that way (`index = r + g·N + b·N²`) and the interpolation
  kernel indexes the same way, so no transposition happens between layers.
- **One kernel, two paths.** Both render paths call `transform_sample`; the
  parallel path only decides *which* pixels a worker owns. Chunk boundaries
  derive solely from pixel count and chunk size, so the output is
  bit-identical regardless of thread count.
- **Clamping.** Domain mapping clamps out-of-range values instead of
  extrapolating, keeping negative values and highlights stable.
- **Atomic output.** PPM writes go to a pid-suffixed temporary file and are
  renamed only after a successful flush; a failed or aborted run leaves
  nothing behind.
- **Deterministic fixtures.** Fixture LUTs and images use fixed arithmetic and
  fixed formatting, so repeated runs are byte-identical.

## Limits

Grid size 2–65 (≤ 65³ nodes), images ≤ 10,000,000 pixels, batch ≤ 4096
inputs, histogram bins 1–4096, threads 1–32. Oversized or overflowing inputs
are rejected with a located error before any large allocation.
