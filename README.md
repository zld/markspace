# mkvmp4

Local Matroska → MP4 remux pipeline with byte-level structural evidence.
Everything — EBML parsing, timestamp rewriting, MP4 box/sample-table writing and
index verification — is implemented in this repository. NumPy 1.26.4 is used
only for integer time-base conversion and batch timestamp checks; no container
library, muxer or transcoder is involved.

## Setup (macOS, Python 3.11)

```sh
python3.11 -m venv .venv
.venv/bin/python -m pip install numpy==1.26.4
```

All later commands use `.venv/bin/python`, which also resolves the `mkvmp4`
package from the repository root (`mkvmp4` is a symlink to `src/mkvmp4`).

## Demo

```sh
.venv/bin/python scripts/make_fixture.py --out /tmp/sample.mkv
.venv/bin/python -m mkvmp4.cli inspect /tmp/sample.mkv --json
.venv/bin/python -m mkvmp4.cli remux /tmp/sample.mkv --out /tmp/sample.mp4 \
    --timescale 1000000 --offset-ms 250 --repair --json
.venv/bin/python -m mkvmp4.cli verify /tmp/sample.mp4
```

With the generated fixture this reports 44 EBML elements, 2 source tracks, 4
clusters (one with unknown size), 10 MP4 samples, one repaired non-monotonic
timestamp and `index_ok: true`.

### Track mapping

`remux` accepts an explicit table, which is validated strictly — every source
track must be covered, duplicate target IDs are rejected, and unsupported codecs
are reported per track with their byte offset:

```sh
.venv/bin/python -m mkvmp4.cli remux in.mkv --out out.mp4 --map 1:1,2:2
```

Without `--map`, an identity table (source number → same MP4 track ID) is
derived and reported as `mapping_derived: true`, both in JSON and in the
terminal table. Pass `--require-map` to make an absent or incomplete table a
hard error, or `--map` to pin IDs explicitly.

### Options that change the timeline

| Flag | Effect |
| --- | --- |
| `--timescale N` | MP4 timescale used for both `mdhd`/`stts` and `mvhd` |
| `--offset-ms N` | Uniform offset applied before scaling (may be negative) |
| `--rounding round\|floor\|ceil` | Conversion rounding; every inexact sample is counted |
| `--no-clip-negative` | Fail instead of clipping negative timestamps to zero |
| `--max-gap-ms N` | Strict mode: fail when a gap exceeds this |
| `--repair` | Repair backward/overlapping timestamps by +1 tick per sample |

A non-zero first sample timestamp is preserved with an `edts/elst` empty edit,
because `stts` deltas can only express spacing between samples.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success |
| 2 | Invalid arguments / option combination |
| 3 | Unsupported input structure or codec |
| 4 | Timestamp policy failure (strict mode, overflow, negative when clipping is off) |
| 5 | Output write or index verification failure |

Failures are printed as JSON with `error`, `message`, `context` (stage, input
byte `offset`, element `path`, `track`, `sample_index`) and `exit_code`.

## Supported codec subset

| Matroska CodecID | MP4 sample entry | Configuration |
| --- | --- | --- |
| `V_MPEG4/ISO/AVC` | `avc1` | `CodecPrivate` copied as `avcC` |
| `A_AAC` | `mp4a` | `CodecPrivate` wrapped in `esds` |
| `S_TEXT/UTF8` | `tx3g` | text samples copied verbatim |

Anything else is rejected with the track number and element offset; payload is
never re-encoded to fake a different codec.

## Layout

```
src/mkvmp4/
  ebml.py        VINT ID/size decoding, element headers, integer/float fields
  tree.py        element registry, bounded tree parse, unknown-size boundaries
  matroska.py    Segment/Info/Tracks, clusters, blocks, lacing, keyframes
  timestamps.py  ns conversion, offset, clipping, rounding, monotonic policies
  mp4.py         box writing, sample entries, sample tables, independent verifier
  pipeline.py    inspect/remux/verify orchestration, mmap payload streaming
  cli.py         argparse front end, JSON evidence, exit codes
  errors.py      typed errors carrying stage/offset/path/track/sample context
scripts/make_fixture.py   synthesises sample and edge-case Matroska inputs
tests/test_pipeline.py    unit + end-to-end tests (stdlib unittest, no pytest)
```

Run the tests with:

```sh
.venv/bin/python -m unittest discover -s tests
```

## Guarantees and limits

* Payload is streamed from a read-only `mmap` one sample at a time; media is
  never copied wholesale into memory and is byte-identical in the output.
* Output is written to a temporary file in the target directory and
  `os.replace`d only after the written file has been re-read and its sample
  offsets, table counts, durations and `mdat` bounds have been checked. Any
  failure leaves an existing target untouched.
* Parsing is bounded by `--max-depth` (default 64) and `--max-elements`
  (default 200000), so hostile nesting cannot exhaust resources.
* Only unknown-size **Segment** and **Cluster** are accepted; the terminating
  sibling is determined from the parent's allowed layout. Any other unknown-size
  element aborts with a located `BoundaryError`.
* `--timescale` is used for both the movie and all tracks; per-track
  retiming to a different movie timescale is intentionally not implemented
  rather than being done lossily.
* Chunk offsets switch to `co64` automatically when any offset exceeds 32 bits.
