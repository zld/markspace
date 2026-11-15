# subtitle-metrics

Offline subtitle line-width / safe-area / wrap checker for multi-platform
release QA. Measures real glyph metrics with FreeType (26.6 fixed-point
output) instead of estimating by character count. No network, no GUI.

## Build & test

```sh
cmake -S . -B build -DCMAKE_BUILD_TYPE=Release
cmake --build build -j4
ctest --test-dir build --output-on-failure
```

Dependencies (Ubuntu 24.04):
`sudo apt-get install -y build-essential cmake libfreetype-dev nlohmann-json3-dev fonts-dejavu-core fonts-noto-cjk`

## Usage

```sh
# Generate a deterministic 128-cue sample (8 unbreakable long tokens)
./build/subtitle-metrics fixture --out /tmp/subtitle-demo.srt --count 128

# Measure: per-cue JSONL plus a summary on stderr
./build/subtitle-metrics measure \
  --input /tmp/subtitle-demo.srt \
  --font /usr/share/fonts/truetype/dejavu/DejaVuSans.ttf \
  --fallback-font /usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc \
  --font-size 32 --max-width 640 --max-lines 2 --jobs 4 --jsonl -
```

`--jsonl -` writes records to stdout; `--summary PATH` additionally writes
the summary JSON via same-directory temp file + atomic rename. Fonts are
tried in order (`--font` first, then each `--fallback-font`). `--jobs`
accepts 1..32; each worker owns its FT_Face set and output stays in input
order.

Per-cue record fields: `bbox_26_6` (ink box, 1/64 px), `line_widths_26_6`,
`wrap_offsets_utf8` (byte offsets of line starts), `missing_glyphs`,
`requires_shaping` (complex script present; reported, not shaped),
`overflow` (any line wider than `--max-width` or more than `--max-lines`).

## Exit codes

| code | meaning                                   |
|------|-------------------------------------------|
| 0    | success                                   |
| 2    | parse or encoding errors were encountered |
| 3    | font loading / FreeType failure           |
| 4    | output write failure                      |
| 64   | usage error / input over limits           |

Limits: input up to 100 MB and 100000 cues; larger inputs are refused.
Failures never overwrite a previous summary file.
