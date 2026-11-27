package pipeline

import (
	"mediacore/internal/archive"
	"mediacore/internal/bitstream"
	"mediacore/internal/clock"
	"mediacore/internal/media"
	"mediacore/internal/syncer"
)

// synchronize builds both presentation timelines and aligns them.
func synchronize(stream *bitstream.Stream, syn media.Synthetic) (*syncer.Result, error) {
	frameInterval := clock.Second / clock.Time(syn.FPS)
	vSamples := make([]syncer.Sample, 0, len(stream.Frames))
	for i := range stream.Frames {
		vSamples = append(vSamples, syncer.Sample{PTS: clock.Time(i) * frameInterval, Duration: frameInterval})
	}
	audioInterval := 1024 * clock.Second / 48000
	var aSamples []syncer.Sample
	end := clock.Time(syn.Seconds) * clock.Second
	for t := clock.Time(0); t < end; t += audioInterval {
		aSamples = append(aSamples, syncer.Sample{PTS: t, Duration: audioInterval})
	}
	return syncer.Sync(
		syncer.Track{Kind: "video", Samples: vSamples},
		syncer.Track{Kind: "audio", Samples: aSamples},
		syncer.DefaultConfig(),
	)
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }
func round3(v float64) float64 { return float64(int64(v*1000+0.5)) / 1000 }

// fragmentDriftMS reports the largest absolute difference between a declared
// track duration (minus its edit offset) and the nominal clip length.
func fragmentDriftMS(t *archive.FragmentTiming, syn media.Synthetic) float64 {
	expected := clock.Time(syn.Seconds) * clock.Second
	var worst clock.Time
	for _, tr := range t.Tracks {
		d := tr.Duration - tr.EditOffset
		if d < 0 {
			d = -d
		}
		diff := d - expected
		if diff < 0 {
			diff = -diff
		}
		if diff > worst {
			worst = diff
		}
	}
	return clock.ToMilliseconds(worst)
}
