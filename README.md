# subtitle_probe

Terminal-only ASS inspection and off-screen layout probe. It parses a local
`.ass` file, resolves multi-level style inheritance, explains inline override
tags, renders each event with libass into an off-screen canvas, and prints the
region size plus a SHA-256 digest per event.

## Build and accept

```sh
brew install cmake pkg-config libass nlohmann-json
scripts/make_fixture.sh ./build/generated/demo.ass
cmake -S . -B build -DCMAKE_BUILD_TYPE=Release
cmake --build build --parallel
./build/subtitle_probe --input ./build/generated/demo.ass --canvas 1920x1080 \
  --font-dir /System/Library/Fonts --json ./build/report.json --jobs 4
```

Expected: `events=8 styles=5 rendered=7 failed=1`, followed by one block per
event with `event_id`, `style_chain`, `inline_tags`, `x`, `y`, `width`,
`height`, `nontransparent_pixels` and `sha256`. Exit code `2` is expected for
the demo because one event deliberately uses a font that is not installed.

## Options

| flag | meaning |
| --- | --- |
| `--input FILE` | ASS script to inspect (required) |
| `--canvas WxH` | off-screen frame size, default `1920x1080` |
| `--font-dir DIR` | font directory scanned by libass (provider is `none`, so a missing face is an error rather than a silent substitution) |
| `--json FILE` | write the full report; the file is written to a temporary sibling and atomically renamed |
| `--jobs N` | render worker threads; output order always matches input order |
| `--strict` | promote input, style-graph and event-level errors to exit code `1` |

Exit codes: `0` all rendered, `1` argument/input error (including `--strict`),
`2` at least one event failed to render, `3` the JSON report could not be
written.

## Diagnostics

Every problem carries a line number and, where relevant, an event id:

* `style-field-count`, `style-duplicate-name`, `style-name-missing`
* `event-field-count`, `event-time-invalid`, `event-field-invalid`, `event-unknown`
* `parent-missing`, `style-cycle`, `style-missing`
* `tag-unknown`, `tag-unclosed`, `reset-style-missing`
* style-value adjustments (clamped or replaced values) are listed with the
  original value, the adopted fallback and the defining style
* font fallback messages from libass, per event

A region is reported as `x`, `y`, `width`, `height` only when non-transparent
pixels exist; otherwise the line shows `region=empty`, `region=transparent` or
`region=failed`. `note: region touches or exceeds the canvas edge` marks
clipping.

## Layout

```
src/ass_document.*    Script Info, V4+ Styles and Events parsing
src/style_resolver.*  inheritance graph, per-field merge, clamping provenance
src/inline_tags.*     override tags, escapes, canonical render text
src/layout_probe.*    libass lifecycle, script synthesis, pixel compositing, SHA-256
src/report.*          terminal output and atomic JSON writer
src/main.cpp          CLI, path validation, parallel orchestration
tests/integration.cpp parser/resolver/tag/render/report checks (ctest)
scripts/make_fixture.sh demo fixture
```
