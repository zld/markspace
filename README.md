# mediaforge

A reusable Go media-processing kernel for multi-camera event capture. It takes
RTP/RTCP packet streams from unstable networks — lossy, reordered, drifting
device clocks, wrapping timestamps, variable frame rate — and produces aligned,
indexed, hashed deliverable segments.

Everything runs locally. No external services, no real capture devices, and no
media files on disk: all fixtures are generated in Go.

## Quick start

```sh
brew install go
go mod tidy
go build ./...
go test ./...
go run ./cmd/mediadump --generate --seed 41 --loss 3 --reorder 7 \
    --drift-ppm 120 --wrap-at 4294967000 --vfr --segments 12
```

Run the last command twice: the per-segment digests are identical.

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Demo ran and every invariant held |
| 1 | Media damage or sync problems were correctly recorded, but a delivery check failed |
| 2 | Parameter, adapter-version, or usage error |

## Architecture

Packet flow, per stream, then across streams:

```
RTP packets
  -> internal/rtp        parse, sequence reorder window, duplicate/late reporting
  -> internal/recovery   XOR parity repair for single-packet loss
  -> internal/buffer     jitter buffer on the shared timeline, explicit gaps
  -> internal/clock      monotone timeline (wrap, drift), RTCP Sender Reports
  -> internal/syncer     audio/video alignment on one clock, bounded correction
  -> internal/adapter    versioned container parsing (MP4 v1), strict bounds
  -> internal/index      VFR keyframe index, deterministic cut points
  -> internal/segment    per-segment SHA-256 over aligned samples
  -> internal/pipeline   orchestration and the report
```

All stages share `internal/merrors`: every recorded problem carries its stage,
a stable category, and where possible an RTP sequence number or byte offset.

### Design points

- **Integer time only.** Timelines are `uint64` ticks at a fixed clock rate.
  No floating-point accumulation, so repeated runs are bit-identical.
- **Never a silent hole.** A missing packet becomes an explicit gap record with
  the sequence run and the timeline positions on each side.
- **Repair is exact or refused.** XOR parity restores one missing payload. Two
  losses in a group, missing parity, or a length mismatch are reported and the
  group is retired so later groups stay decodable.
- **Clock glitches are quarantined.** An implausible timestamp jump or Sender
  Report never touches the committed timeline; the estimator only re-anchors
  after two agreeing outliers show a genuine device re-baseline.
- **Out-of-sync is visible.** A correction beyond the configured maximum is
  refused, the track is flagged, and original timestamps are preserved.
- **Adapters fail before writing.** Unsupported versions and codecs are errors;
  truncated samples report the byte offset where their data should have begun.
- **Bounded everywhere.** Packet size, reorder window, jitter occupancy, FEC
  group count and bytes, sample count, and payload bytes all have hard limits.

## Performance

`BenchmarkProcess` in `internal/pipeline` measures end-to-end throughput on the
generated scenario. On an Apple M1 Ultra it runs ~495k packets/second, roughly
ten times the 50k packets/second target. Run it with:

```sh
go test -run xxx -bench BenchmarkProcess ./internal/pipeline/
```

## Testing

Each subsystem has independent tests, plus end-to-end tests in the pipeline
package that assert the demo's invariants and hash reproducibility. Fixtures
include normal, duplicate, reordered, single-loss, consecutive-loss, timestamp
wrap, implausible RTCP, and truncated-MP4 cases.
