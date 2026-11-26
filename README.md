# replaykit

Local, deterministic Go media-processing kernel for replay clipping.

The library scans fragmented MP4 renditions, rebuilds sample/keyframe indexes,
reorders length-prefixed RTP audio (Opus or AAC), cuts event intervals per
bitrate layer on a shared rational clock, audits every anomaly, and resumes
from content-addressed checkpoints. `cmd/replay` is a thin terminal front end
that generates its own fixtures and prints verification counters.

Requirements: Go 1.23.12 (`go1.23.12` wrapper), macOS arm64/amd64, no services.
See `README` sections below for commands.
