package mediacore

import (
	"path/filepath"
	"testing"
)

// genFixtures generates the deterministic fixture set into the test temp dir.
func genFixtures(t *testing.T, seed int64) *FixturePaths {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "fx")
	p, err := GenerateFixtures(dir, seed)
	if err != nil {
		t.Fatalf("GenerateFixtures: %v", err)
	}
	return p
}

func TestDemuxReportsTrackMetadataAndCounts(t *testing.T) {
	p := genFixtures(t, 17)
	c, err := DemuxFile(p.MP4, DefaultDemuxOptions())
	if err != nil {
		t.Fatalf("DemuxFile normal: %v", err)
	}
	if c.Brand != "isom" {
		t.Fatalf("brand = %q, want isom", c.Brand)
	}
	if c.MovieTimescale != 1000 {
		t.Fatalf("movie timescale = %d, want 1000", c.MovieTimescale)
	}
	if len(c.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(c.Tracks))
	}
	v := c.TrackByID(1)
	a := c.TrackByID(2)
	if v == nil || a == nil {
		t.Fatalf("expected tracks 1 and 2, got %+v", c.Tracks)
	}
	if v.Kind != KindVideoTrack || v.Codec != "avc1" || v.Timescale != 90000 {
		t.Fatalf("video metadata wrong: %+v", v)
	}
	if v.SampleCount() != 40 {
		t.Fatalf("video samples = %d, want 40", v.SampleCount())
	}
	if v.StartTicks != 0 || v.EndTicks != 120000 {
		t.Fatalf("video start/end = %d/%d, want 0/120000", v.StartTicks, v.EndTicks)
	}
	if a.Kind != KindAudioTrack || a.Codec != "mp4a" || a.Timescale != 48000 {
		t.Fatalf("audio metadata wrong: %+v", a)
	}
	if a.SampleCount() != 64 {
		t.Fatalf("audio samples = %d, want 64", a.SampleCount())
	}
	if a.EndTicks <= a.StartTicks {
		t.Fatalf("audio end %d must exceed start %d", a.EndTicks, a.StartTicks)
	}
	// Every sample offset must resolve inside an mdat and carry a positive size.
	for _, tr := range c.Tracks {
		for i, s := range tr.Samples {
			if s.Size <= 0 {
				t.Fatalf("track %d sample %d non-positive size %d", tr.ID, i, s.Size)
			}
			if s.Offset <= 0 {
				t.Fatalf("track %d sample %d bad offset %d", tr.ID, i, s.Offset)
			}
		}
	}
}

func TestDemuxQuickTimeMOV(t *testing.T) {
	p := genFixtures(t, 17)
	c, err := DemuxFile(p.MOV, DefaultDemuxOptions())
	if err != nil {
		t.Fatalf("DemuxFile mov: %v", err)
	}
	if c.Brand != "qt  " {
		t.Fatalf("mov brand = %q, want qt", c.Brand)
	}
	if len(c.Tracks) != 2 {
		t.Fatalf("mov tracks = %d, want 2", len(c.Tracks))
	}
}

func TestDemuxUnknownTrackKeepOrReject(t *testing.T) {
	p := genFixtures(t, 17)

	kept, err := DemuxFile(p.Unknown, DemuxOptions{AllowUnknownTracks: true, ValidateOffsets: true})
	if err != nil {
		t.Fatalf("keep-unknown demux: %v", err)
	}
	var other *Track
	for i := range kept.Tracks {
		if kept.Tracks[i].Kind == KindOtherTrack {
			other = &kept.Tracks[i]
		}
	}
	if other == nil {
		t.Fatalf("expected an other/unknown track, got %+v", kept.Tracks)
	}
	if !other.Unknown || other.SampleCount() != 3 {
		t.Fatalf("unknown track not preserved: unknown=%t samples=%d", other.Unknown, other.SampleCount())
	}

	if _, err := DemuxFile(p.Unknown, DemuxOptions{AllowUnknownTracks: false, ValidateOffsets: true}); !IsKind(err, KindContainer) {
		t.Fatalf("reject-unknown expected KindContainer, got %v (%q)", err, KindOf(err))
	}
}

func TestDemuxFaultsAreClassified(t *testing.T) {
	p := genFixtures(t, 17)
	cases := []struct {
		name string
		path string
	}{
		{"truncated_moov", p.Truncated},
		{"bad_offset", p.BadOffset},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DemuxFile(tc.path, DefaultDemuxOptions())
			if err == nil {
				t.Fatalf("%s: corrupt input was accepted", tc.name)
			}
			if KindOf(err) != KindContainer {
				t.Fatalf("%s: kind = %q, want container (err=%v)", tc.name, KindOf(err), err)
			}
		})
	}
}

func TestSampleReaderStreamsExactBytes(t *testing.T) {
	p := genFixtures(t, 17)
	c, err := DemuxFile(p.MP4, DefaultDemuxOptions())
	if err != nil {
		t.Fatal(err)
	}
	v := c.TrackByID(1)
	r, err := NewSampleReader(p.MP4, v)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for i := 0; i < v.SampleCount(); i++ {
		b, err := r.SampleBytes(i)
		if err != nil {
			t.Fatalf("SampleBytes %d: %v", i, err)
		}
		if int64(len(b)) != v.Samples[i].Size {
			t.Fatalf("sample %d len %d != size %d", i, len(b), v.Samples[i].Size)
		}
		h1, err := r.SampleHash(i)
		if err != nil {
			t.Fatalf("SampleHash %d: %v", i, err)
		}
		if h2 := sha256Bytes(b); h1 != h2 {
			t.Fatalf("sample %d hash mismatch: reader=%s bytes=%s", i, h1, h2)
		}
	}
	if _, err := r.SampleBytes(v.SampleCount()); !IsKind(err, KindIndex) {
		t.Fatalf("out of range sample expected KindIndex, got %v", err)
	}
}
