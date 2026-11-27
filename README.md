# assetpack

Audio spec normalization and versioned, verifiable release manifests for MPEG-TS
assets. Every sample used by the demo is generated in Go; nothing is downloaded
or read from outside the work directory.

## Requirements

- Go 1.23 or newer (darwin/arm64 or any Go-supported platform)
- No network access at run time

Third-party modules are pinned in `go.mod`:

| module | version | used for |
| --- | --- | --- |
| `github.com/abema/go-mp4` | v1.7.4 | ISO-BMFF box writing and the container index |
| `github.com/pion/rtp` | v1.8.11 | RTP time-base input adaptation |

> The brief named go-mp4 `v1.25.0`, but that version does not exist on the module
> proxy (the newest published tag is v1.7.4), so the newest available release is
> pinned instead. pion/rtp is pinned at the requested v1.8.11.

## Build and test

```sh
go mod tidy
go build ./...
go test ./...
```

## Demo: a successful release

```sh
go run ./cmd/assetpack demo --work ./tmp/demo \
  --target-rate 48000 --target-layout stereo --lufs -16
```

Prints the five stage transitions, the resume boundary reached by each, the
fault summary, per-asset before/after frame counts and peaks, the diff against
the previously committed manifest, and the final manifest version plus SHA-256.
Exit code 0 means the release verified.

Run the same command again: it reports `added: 0`, `reused: 3` and the same
manifest digest.

## Demo: a damaged hand-off

```sh
go run ./cmd/assetpack demo --work ./tmp/broken \
  --inject truncated-ts,cc-jump,psi-update --fail-after manifest-stage
```

Three acts run in one invocation:

1. a healthy baseline release is committed;
2. the damaged inputs are processed — each injected fault is reported with its
   input offset, the source digest no longer matches the legacy manifest, and
   the attempt fails at `manifest-stage`; the rollback restores the baseline
   manifest byte for byte;
3. the clean inputs are processed again, resuming from the recorded boundary,
   and reach `commit` with the manifest still valid.

## Other commands

```sh
go run ./cmd/assetpack run    --work DIR --target-rate HZ --target-layout LAYOUT --lufs DB
go run ./cmd/assetpack verify --work DIR
```

`run` processes an existing `DIR/input` tree without regenerating fixtures.
`verify` re-hashes every committed artifact against the manifest.

## Layout

```
cmd/assetpack        terminal adapter
internal/cli         flags, fixture preparation, report rendering
internal/pipeline    five-stage orchestration
internal/migrate     durable stage journal and resume positions
internal/rollback    atomic staging / commit / rollback and crash recovery
internal/manifest    v1 reader, v2 schema, version adapter and idempotent merge
internal/audio       rate, layout and BS.1770 loudness normalization
internal/media       MP4 container writer/index reader, RTP clock adaptation
internal/ts          packet reader, PCR unwrap, PSI versions, PES demux
internal/clock       shared integer nanosecond / sample time base
internal/fault       the single structured error model
internal/fixture     deterministic TS, damaged variants and legacy manifests
```

## Guarantees

- PCRs are unwrapped modulo 2^33 only when PID continuity and time monotonicity
  prove a wrap; every packet keeps both its raw and unwrapped value.
- Damaged packets never contribute payload to PSI or audio; each is reported
  with the byte offset where it was observed.
- Audio gaps are padded and counted; a timeline is never shortened silently.
- Manifests are canonical: identical content yields an identical SHA-256.
- Commits and rollback copies are written through temp files and atomic
  renames; recovery yields either the complete old or the complete new release.
- Every path is confined to the work directory.
