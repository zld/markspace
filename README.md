# mediatool

A reusable Go kernel for turning multi-track RTP/RTCP captures into aligned,
multi-bitrate ISO BMFF segments and a stable consumption manifest. It locates
damaged packets by byte offset, resumes from checkpoints, and proves that the
same input and parameters always produce the same output.

Everything runs locally. There are no network or device dependencies.

## Quick start

```sh
go mod tidy
go build ./...
go test ./...
go run ./cmd/mediatool demo --seed 42 --tracks 4 --bitrates 3 \
  --inject-loss 7 --inject-truncation 1 --interrupt-after 2
```

The demo prints a machine-readable summary:

```
tracks=4
representations=3
loss_packets=7
corruptions=1
damage_offset_bytes=292912
offset_error_bytes=0
resume_ms=393
manifest_segments=7
alignment_skew_ms=20
output_sha256=<64 hex digits>
```

`output_sha256` and `manifest_segments` are identical across repeated runs of
the same command.

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Processing and verification succeeded |
| 2 | Input was damaged but the damage was located precisely |
| 3 | Unrecoverable metadata or alignment error |
| 4 | Checkpoint and input digest disagree; resume was refused |

## Commands

- `demo` — generate a capture, scan, index, align, segment, interrupt, resume,
  and verify. Flags: `--seed --tracks --bitrates --inject-loss
  --inject-truncation --interrupt-after --workdir --keep`.
- `scan` — generate a capture and print every located fault by byte offset.
- `verify` — exercise all four self-generated fault classes (reorder/duplicate,
  truncation, RTCP regression, index digest mismatch).
- `resume` — interrupt then resume; `--corrupt-checkpoint` demonstrates the
  exit-code-4 refusal path.

## Architecture

```
cmd/mediatool          terminal entry point
internal/fault         located error model + exit-code contract
internal/clock         RTP timestamp extension, RTCP mapping, trust windows
internal/rtp           streaming capture scanner (pion/rtp) and sequence tracking
internal/index         digest-bound index, scan pipeline, persistence
internal/audio         explicit channel maps, integral-only rate boundaries
internal/video         priority-based color normalization
internal/segment       shared-boundary alignment with keyframe search budget
internal/stream        bounded-window runner, checkpoints, resume, segmentation
internal/manifest      deterministic sorted manifest with bytes-derived digests
internal/media         fragmented ISO BMFF writing via mp4ff
internal/fixture       in-code capture and fault-sample generation
```

### Data flow

```
RTP/RTCP capture
   -> scanner: packet events with byte offsets, loss/reorder/dup/truncation
   -> timeline: wrap-extended RTP time, RTCP-mapped media time, trust windows
   -> index: per-track packet range, time range, keyframes, layout/color
   -> runner: shared-boundary alignment, bounded segment rendering, checkpoints
   -> manifest: sorted representations, segments, faults, digests
```

### Capture format

Generated and scanned captures use a minimal local framing so a truncated
datagram's declared length survives even when its body was cut short:

```
file   := "MTCAPv1\n" record*
record := capturedLen uint32 | originalLen uint32 | body[capturedLen]
```

A record whose captured length is below its declared length is a truncated
datagram. Packet offsets refer to the body, so fault offsets stay within one
RTP header length of the packet they describe.

## Key guarantees

- **Located damage.** Every loss, reorder, duplicate, truncation, or unusable
  RTCP report carries the first suspicious byte offset. Truncation offsets are
  verified to be within one RTP header (12 bytes) of ground truth.
- **No silent zeroing.** A track with no sender report is reported invalid, and
  a non-monotonic report opens an untrusted interval instead of resetting time.
  Alignment refuses keyframes that fall inside an untrusted interval.
- **Digest-bound index.** The index records the SHA-256 of the exact input and
  its own checksum. Loading against a different input is refused.
- **Explicit audio mapping.** Layout conversion requires a map that accounts for
  every source channel; a silent drop is a fatal layout fault. Non-integral
  sample-rate boundaries are rejected.
- **Deterministic output.** Manifest field order, track/segment ordering, and
  checksums are stable. Segment SHA-256 values are computed from the bytes
  actually written.
- **Bounded memory.** The scanner holds at most one reorder window plus retained
  keyframes; the runner reads one segment span at a time and never buffers the
  whole scene.

## Dependencies

- `github.com/pion/rtp` v1.8.11 — RTP parsing
- `github.com/Eyevinn/mp4ff` v0.47.0 — ISO BMFF init and media segments
