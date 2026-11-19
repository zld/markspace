# lutcheck

Offline 3D LUT and colour-space verification for the level-render pipeline. The
tool parses Adobe `.cube` LUTs, converts sampled frames into the LUT working
space, then runs the same trilinear kernel through a scalar reference path and
a chunked parallel path. It reports the largest scalar/parallel deviation, the
grid metadata and per-channel 16-bucket histograms before and after the LUT.

Everything — the demo LUT and the sample pixels — is generated deterministically
from the command-line options; no external assets are read.

## Build and verify

```sh
brew install cmake
cmake -S . -B build -DCMAKE_BUILD_TYPE=Release
cmake --build build --parallel
ctest --test-dir build --output-on-failure
./scripts/verify.sh      # build + ctest + acceptance demo checks
```

Requires macOS, C++17, Apple Clang 15+, CMake 3.27+.

## Acceptance run

```sh
./build/lutcheck demo --size 17 --pixels 65536 --input-space srgb \
    --lut-space linear-rgb --threads 4 --boundary clamp --format json
```

The JSON report contains `"lut_size":17`, `"pixels":65536`,
`"max_abs_diff":[0.000000,0.000000,0.000000]`, and `histogram_before` /
`histogram_after` with R, G and B arrays of 16 counts each summing to 65536.
A one-line `"summary"` field repeats `lut_size:17 pixels:65536
max_abs_diff:[...]` so shell checks can grep without a JSON parser.

## Options

| Option | Meaning |
| --- | --- |
| `--size N` | generated LUT grid size, 2–65 (default 17) |
| `--pixels N` | sample count, 1–16,777,216 (default 65536) |
| `--input-space NAME` | `srgb` or `linear-rgb` (default `srgb`) |
| `--lut-space NAME` | `srgb`, `linear-rgb` or `xyz` (default `linear-rgb`) |
| `--threads N` | parallel workers, 1–32 (default 4) |
| `--boundary POLICY` | `clamp` or `reject` (default `clamp`) |
| `--format FORMAT` | `json` or `text` (default `text`) |
| `--seed N` | PRNG seed (fixed default → identical output every run) |
| `--output PATH` | additionally write the report to PATH |
| `--lut-file PATH` | verify an external `.cube` instead of the generated one |
| `--lut-order ORDER` | `rgb`/`bgr` channel order for the generated `.cube` |

Sampled pixels enter as sRGB-encoded or linear RGB. XYZ is a working space for
`--lut-space` only; `--input-space xyz` is rejected as a parameter error.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | success |
| 2 | bad arguments (unknown option/space, out-of-range size, pixels or threads) |
| 3 | invalid LUT or colour data (malformed `.cube`, unreadable LUT file) |
| 4 | processing or output failure (reject trips, unwritable output, I/O) |

Errors are written to standard error; the summary goes to standard output.

## Layout

```
include/color/types.hpp        RGB, matrices, domains, LUT and image types
include/color/cube_parser.hpp  .cube parsing interface
src/cube_parser.cpp            parser with line-numbered diagnostics
src/color_space.cpp            sRGB transfer + sRGB/XYZ matrices
src/lut_kernel.cpp             trilinear kernel, clamp/reject boundaries
src/render_paths.cpp           scalar and chunked parallel paths
src/histogram.cpp              16-bucket per-channel histograms
src/options.cpp, src/main.cpp  CLI parsing and pipeline orchestration
src/demo_data.cpp              deterministic LUT and pixel fixtures
src/report.cpp                 JSON and text summaries
tests/test_pipeline.cpp        parser, colour, kernel, paths, report tests
scripts/verify.sh              build + CTest + acceptance checks
```
