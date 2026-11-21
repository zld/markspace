# subtitle-metrics

Local, terminal-only subtitle layout verification for macOS. Reads a JSON
manifest of subtitles, measures real glyph advances with FreeType, breaks
lines against a safe area, and writes a per-subtitle JSONL summary used by
review scripts and the build.

No network access, no services, no rendering.

## Build

```sh
brew install cmake freetype
cmake -S . -B build -DCMAKE_BUILD_TYPE=Release
cmake --build build --parallel
```

macOS 12+, Apple Silicon and Intel. The only framework dependency is
CoreFoundation, used for Unicode character classes; FreeType is linked from
Homebrew.

## Acceptance path

```sh
./build/subtitle-metrics --make-fixture build/demo.json
./build/subtitle-metrics measure --input build/demo.json --output build/summary.jsonl \
  --font /System/Library/Fonts/Supplemental/Arial.ttf \
  --width 640 --safe-inset 32 --max-lines 2 --collect-errors
./tests/run.sh build/subtitle-metrics
```

The `measure` run prints:

```
subtitles=12 lines=18 overflows=2 missing_glyphs=1 errors=2
```

and exits **3** because the demo fixture deliberately contains two
over-max-lines subtitles. `tests/run.sh` exits 0.

## Manifest

```json
{
  "font": {
    "path": "/System/Library/Fonts/Supplemental/Arial.ttf",
    "size": 40,
    "letter_spacing": 0,
    "line_spacing": 1.25,
    "missing_glyph": "fallback"
  },
  "canvas": { "width": 640, "safe_inset": 32 },
  "max_lines": 2,
  "subtitles": [
    { "id": "line-01", "language": "en", "text": "Session one begins." }
  ]
}
```

Required: `font.path`, `font.size`, `canvas.width`, `canvas.safe_inset`,
`max_lines`, and `id`/`language`/`text` per subtitle. `font.letter_spacing`
(default 0), `font.line_spacing` (default 1.0) and `font.missing_glyph`
(default `error`) are optional. Limits: 10 000 subtitles per batch and
16 KiB of UTF-8 per `text`; duplicates ids, illegal dimensions and missing
font files are rejected with a JSON path plus subtitle index.

Command-line `--font`, `--width`, `--safe-inset`, `--max-lines` and
`--missing-glyph` override the manifest. `--missing-glyph error` is the
default and reports substitutions as item errors; `fallback` measures the
`.notdef` box and counts substitutions instead.

## Summary records

One JSON object per line, in manifest order:

| field | meaning |
| --- | --- |
| `id`, `language` | from the manifest |
| `lines` | laid-out line count (non-negative) |
| `bbox` | union of line advances and the vertical block, pixels |
| `line_breaks` | code-point offset where each line after the first starts |
| `line_ranges` | `{begin,end}` cluster index ranges per line |
| `break_reasons` | `space`, `punctuation`, `fallback`, `explicit`, `end` |
| `overflow_count` | non-negative count of unsatisfied lines |
| `overflow_reasons` | per-line reason or `""`, plus `cluster_too_wide` / `max_lines` |
| `missing_glyphs` | distinct code points substituted |
| `errors` | locatable item messages (offsets are UTF-8 bytes) |

`tests/validate_summary.py FILE` checks these invariants independently of the
exporter.

## Behavior notes

- UTF-8 decoding rejects overlong encodings, isolated continuation bytes,
  truncated sequences, surrogates and code points above U+10FFFF.
- Combining marks, variation selectors, emoji modifiers, enclosing marks,
  regional-indicator pairs and full ZWJ emoji sequences form single clusters
  that line breaking never splits.
- Break opportunities prefer whitespace and punctuation; a run with no such
  opportunity falls back to cluster boundaries, so a long word is split but
  never silently widened. A cluster wider than the safe area is reported as
  `cluster_too_wide` and counted as an overflow.
- Explicit newlines are honored before automatic wrapping, and exceeding
  `max_lines` adds one overflow plus a locatable error.
- `{t=<ms>}` reveal markers are stripped before measuring and never add width.
  Unknown markers, malformed times, unterminated markers and decreasing times
  are reported by byte offset and do not affect later subtitles. `{{` and
  `}}` are literal braces.
- The summary is written to a same-directory temporary file and renamed into
  place only after the whole run succeeds.

## Exit codes

| code | meaning |
| --- | --- |
| 0 | completed with no item errors |
| 2 | input, font or layout parameters invalid |
| 3 | item errors found (strict or collect mode) |
| 4 | summary could not be written |

In strict mode (the default) the first item error stops the run before the
summary is replaced; the previous file is preserved. `--collect-errors`
processes every subtitle and writes the full summary. The totals line always
describes the records that were emitted.

## Layout

```
src/main.cpp          CLI parsing, orchestration, exit codes
src/manifest.cpp      manifest loading and validation
src/json.cpp          local JSON reader
src/unicode.cpp       UTF-8 decoding, cluster building, break classes
src/font_metrics.cpp  FreeType face, metrics and glyph cache
src/line_breaker.cpp  shaping, greedy breaking, overflow accounting
src/timing.cpp        per-character marker stripping
src/exporter.cpp      JSONL serialization and atomic replacement
tests/fixtures.cpp    deterministic demo manifest
tests/run.sh          command-line regression
tests/validate_summary.py  independent summary checks
```
