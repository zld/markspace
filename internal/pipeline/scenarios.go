package pipeline

import (
	"math/rand"

	"mediarc/internal/demux"
	"mediarc/internal/model"
)

// buildScenarios generates the full demo fixture set from one seed:
// healthy DASH + HLS representations, a truncated-manifest case, a corrupt
// fragment case, a 32-bit wraparound case and a drift-over-limit case. The
// drift flag is driven by the CLI's --drift-ms so the acceptance commands
// exercise both under- and over-threshold behavior.
func buildScenarios(cfg Config, rng *rand.Rand) []Scenario {
	mkVideo := func(id string, base, step uint64, n int, mutate func(i int, dts uint64) uint64) []model.Track {
		var samples []demux.TSFrame
		dts := base
		for i := 0; i < n; i++ {
			payload := make([]byte, 16)
			rng.Read(payload)
			if i%4 == 0 {
				payload[0] = 0x65 // H.264 IDR hint
			}
			samples = append(samples, demux.TSFrame{
				PTS: dts, DTS: dts, Payload: payload, Key: i%4 == 0,
			})
			dts = mutate(i, dts) + uint64(step)
		}
		audio := []demux.TSFrame{
			{PTS: 0, DTS: 0, Payload: []byte{0xAA, 0xBB}},
			{PTS: 1024, DTS: 1024, Payload: []byte{0xCC, 0xDD}},
		}
		data := demux.WriteMPEGTS(samples, audio)
		tracks, err := demux.NewTSDemuxer(id + ".ts").Demux(data)
		if err != nil {
			// Fixture generation is deterministic and must never fail; keep
			// the failure visible rather than silently dropping tracks.
			panic("fixture demux failed for " + id + ": " + err.Error())
		}
		return tracks
	}

	plain := func(i int, dts uint64) uint64 { return dts }
	out := []Scenario{
		{
			Rep:    model.Representation{ID: "dash-720p", Format: "dash", Timescale: 90000},
			Tracks: mkVideo("dash-720p", 0, 3000, 12, plain),
		},
		{
			Rep:    model.Representation{ID: "hls-variant-a", Format: "hls", Timescale: 90000},
			Tracks: mkVideo("hls-variant-a", 90000, 3000, 12, plain),
		},
	}

	// Truncated manifest: representation carries a manifest error and no
	// recoverable fragment. Recovery stays isolated to this item.
	out = append(out, Scenario{
		Rep: model.Representation{
			ID: "hls-truncated", Format: "hls", Timescale: 90000,
			ManifestErrors: []*model.MediaError{
				model.AtLine(model.KindManifestTruncated, "truncated.m3u8", 7, "playlist ends before ENDLIST"),
			},
		},
		Truncated: true,
	})

	// Corrupt fragment: a healthy-looking rep whose bytes fail demux. The
	// tracks are still generated so the pipeline exercises the isolation path
	// rather than a nil input.
	corruptTracks := mkVideo("dash-corrupt", 0, 3000, 6, plain)
	out = append(out, Scenario{
		Rep:    model.Representation{ID: "dash-corrupt", Format: "dash", Timescale: 90000},
		Tracks: corruptTracks, Corrupt: true,
	})

	// Wraparound: timestamps climb to the 32-bit ceiling and restart on the
	// other side, exercising both the carrier unwrap and the full-width DTS
	// detection path.
	wrapTracks := mkWrapTracks("dash-wrap", rng)
	out = append(out, Scenario{
		Rep:    model.Representation{ID: "dash-wrap", Format: "dash", Timescale: 90000},
		Tracks: wrapTracks, Wrap: true,
	})

	// Drift over the configured threshold: actual DTS steps grow 8% while the
	// declared sample durations stay at the nominal 3000 ticks, producing a
	// measurable positive slope error against the nominal clock.
	driftTracks := mkDriftTracks("hls-drift", rng, 16, 3000, 3240)
	if cfg.DriftMs > 0 {
		out = append(out, Scenario{
			Rep:    model.Representation{ID: "hls-drift", Format: "hls", Timescale: 90000},
			Tracks: driftTracks, Drift: true,
		})
	}

	return out
}

// mkWrapTracks builds a stream whose DTS crosses the 32-bit carrier ceiling:
// the post-wrap timestamps restart near zero, which is the full-width wrap
// signature the recovery layer must expand while keeping raw values intact.
// Values stay within 32 bits so the same samples still round-trip through the
// 32-bit duration fields of fragmented MP4.
func mkWrapTracks(id string, rng *rand.Rand) []model.Track {
	const step = uint64(3000)
	const ceiling = uint64(0xFFFFFFFF)
	dtsSeq := []uint64{
		ceiling - 2*step,
		ceiling - step,
		ceiling - 1,
		step - 1, // first post-wrap timestamp
		2*step - 1,
		3*step - 1,
	}
	var frames []demux.TSFrame
	for i, dts := range dtsSeq {
		payload := make([]byte, 16)
		rng.Read(payload)
		if i == 0 || i == 3 {
			payload[0] = 0x65
		}
		frames = append(frames, demux.TSFrame{PTS: dts, DTS: dts, Payload: payload, Key: i == 0 || i == 3})
	}
	data := demux.WriteMPEGTS(frames, nil)
	tracks, err := demux.NewTSDemuxer(id + ".ts").Demux(data)
	if err != nil {
		panic("wrap fixture demux failed: " + err.Error())
	}
	// Declare nominal constant durations so remux duration fields stay valid.
	for ti := range tracks {
		for si := range tracks[ti].Samples {
			tracks[ti].Samples[si].Duration = uint32(step)
		}
	}
	return tracks
}

// mkDriftTracks builds a TS video stream whose real inter-sample DTS step
// (actualStep) diverges from the nominal frame duration (nominalDur), so the
// clock estimator sees a constant slope drift while VFR stays false.
func mkDriftTracks(id string, rng *rand.Rand, n int, nominalDur, actualStep uint64) []model.Track {
	var frames []demux.TSFrame
	var dts uint64
	for i := 0; i < n; i++ {
		payload := make([]byte, 16)
		rng.Read(payload)
		if i%4 == 0 {
			payload[0] = 0x65
		}
		frames = append(frames, demux.TSFrame{PTS: dts, DTS: dts, Payload: payload, Key: i%4 == 0})
		dts += actualStep
	}
	data := demux.WriteMPEGTS(frames, nil)
	tracks, err := demux.NewTSDemuxer(id + ".ts").Demux(data)
	if err != nil {
		panic("drift fixture demux failed: " + err.Error())
	}
	// Overwrite the derived durations with the nominal frame duration: the
	// drift is the gap between the declared clock and the actual timestamps.
	for ti := range tracks {
		for si := range tracks[ti].Samples {
			tracks[ti].Samples[si].Duration = uint32(nominalDur)
		}
	}
	return tracks
}
