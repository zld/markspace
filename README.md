# mediapack

A reusable, offline Go media-processing kernel for multi-platform release
packaging. It synthesizes project-subset MXF/MP4 track metadata — including
rotation, clean-aperture crop and conflicting color descriptions — into
traceable, re-verifiable off-screen frame sequences and independently
recoverable bitrate representations.

The tool is fully local: sample data is generated in memory from a seed, no
external files or services are referenced, and there is no GUI.

## Requirements

- Go 1.23.0 (the module's `go` directive is pinned to 1.23.0)
- Dependencies, pinned in `go.mod`:
  - `github.com/abema/go-mp4 v1.4.1` — MP4 box and track metadata parsing
  - `github.com/pion/rtp v1.8.27` — local representation packetization and
    loss/reorder/dedup recovery simulation

## Build, test and demo

```sh
go mod tidy
go test ./...
go build -o /tmp/mediapack ./cmd/mediapack

# self-generated sample with conflicting metadata, two bitrates, multichannel
/tmp/mediapack demo   --out /tmp/mp-demo --seed 17 --interrupt-after 2
/tmp/mediapack resume --dir /tmp/mp-demo
/tmp/mediapack verify --dir /tmp/mp-demo --repeat 2

# corrupt input: reports offset + field path, exit code 2, writes nothing
/tmp/mediapack demo   --out /tmp/mp-bad --seed 17 --corrupt mxf-klv-length
```

Exit codes: `0` success (an interrupted demo is a successful checkpointed
stop), `2` input/structural failure, `3` verification failure.

## Pipeline and stage machine

```
created -> parsed -> composed -> exported -> verified
```

Illegal jumps are rejected. A checkpoint records, per representation layer,
the last completed segment and frame sequence. A `resume` retransmits only the
packets behind open gaps from the original sender set — media is never
fabricated — and then re-verifies segment boundaries, frame ranges and the
SHA-256 manifest.

## Package layout

| Package       | Responsibility                                              |
| ------------- | ----------------------------------------------------------- |
| `model`       | cross-module structured errors (offset + field path), stage machine, provenance |
| `clock`       | integer 90 kHz tick timebase, floor conversion, alignment error |
| `ingest`      | MP4 box-boundary and MXF partition/KLV parsing with provenance |
| `fixture`     | deterministic in-memory MP4/MXF sample and corruption generation |
| `timeline`    | shared-clock composition with stable boundary ordering     |
| `audio`       | channel-layout and sample-rate normalization (fails one track only) |
| `render`      | fixed RGBA off-screen frames, crop/rotation, per-frame SHA-256 |
| `fanout`      | multi-bitrate pion/rtp layers, dedup, reorder, gap recovery |
| `recovery`    | per-layer checkpoints and resume anchors                    |
| `manifest`    | deterministic sealed manifest, checksum sidecar, verification, safe paths |
| `pipeline`    | orchestration of the resumable stage machine                |
| `cmd/mediapack` | terminal acceptance adapter (`demo`/`resume`/`verify`)    |

## Guarantees

- Rerunning identical inputs produces identical frame hashes and an identical
  manifest hash.
- The maximum track-to-tick alignment error is at most one tick.
- Conflicting color descriptions are retained and marked `unresolved`, never
  silently overwritten; a legal crop wins over the default full aperture.
- Corrupt inputs are rejected before any output write; already-verified
  artifacts cannot be overwritten.
- All output paths are confined to the chosen output directory; input
  dimensions are capped at 4096x4096.
