# mediad — aligned timed-metadata pipeline

`mediad` processes local MP4/fMP4 media and the timed metadata beside it
(subtitles, chapters, ID3, `emsg`, SEI) against an RTP stream, places every
rendition on one exact media timeline, and emits a reviewable segment manifest.
It is a pure terminal tool with no services, no network and no checked-in media:
all sample material is generated from a seed.

```
go build ./...
go test ./...
go run ./cmd/mediad demo --seed 7 --loss 0.04 --chunk-size 64 --report stdout
```

## What it guarantees

- **Exact time.** Every timestamp is an integer tick in a declared track time
  base and converted to seconds through exact rational arithmetic. No decision
  about ordering, alignment or hashing uses floating point. 32-bit wrap, late
  packets, reorders and duplicates are detected; an ambiguous half-window jump
  is reported with its raw tick rather than guessed.
- **Located errors.** A truncated box, an over-long subtitle or emsg string, a
  bad ID3 frame or an SEI payload that overruns its NAL is reported with file,
  byte offset, track and raw tick. Damaged metadata never enters the manifest.
- **Three-way identity.** Each input has a content, container and metadata
  digest. A same-path replacement is caught by content; a metadata edit keeps
  the sample index reusable but drops the metadata cache; a remux invalidates
  the index. A parser-version bump invalidates both.
- **Keyframe alignment.** Cuts are driven by the densest rendition's keyframes;
  every other rendition must have a keyframe within one video frame of each cut
  or the segment is reported (naming the rendition) and publication fails.
- **Bounded RTP recovery.** Sequence numbers are extended across the 65536 wrap.
  Duplicates count once; a late packet is accepted only inside the reorder
  window; a gap the window passes is recorded as unrecoverable, never
  fabricated into the stream.
- **Crash-safe batches.** Fixed-size work chunks are committed to a checkpoint
  after re-fingerprinting every input. A resumed run skips committed chunks and
  reproduces the continuous run's manifest digest, which the tests assert. A
  manifest is never published while incomplete or while carrying a fatal issue.

## Exit codes

| code | meaning |
|------|---------|
| 0 | completed |
| 1 | input or processing failure |
| 2 | usage error |

## The `demo` command

`demo` generates the acceptance scenario, runs it, and prints the figures:

```
av_sync_ms=...
loss_recovery_rate=...
latency_p95_ms=...
aligned_segments=...
index_reused=...
manifest_sha256=...
```

It then demonstrates the two negative paths: rewriting an input at the same
path prints `replacement_index_reused=false`, and each damaged input run on its
own is refused with its file and byte offset printed and `damaged_rejected=true`.

Manifest output: with `--report stdout` the terminal shows the summary and the
full manifest is written to `<work-dir>/manifest.json`; with `--report <file>`
it is also written there. The manifest digest is location-independent — the
same inputs and config produce the same digest under any work directory.

### Resume

```
go run ./cmd/mediad demo --seed 7 --loss 0.04 --chunk-size 64 --stop-after-chunks 2
go run ./cmd/mediad demo --seed 7 --loss 0.04 --chunk-size 64
```

The second invocation continues from the checkpoint and must print the same
`manifest_sha256` as a single continuous run.

## Go version

`github.com/pion/rtp v1.10.6` declares `go 1.24.0`, so a Go 1.23 toolchain cannot
compile it directly. With the default `GOTOOLCHAIN=auto`, installing `go@1.23`
and running `go mod tidy; go build ./...` makes the 1.23 binary fetch and run the
1.24 toolchain automatically; this is the supported path on the acceptance
machine. The module's own `go` directive is `1.24.0` to match the dependency.

## Layout

```
timeline/   exact rational timebase, timestamps, wrap handling, located errors, event set
index/      three-way fingerprints, parse index, chunk checkpoints, metadata cache
media/      MP4 box parsing, sample tables, subtitle/chapter/emsg/ID3/SEI, RTP, alignment
pipeline/   batch kernel: scan, chunked commit/resume, manifest
fixture/    deterministic MP4 and RTP generation (no external assets)
cmd/mediad  terminal entry point
```

## Scope

Local MP4/fMP4 and the specified timed metadata, with synthetic RTP fixtures.
Arbitrary transcoding and live transport are out of scope. The run statistics
(`av_sync_ms`, `loss_recovery_rate`, `latency_p95_ms`) are acceptance figures for
the generated samples, not a claim about arbitrary hardware.
