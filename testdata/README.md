# Generated fixtures

Nothing in this directory is committed media. Run

    go run ./testdata/generate.go --dir testdata/fixtures

to materialize the same deterministic set the CLI creates with
`--fixture generated`. Regenerating always produces byte-identical files.

| set | asset | exercises |
| --- | --- | --- |
| normal | normal_2997 | 29.97 video, DF labels in cue text, 12 cues |
| normal | normal_25 | 25 fps reel, 11 cues |
| normal | normal_50 | 50 fps reel, integer-timecode label, 8 cues |
| normal | normal_24 | 24 fps film reel, integer-timecode label, 10 cues |
| drift | drift_2997 | 29.97 with a +~333 ppm sample-clock drift |
| vfr | vfr_hold | real hold/dropped-frame intervals, WebVTT cues |
| truncated | truncated | MP4 cut inside mdat, cue past media end |
| wrap | rtp_wrap | RTP sequence and 32-bit timestamp wrap |
| rtp | rtp_lossy | gaps, late fills, duplicate, reorder, timestamp burst |
| subtitle | subtitle_bad | BOM, invalid/zero-length/truncated cues |
