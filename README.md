# audiomix

Deterministic offline and streaming audio mixing for local level sessions.
The same mixing graph drives both render paths, so a session mixed offline
and a session streamed at any block size produce identical samples.

## Setup (macOS Terminal)

    python3.11 -m venv .venv
    .venv/bin/python -m pip install numpy==2.1.3 soundfile==0.12.1 pytest==8.3.3
    .venv/bin/python -m pip install -e .

## Demo

    .venv/bin/python -m audiomix generate-sample --dir ./.demo
    .venv/bin/python -m audiomix render \
        --manifest ./.demo/session.json \
        --output ./.demo/mix.wav \
        --report ./.demo/report.json \
        --rate 48000 --block-frames 257 --mode stream

The terminal summary prints the track count, output frames, and clip counts.
The JSON report holds, per track and for the bus and master, `peak`, `rms`,
`valid_frames`, and `clip_count`, plus each track's automation sample list.

Check that both paths agree:

    .venv/bin/python -m audiomix compare --manifest ./.demo/session.json \
        --rate 48000 --block-frames 257

Exit codes: `0` success, `2` input or processing error, `3` internal error.

## Manifest

    {
      "channels": 2,
      "sample_rate": 48000,
      "frames": 48000,
      "tracks": [
        {
          "name": "dialog",
          "path": "dialog.wav",
          "start_frame": 0,
          "gain_db": -3.0,
          "pan": -0.35,
          "channel_map": [0, 1],
          "loop": {"start_frame": 4000, "end_frame": 30000},
          "automation": [
            {"frame": 0, "value": -18.0, "unit": "db"},
            {"frame": 1200, "value": 0.0, "unit": "db"}
          ]
        }
      ]
    }

* Paths are relative to the manifest and may not escape it.
* `frames` sets the session length; without it the session ends at the latest
  track's first-cycle end.
* `loop` is a half-open range of the track's own resampled material.
* Automation frames are absolute project frames. One unit per curve
  (`linear` or `db`); values are clamped outside the keyframes.
* `--clip report` (default) leaves overs in the output and counts them;
  `--clip limit` clamps only the master.

## Layout

    src/audiomix/
      model.py       manifest, tracks, loops, channel maps
      automation.py  linear/dB curves, validation, sampling
      resample.py    deterministic linear resampling and frame-count rule
      mixer.py       shared mixing graph
      offline.py     whole-session driver
      stream.py      block driver
      stats.py       peak/RMS/valid frames/clip counts
      report.py      JSON report and terminal summary
      artifacts.py   atomic WAV + report staging
      sample.py      deterministic demo generator
      cli.py         argument parsing and exit codes
    tests/test_render.py   offline, validation, resampling, automation
    tests/test_stream.py   streaming parity, block edges, CLI, cleanup

Run the suite with `.venv/bin/python -m pytest`.
