# Media Archive Kernel

A reusable Go media-processing kernel for long-term media archiving and
historical asset recovery. It unifies subtitle, chapter and timed-metadata
tracks (ID3, emsg, SEI) that use **different time bases**, survives
duplicated, reordered and burst-lost network packets, replays late events
against the committed timeline, and exports a deterministic, reviewable
frame sequence and loudness summary — entirely offline, with no graphics
device and no external service.

## Quick start (macOS Terminal)

```sh
brew install go                      # Go 1.23 or newer
cd <project root>
go mod tidy && go build ./... && go test ./...
go run ./cmd/archive -fixture normal -seed 17 -out /tmp/archive-a
go run ./cmd/archive -fixture normal -seed 17 -out /tmp/archive-b
diff /tmp/archive-a/manifest.json /tmp/archive-b/manifest.json   # identical
```

All fixtures are generated in-process; nothing is read from disk or the
network.

## Fixtures

| `-fixture`   | Meaning |
|--------------|---------|
| `normal`     | Well-formed, in-order archive. Clean exit **0**. |
| `truncated`  | MP4 structure cut inside a fragment: reports track + byte offset, incomplete track is dropped, others still export. Exit **2**. |
| `damaged`    | RTP duplicates, reordering and burst loss: unresolved gaps > 0, frames still export. Exit **2**. |
| `late`       | Packets delivered after their hole was declared lost: triggers checkpoint replay; final state hash equals the fully ordered run. Exit **0**. |
| `boundary`   | Timestamps at zero, one tick, the last representable instant and beyond the 24h range: the out-of-range item is reported and skipped. Exit **2**. |

Flags:

```
-fixture   normal | truncated | damaged | late | boundary   (default normal)
-seed      deterministic generation seed                     (default 17)
-out       required output root; manifest is <out>/manifest.json
-late-window  allowed event lateness, e.g. 2s or 500ms       (default 2s)
-v         print per-track and per-error detail
```

## Exit codes

| Code | Meaning |
|------|---------|
| 0    | Success. |
| 2    | Input was damaged but partially recovered (unresolved gaps or a truncated document). |
| 1    | Fatal: unusable time base, exceeded limit, or a write/path failure. |
| 64   | Usage error. |

> Note: `go run` may report a bare `1` for any non-zero exit on some
> toolchains. Build the binary (`go build -o archive ./cmd/archive`) if you
> need to observe the exact code.

## Architecture

```
cmd/archive/main.go          command adapter: flags, fixture generation,
                             printing, exit codes only

internal/events/types.go     shared event / gap / error model; canonical
                             state serialisation and total event ordering
internal/clock/base.go       unified integer-nanosecond time base; 32-bit
                             wrap unwrapping, timescale validation, ranges
internal/rtp/reorder.go      sequence de-duplication, reorder buffering,
                             watermark-driven loss declaration, late queue
internal/metadata/parse.go   MP4 box parsing (go-mp4 for emsg; documented
                             layouts for generated timed boxes), ID3/SEI
                             codec decoding, byte-offset error reporting
internal/timeline/watermark.go  event-time watermark and commit prefix
internal/timeline/replay.go     checkpoint rollback for late events
internal/render/frames.go    offscreen deterministic frame sequence
internal/audio/metrics.go    synthetic PCM and finite peak/RMS loudness
internal/manifest/hash.go    aggregate SHA-256 manifest, atomic write
internal/fixture/            deterministic MP4 and RTP generation
internal/archive/pipeline.go wires the subsystems into one run
```

### Data flow

1. The fixture generator builds an MP4 document whose `moov` declares one
   `trak` per track, each with its **own `mdhd` timescale** (video 30 kHz,
   audio 48 kHz, subtitle/chapter/ID3 1 kHz, SEI 30 kHz), plus `emsg` boxes
   carrying an **independent 90 kHz clock**. It also emits an RTP stream.
2. RTP packets pass through the reorderer: duplicates are counted once,
   holes behind the watermark are declared lost exactly once, and a packet
   that fills such a hole is queued as late rather than overwriting the loss.
3. The metadata parser and the RTP decoder produce events in **one unified
   nanosecond time base**; each conversion failure carries the track, input
   sequence and byte offset.
4. Both streams are admitted in one canonical media-time order, so the
   watermark (`max event time − allowed lateness`) advances once. Events at
   or below it commit; anything after waits.
5. A late event that lands inside the committed prefix rolls back to the
   nearest checkpoint, re-derives the committed projection purely from the
   accepted log, and reports which outputs changed.
6. The committed timeline is rendered into an RGB565 frame sequence; each
   frame is hashed and its pixel mean/min/max and frame-to-event alignment
   error are measured. Audio samples are synthesised and peak/RMS loudness
   measured. The manifest aggregates all of it and is written atomically.

## Determinism guarantees

- `Unwrap32` and the RTP sequence unwrap are pure functions of `(anchor, raw)`
  — arrival order can never change a timestamp.
- Event ordering is a total order over `(time, track, kind, seq, text,
  payload digest)`; diagnostics (source name, byte offset) are excluded from
  the canonical state.
- Frames are a pure function of committed events and frame index; audio is a
  pure function of committed events and sample index.
- The manifest contains no paths, PIDs, timestamps or wall-clock values, and
  sorts every list deterministically.

## Verification

```sh
./scripts/check.sh        # full acceptance procedure with pass/fail per item
./scripts/verify.sh       # same procedure, fails fast on the first error
```

`check.sh` proves: normal manifests are byte-identical across runs;
truncated input reports track and offset while other tracks export; damaged
input reports loss and unresolved gaps while frames still export; the late
replay state hash equals the ordered run; boundary input reports its
out-of-range timestamp.

## Engineering constraints honoured

- Local archive material and parsable MP4 timed metadata only; unrecoverable
  payloads are never repaired and lost content is never fabricated.
- Single-input and replay windows are bounded and produce explicit errors.
- Output goes through a temp file followed by `rename`; paths are confined
  and `..`/`~` are rejected.
- `go.mod` pins dependency versions; `go.mod` + `go.sum` are committed.

## Dependency note

The brief referenced `go-mp4 v1.15.0`; that version does not exist upstream.
The newest published tag is **v1.7.4**, which is what `go.mod` pins. It
provides full `emsg`/`mdhd`/`hdlr`/`tfhd` parsing. `pion/rtp` is pinned at
**v1.8.12** as requested.
