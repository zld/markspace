# replaycheck

A Go media-processing kernel for cutting replay fragments out of a
multichannel PCM/WAV programme and a video track, while telling you exactly
what arrived intact.

## What it does

* parses WAV chunk structure, validates channels, sample rate, block
  alignment and frame completeness, and reports the first unreadable byte
  with its media time — nothing is padded or repaired;
* de-duplicates, re-orders and window-bounds a lossy RTP trace, detects
  32-bit timestamp wraps, and freezes committed spans so a late packet can
  never rewrite delivered history;
* scans a progressive MP4 sample table for sample timing, keyframes,
  keyframe intervals and the first sample payload missing from the file;
* localises WAV, MP4 and RTP damage as half-open intervals on one shared
  tick base;
* persists task progress per stage, resumes from the first incomplete
  stage, verifies the input digest before resuming and refuses stale input;
* cuts events with left-closed/right-open boundaries: audio edges floor to
  whole sample frames, video starts at the latest keyframe not after the
  event start and records the lead-in;
* merges results into a task-key-sorted manifest whose bytes are identical
  for identical task key, inputs and events, writing fragments and the
  manifest through temp files and atomic renames.

## Acceptance run

```
brew install go@1.23
go mod tidy && go build ./... && go test ./... && go run ./cmd/replaycheck --self-test --out /tmp/replaycheck
```

The command writes fixtures and fragments under the output directory and
prints one JSON report on stdout. Exit code 0 means every assertion passed.

The report contains, for the normal and damaged scenarios:

| field | meaning |
| --- | --- |
| `events[].audio_frames` | delivered whole sample frames |
| `events[].clip_sha256` | digest of the delivered audio and video fragments |
| `events[].frame_hash` | digest over coded video samples in decode order |
| `events[].pixel_mean` | mean payload byte value, 0..1 |
| `events[].audio_peak` | peak absolute PCM sample, 0..1 |
| `events[].alignment_error_ms` | sample-floor plus keyframe lead-in deviation |
| `packets_duplicate/reordered/lost` | transport counters |
| `keyframe_intervals` | ticks between consecutive keyframes |
| `damaged_offsets` | first unreadable byte of each damaged medium |
| `damaged_gaps` | damaged intervals with source and byte offset |
| `manifest_hash` | SHA-256 of the deterministic manifest |

`recovery` shows the stage the interrupted run resumed from, the stages it
reloaded from checkpoints, and confirms its manifest hash equals an
uninterrupted run of the same task. `partial_tail` shows how the
incomplete-trailing-frame fixture was localised.

## Layout

```
cmd/replaycheck/       CLI, self-test scenarios, fixture generation
internal/clock/        shared tick/sample/millisecond timeline
internal/fault/        severity-tagged issue model with locations
internal/wav/          RIFF/WAVE chunk parser, bounded PCM access
internal/rtp/          RTP dedup, reorder window, gap and wrap detection
internal/stream/       MP4 sample table scanner, deterministic MP4 builder
internal/repair/       damage interval index
internal/state/        persistent stage state machine
internal/checkpoint/   content-addressed per-stage checkpoints
internal/clip/         event cutter with sample and keyframe alignment
internal/manifest/     deterministic manifest merger
internal/metrics/      hashing and statistics probes
```

## Notes

* Everything runs locally; no network, no external media, no GUI.
* Generated artefacts live only under `--out`; source media is opened
  read-only and never rewritten.
* Per-task memory is bounded: PCM reads, payload reads and hashing all use
  fixed-size blocks, and clip sizes follow the event, not the programme.
