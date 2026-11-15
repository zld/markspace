# subsync

Offline, terminal-only normalizer for **SRT, WebVTT and ASS** subtitles. It
unifies mixed-vendor inputs behind one cue model, converts frame rates with
exact integer rational arithmetic, resolves timing overlaps under a chosen
policy, and verifies every output cue by parsing the rendered file again.

No subtitle parsing library is used: all three grammars are hand-written in
`internal/parse`. The only third-party modules are `golang.org/x/net`
(charset detection/lookup) and `golang.org/x/text` (encoders/decoders),
vendored under `vendor/`.

## Pipeline

```
bytes → encoding sniff/decode → format parser → unified Cue model
      → overlap resolution → frame math → serializer → atomic file write
      → re-parse output → round-trip verification → JSONL report
```

## Demo (acceptance command)

```sh
go mod download && go mod vendor &&
go run -mod=vendor ./cmd/subsync demo \
  --input-format srt --output-format vtt \
  --fps 24000/1001 --overlap clip --jsonl
# or simply: scripts/demo.sh
```

The demo generates its own input (no external files): 8 cues with one
overlapping pair, encoded as **UTF-16LE with BOM**. It prints 8 per-cue JSONL
records plus one summary record and exits 0 when every cue round-trips within
42 ms with no lost entries.

## Usage

```sh
subsync run --input in.srt --output out.vtt \
  --input-format srt --output-format vtt \
  --fps 24000/1001 --overlap clip --jsonl
```

Key flags:

| flag | meaning |
| --- | --- |
| `--encoding` | force `UTF-8`, `UTF-16LE`, `UTF-16BE`, `GB18030` or `Big5` |
| `--fps` | positive integer fraction, e.g. `24000/1001` or `25` |
| `--overlap` | `keep` (report only), `clip` (shorten; zero-length cues dropped), `fail` |
| `--skip-bad` | skip malformed cues instead of failing the whole batch |
| `--max-delta-ms` | permitted round-trip timing deviation (default 42) |
| `--max-dropped` | permitted dropped/missing cues (default 0) |
| `--jsonl` | emit machine-readable JSON Lines instead of the human summary |

## Exit codes

| code | meaning |
| --- | --- |
| 0 | success |
| 2 | invalid arguments |
| 3 | decoding or parsing failure |
| 4 | deviation/drop/overlap threshold exceeded |
| 5 | file I/O failure |

## Guarantees and limits

- Input is capped at **64 MiB** and **200 000 cues per file**.
- Output goes to a same-directory temp file, fsyncs and is atomically renamed;
  a failed write never leaves a half-written target.
- Frame math is purely integer rational: start frames floor, end frames ceil,
  and a cue shorter than one frame still occupies exactly one frame.
- Decoding is strict: invalid byte sequences for the detected/declared
  encoding are rejected rather than replaced. When no BOM is present and the
  charset detector is not confident, `--encoding` is mandatory.
- ASS cues converted to SRT/VTT cannot keep their named styles; each such cue
  increments the `style_loss` counter.

### Known encoding boundary

GB18030 and Big5 share overlapping byte ranges. A BOM-less file containing
only a few CJK characters may be a byte-valid sequence in both encodings
(decoding to different but equally legal characters), so no tool can infer
the intended one from bytes alone. Pass `--encoding` explicitly for such
files; unambiguous cases (BOM, valid UTF-8, or invalid byte sequences) are
handled automatically.
