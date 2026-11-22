# mediq — broadcast segment pipeline

`mediq` turns delivered H.264, H.265 and AV1 elementary streams plus
their keyframe indexes into broadcast-ready, transactionally committed
segment manifests, aligning multiple bitrate representations on one
unified 90 kHz timeline. Everything runs locally and offline: samples
are synthesized in-process, no external media tools or network calls
are used, and the only third-party dependencies are the pinned
`go-mp4` and `pion/rtp` modules.

## Architecture

Four cooperating subsystems, each in its own package under
`internal/`, share one time base (`internal/timeline`) and one error
model that always carries a stage, byte offset and recovery action.

| Subsystem | Packages | Responsibility |
| --- | --- | --- |
| Bitstream & timeline | `bitstream`, `timeline` | Annex B / AV1 unit parsing, RAP scanning, unified clock, error taxonomy |
| Index & segmentation | `index`, `segment` | Index validation/repair, segment planning, cross-representation alignment |
| Transactions | `journal`, `commit` | Write-ahead log, crash recovery, atomic same-directory commits |
| Orchestration & evidence | `pipeline`, `manifest`, `report`, `cmd/mediq` | End-to-end flow, manifest round trip, hashing, terminal UI |

Data flow:

```
raw stream scan -> unified timeline -> index repair -> segment plan
and cross-rep alignment -> transactional commit -> manifest round
trip and per-segment hash report
```

Synthetic units carry a 24-byte `MDIQ` meta header (sequence, PTS on
the unified clock, declared length, CRC32). H.264/H.265 NAL bodies are
emulation-prevention escaped so start codes cannot appear inside a
unit; AV1 uses low-overhead OBU framing with LEB128 sizes.

## Acceptance commands

```sh
go mod tidy
go build ./...
go test ./...
go run ./cmd/mediq demo    --work ./tmp/demo   --seed 23
go run ./cmd/mediq verify  --work ./tmp/demo
go run ./cmd/mediq demo    --work ./tmp/broken --seed 23 \
  --inject truncate,drop-index,corrupt-unit,misalign --fail-after commit
go run ./cmd/mediq recover --work ./tmp/broken
```

`scripts/demo.sh` runs the whole sequence in one go.

## Fault injection and boundary samples

Five boundary classes are generated deterministically from the seed:

* `truncate` — cuts the final unit so the scanner marks a truncated
  tail and never commits the incomplete unit; also exercises commit
  rollback when combined with a fail point.
* `corrupt-unit` — flips payload bytes so CRC validation quarantines
  the fully delimited unit into an isolated segment.
* `drop-index` — removes keyframe index rows; reconcile recovers the
  proven keyframes from the bitstream and propagates the repair into
  the segment plan and report.
* `duplicate-index` — repeats an index row; the duplicate is
  quarantined rather than used twice.
* `misalign` — shifts one representation's PTS beyond the configured
  tolerance without moving byte offsets; the whole group is rejected.

Fail points (`--fail-after scan|plan|commit`) stage work through the
WAL without committing. After a simulated crash, `recover` removes
pending temp files, leaves durable commits untouched and is idempotent.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Every invariant holds. |
| 2 | Input damage was detected, quarantined and safely committed. |
| 1 | Recovery or consistency failure, or an injected crash stopped the run. |

Note: `go run` on some newer Go toolchains maps any non-zero child exit
to `1`; the compiled binary (`go build -o mediq ./cmd/mediq`) returns
the exact business code above.

## Guarantees and limits

* Repeated runs over identical inputs produce identical manifests and
  per-segment SHA-256 values.
* Segments never overlap, timestamps are monotonic and boundary numbers
  are shared across representations within the configured skew.
* Manifest re-parse must reproduce the exact segment row sequence.
* Inputs are capped at 8 GiB with unit and RTP-packet resource quotas;
  timestamp conversions are overflow-checked on the 90 kHz clock.
