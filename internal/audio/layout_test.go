package audio_test

import (
	"testing"

	"mediapack/internal/audio"
	"mediapack/internal/fixture"
	"mediapack/internal/ingest"
	"mediapack/internal/model"
)

func buildTracks(t *testing.T, s fixture.Spec) []*ingest.Track {
	t.Helper()
	dir := t.TempDir()
	p := dir + "/s.mp4"
	if err := writeFile(p, fixture.BuildMP4(s)); err != nil {
		t.Fatal(err)
	}
	c, err := ingest.ParseMP4(p)
	if err != nil {
		t.Fatal(err)
	}
	return c.Tracks
}

func TestNormalizeSupportedLayoutsAndRates(t *testing.T) {
	s := fixture.Spec{
		Video: fixture.VideoSpec{TrackID: 1, Timescale: 24000, Width: 8, Height: 8,
			Delta: 1001, Frames: 2},
		Audio: []fixture.AudioSpec{
			{TrackID: 2, Timescale: 48000, Channels: 2, SampleRate: 48000,
				SampleSize: 16, Delta: 1024, Samples: 2},
			{TrackID: 3, Timescale: 44100, Channels: 6, SampleRate: 44100,
				SampleSize: 16, Delta: 735, Samples: 2},
		},
	}
	n := audio.NewNormalizer(48000)
	for _, tr := range buildTracks(t, s) {
		if tr.Kind != ingest.TrackAudio {
			continue
		}
		st, err := n.Normalize(tr.Index, tr)
		if err != nil {
			t.Fatalf("supported layout failed: %v", err)
		}
		if !st.LayoutSupported || !st.NormalizationOK {
			t.Fatalf("normalization flags wrong: %+v", st)
		}
		if st.NormalizedRate != 48000 {
			t.Fatalf("normalized rate = %d", st.NormalizedRate)
		}
	}
	stats := n.Stats()
	if len(stats) != 2 {
		t.Fatalf("stats = %d, want 2", len(stats))
	}
	// Six-channel layout must expose the 5.1 channel map.
	if len(stats[1].ChannelMap) != 6 || stats[1].ChannelMap[3] != audio.ChLFE {
		t.Fatalf("5.1 map wrong: %+v", stats[1].ChannelMap)
	}
}

func TestUnsupportedLayoutFailsTrackOnly(t *testing.T) {
	s := fixture.Spec{
		Video: fixture.VideoSpec{TrackID: 1, Timescale: 24000, Width: 8, Height: 8,
			Delta: 1001, Frames: 2},
		Audio: []fixture.AudioSpec{
			// 3 channels is outside the supported {1,2,6} set.
			{TrackID: 2, Timescale: 48000, Channels: 3, SampleRate: 48000,
				SampleSize: 16, Delta: 1024, Samples: 2},
			{TrackID: 3, Timescale: 48000, Channels: 2, SampleRate: 48000,
				SampleSize: 16, Delta: 1024, Samples: 2},
		},
	}
	n := audio.NewNormalizer(48000)
	var failed, ok bool
	for _, tr := range buildTracks(t, s) {
		if tr.Kind != ingest.TrackAudio {
			continue
		}
		st, err := n.Normalize(tr.Index, tr)
		if err != nil {
			if !model.IsKind(err, model.KindUnsupported) || st.LayoutSupported {
				t.Fatalf("expected unsupported kind, got %v", err)
			}
			failed = true
			continue
		}
		ok = true
	}
	if !failed || !ok {
		t.Fatalf("one track must fail (got failed=%v) while another proceeds (ok=%v)",
			failed, ok)
	}
	if len(n.Stats()) != 2 {
		t.Fatal("failed track must still be recorded in stats")
	}
}

func TestZeroSampleRateRejected(t *testing.T) {
	s := fixture.Spec{
		Video: fixture.VideoSpec{TrackID: 1, Timescale: 24000, Width: 8, Height: 8,
			Delta: 1001, Frames: 2},
		Audio: []fixture.AudioSpec{
			{TrackID: 2, Timescale: 48000, Channels: 2, SampleRate: 0,
				SampleSize: 16, Delta: 1024, Samples: 2},
		},
	}
	n := audio.NewNormalizer(48000)
	for _, tr := range buildTracks(t, s) {
		if tr.Kind != ingest.TrackAudio {
			continue
		}
		if _, err := n.Normalize(tr.Index, tr); err == nil {
			t.Fatal("zero sample rate must fail")
		}
	}
}
