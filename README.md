# subtitle_audit

Offline ASS subtitle style/inline-tag auditor with offscreen libass
rendering. Pure terminal tool: no GUI, no network, no script execution.

## Build

```sh
cmake -S . -B build
cmake --build build
ctest --test-dir build
```

Dependencies (all local): C++17 compiler, CMake >= 3.20, pkg-config,
libass >= 0.17.3 (the build also works against 0.17.5). Fonts are
resolved through the system font provider; font substitution is reported
per event as `font_fallback` instead of being assumed.

## Demo

```sh
./scripts/demo.sh
# or the acceptance command directly:
./build/subtitle_audit --render --resolution 1920x1080 \
    --output build/render.jsonl --strict
```

The demo document is generated in code (`tests/fixtures.cpp`); no
external sample files are required. Expected summary:

```
styles=8 inheritance_edges=6 inheritance_errors=0
events=12 tags_total=47 tags_unknown=2 rendered=12
```

## Options

| Option | Meaning |
| --- | --- |
| `--check` | parse + resolve + diagnose, no rendering |
| `--render` | full pipeline incl. offscreen libass rendering |
| `--strict` | structural errors cause exit code 3 |
| `--demo` | use the built-in generated document |
| `--verbose`, `-v` | print every merged field with its provenance |
| `--resolution WxH` | target video size (default 1920x1080) |
| `--output FILE` | per-event JSONL image summaries |

Exit codes: `0` success, `2` input/argument error, `3` structural error
in strict mode, `4` rendering or output-file failure.

Each stdout event line contains `event_id`, `tag_valid`, `tag_override`,
`region_width`, `region_height`, `image_blocks` and `pixel_digest`.
Region dimensions are merged from libass image-block bounds, never
estimated from character counts, and are clamped to the configured
resolution. The JSONL adds tag totals, fallback flags and diagnostics.

## Pipeline

```
generated/input ASS -> ass_parser -> style_resolver + tag_resolver
                    -> layout_engine -> libass offscreen render
                    -> terminal summary + JSONL image summaries
```

Inheritance is expressed with an extra trailing `Parent` column in the
V4+ Styles `Format` line; libass ignores the unknown column while this
tool resolves it. Missing parents, cycles and duplicate style names
block the affected events with line-numbered diagnostics. Field values
distinguish unset, explicit-empty, explicit and library-default states.
Inline tags are scoped per event; `\t` transform contents are counted
as deferred overrides, repeated properties keep the last valid value,
and unclosed/nested groups plus bad parameters use fixed recovery rules
and are counted as unknown/invalid instead of aborting the scan.
