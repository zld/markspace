# loudcheck

Offline loudness and true-peak gate for music and ambience deliverables.
Pure Go, standard library only, no third-party runtime dependencies.

It measures ITU-R BS.1770-style integrated loudness (400 ms blocks, 100 ms
hop, -70 LUFS absolute gate, relative gate), the loudest 3 s short-term
window, 4x oversampled true peak, and the raw sample peak for diagnostics.
Nothing is played back and no window or device is opened.

## Build and test

```sh
brew install go && go install golang.org/dl/go1.22.12@latest && "$(go env GOPATH)/bin/go1.22.12" download
GO122="$(go env GOPATH)/bin/go1.22.12"
"$GO122" mod download
mkdir -p build .tmp/in
"$GO122" build -o ./build/loudcheck ./cmd/loudcheck
"$GO122" test ./...
```

## Demo

```sh
./build/loudcheck synth --out .tmp/in/demo.wav --layout 5.1 --seconds 12 --tone-dbfs -27
./build/loudcheck batch --input-dir .tmp/in --workers 4 \
  --target-lufs -23 --tolerance-lu 1 --max-true-peak-dbtp -1 --format text
```

The synthesized 5.1 tone is a -27 dBFS sine on every channel. LFE is
excluded from loudness and the five audible channels sum to about -22.4 LUFS,
which is inside the 1 LU window around -23. True peak stays at -27 dBTP.

## Commands

- `synth` writes a deterministic WAV (PCM16/24/32 or float32, mono through
  7.1, optional extensible headers and unusual chunk orders) and then
  verifies it with the same analyzer and thresholds used for real files.
- `analyze` measures one file.
- `batch` measures files and/or directories (one level of `.wav` discovery),
  with a bounded worker pool. Results are sorted by path and are identical
  for every worker count.

## Exit codes

| code | meaning |
| ---- | ------- |
| 0 | every file measured and passed |
| 1 | measurement completed, at least one threshold failed |
| 2 | bad arguments, unreadable input, or a layout that cannot be resolved |
| 3 | at least one file failed to parse or analyse |

A batch with one corrupt file still reports the healthy files and exits 3;
it is never reported as a clean pass.

## Measurement notes

- Layouts: mono, stereo, 5.1, 7.1. Surround channels use the 1.41 weight,
  LFE uses 0. Any other channel count requires `--map 1,1,1,0,...` with one
  weight per channel; a length mismatch is refused.
- Digital silence and material shorter than one 400 ms block report an
  invalid integrated loudness (`n/a`, JSON `null`) instead of a number.
  Material shorter than 3 s reports `short_term_insufficient` while keeping
  the integrated result.
- True peak interpolates three phases per sample with a windowed-sinc
  kernel. Interpolation only runs where the kernel is fully covered by real
  samples; the raw samples at both edges are still counted, so a one-sample
  transient at the start or end is reported. Silent padding is never used,
  because it would ring the boundary and inflate the peak.
- Each file gets a fresh analyzer; filter state never crosses file
  boundaries.
