# subtitle-metrics

Offline multilingual subtitle layout metrics tool. Given a JSON subtitle
file and a local TTF/OTF font, it measures each subtitle's bounding box,
line break positions and safe-area overflows **without rendering any
image**. It is a pure terminal tool: no windows, no browser, no network,
no microphone or camera access.

- C++17, CMake 3.20+, FreeType 2.13+, nlohmann/json 3.11+
- Self-contained strict UTF-8 decoder, UAX #29 grapheme cluster
  segmentation and a greedy line breaker (space / kinsoku / hard)
- Deterministic, byte-stable JSON summary suitable for build gates

## Dependencies

### macOS (Homebrew)

```sh
brew install cmake freetype nlohmann-json
```

### Debian / Ubuntu

```sh
sudo apt-get install build-essential cmake libfreetype6-dev nlohmann-json3-dev
```

## Build

```sh
cmake -S . -B build
cmake --build build -j
```

The build produces `build/subtitle-metrics` and, when testing is enabled,
the `build/fixture-samples` helper. Run the test suite with:

```sh
ctest --test-dir build
# or directly:
FONT=/path/to/font.ttf tests/cli_smoke.sh build
```

## Demo / acceptance flow

```sh
# 1. Generate the bundled 20-entry multilingual sample (no external files).
./build/subtitle-metrics --make-fixture ./build/generated-subs.json

# 2. Measure it against a local font and a 1280px canvas.
./build/subtitle-metrics \
    --font /path/to/font.ttf \
    --input ./build/generated-subs.json \
    --width 1280 --safe-left 96 --safe-right 96 \
    --font-size 42 --wrap punctuation \
    --json-out ./build/summary.json
```

Terminal output contains the aggregate counters `subtitle_count`,
`bbox_count`, `overflow_count`, `missing_glyph_count`, followed by each
`id`, its `lines`, `bbox{x,y,width,height}` and `overflow_entries`. The
summary JSON additionally stores the source SHA-256 hash, per-line text,
cluster ranges and box coordinates.

## Options

| Option | Meaning |
| --- | --- |
| `--font PATH` | Local TTF/OTF font file (required for analysis) |
| `--input PATH` | Subtitle JSON document (required for analysis) |
| `--width PX` | Canvas width in pixels (required) |
| `--height PX` | Optional canvas height; enables vertical overflow checks |
| `--safe-left/right/top/bottom PX` | Safe-area insets (default 0) |
| `--font-size PX` | Font size in pixels (required) |
| `--scale N` | Pixel-size multiplier, range 0.01–100 (default 1.0) |
| `--wrap space\|punctuation\|hard` | Line breaking strategy (default punctuation) |
| `--json-out PATH` | Write summary to a file, or `-` for stdout |
| `--make-fixture PATH` | Generate the demo fixture and exit |

### Wrap strategies

- **space** — break only at spaces; unbreakable runs that overflow are
  kept whole and reported.
- **punctuation** — space breaks plus CJK boundary breaks with Japanese
  kinsoku rules: closing punctuation never starts a line and opening
  punctuation never ends one.
- **hard** — break at any cluster boundary when the line is full.

In every strategy a grapheme cluster is atomic: combining marks,
variation selectors and ZWJ/flag emoji sequences can never be split. A
single cluster wider than the safe area occupies its own line and is
flagged `cluster_too_wide`; the algorithm always consumes at least one
cluster per iteration, so it cannot loop forever or drop characters.

## Input format

```json
{
  "subtitles": [
    {"id": "s01", "text": "Hello 世界", "lang": "zh"},
    {"id": "s02", "text": ""}
  ]
}
```

- `id` (string, required, unique) and `text` (string, required, may be
  empty); `lang` is an optional string.
- Duplicate ids and wrong field types are rejected with the 1-based entry
  position. Invalid UTF-8 (overlong, surrogate, truncated, stray
  continuation) fails the entry instead of inserting replacement
  characters.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Analysis completed, no overflow |
| 2 | Input or configuration error |
| 3 | Font loading or output error |
| 4 | Analysis completed but at least one subtitle overflows |

Missing glyphs are recoverable: analysis continues and the codepoints are
listed per subtitle (and counted in `missing_glyph_count`). Font load
failure, an unwritable output path or an illegal configuration aborts the
whole run. File output is written to a sibling temporary file and
atomically renamed, so a failed run never leaves a partial target.

## Limits and safety

To keep the tool bounded, inputs outside these limits are rejected:

- at most 10 000 subtitles per document
- at most 65 536 bytes per subtitle text
- at most 64 MiB of serialized summary output
- canvas dimensions up to 65 535 px, font size up to 1024 px

Only measurements and the on-disk summary are produced — no image is ever
generated. The repository contains no credentials or connection strings,
and build artifacts are git-ignored.
