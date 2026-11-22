package timeline_test

import (
	"math"
	"path/filepath"
	"testing"

	"archive/internal/fixtures"
	"archive/internal/model"
	"archive/internal/parse"
	"archive/internal/timeline"
)

func loadTracks(t *testing.T) ([]*parse.Track, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	paths, corrupt, err := fixtures.Generate(dir, 17)
	if err != nil {
		t.Fatal(err)
	}
	var tracks []*parse.Track
	for _, p := range paths {
		var tr *parse.Track
		var err error
		if filepath.Ext(p) == ".mp4" {
			tr, err = parse.ParseMP4File(p)
		} else {
			tr, err = parse.ParseMXFFile(p)
		}
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		tracks = append(tracks, tr)
	}
	return tracks, corrupt
}

func alignAll(t *testing.T, tracks []*parse.Track) [][]timeline.AlignedSample {
	t.Helper()
	out := make([][]timeline.AlignedSample, len(tracks))
	for i, tr := range tracks {
		a, err := timeline.AlignTrack(i, tr)
		if err != nil {
			t.Fatalf("align track %d: %v", i, err)
		}
		out[i] = a
	}
	return out
}

func TestComposeCrossTrackInvariants(t *testing.T) {
	tracks, _ := loadTracks(t)
	segs, err := timeline.Compose(tracks, alignAll(t, tracks), timeline.DefaultPolicy())
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if len(segs) != 8 {
		t.Fatalf("expected 8 segments, got %d", len(segs))
	}
	for i, seg := range segs {
		if seg.Index != i || seg.Track != i {
			t.Fatalf("segment %d has index=%d track=%d", i, seg.Index, seg.Track)
		}
		// Samples strictly increasing and inside the segment interval.
		for j, s := range seg.Samples {
			if j > 0 && seg.Samples[j-1].Start.Cmp(s.Start) >= 0 {
				t.Fatalf("segment %d: non-monotonic sample %d", i, j)
			}
			ok, err := seg.Interval.Contains(s.Start)
			if err != nil || !ok {
				t.Fatalf("segment %d: sample %d start %s outside interval", i, j, s.Start)
			}
			// Unified position must be exact at 90 kHz.
			if _, err := s.Start.ToTicks(model.UnifiedScale); err != nil {
				t.Fatalf("segment %d sample %d: %v", i, j, err)
			}
		}
		// Transform record completeness.
		tr := seg.Transform
		if tr.Color.PolicyVersion != "v1" {
			t.Fatalf("segment %d: missing policy version", i)
		}
		if len(tracks[i].Colors) > 0 {
			found := false
			for _, c := range tr.Color.Original {
				if c == tr.Color.Chosen {
					found = true
				}
			}
			if !found {
				t.Fatalf("segment %d: chosen colour not among originals", i)
			}
		}
		if c := tr.Crop; c != nil {
			if c.X+c.W > tracks[i].Width || c.Y+c.H > tracks[i].Height {
				t.Fatalf("segment %d: crop escapes frame", i)
			}
		}
	}
	// Colour conflicts on tracks 3 and 6 must be resolved to BT.709.
	for _, i := range []int{3, 6} {
		if !segs[i].Transform.Color.Conflict {
			t.Fatalf("segment %d: expected colour conflict to be recorded", i)
		}
		if segs[i].Transform.Color.Chosen.Primaries != 1 {
			t.Fatalf("segment %d: prefer_bt709 did not choose BT.709", i)
		}
	}
	// Non-zero starts must survive on the unified base.
	if segs[2].Interval.Start.IsZero() {
		t.Fatal("track 2 non-zero media offset lost")
	}
	if segs[4].Interval.Start.IsZero() {
		t.Fatal("track 4 non-zero media offset lost")
	}
}

func TestComposeRejectsSingularMatrix(t *testing.T) {
	tracks, _ := loadTracks(t)
	aligned := alignAll(t, tracks)
	tracks[0].Matrix = [9]int32{} // zero matrix: not invertible
	_, err := timeline.Compose(tracks, aligned, timeline.DefaultPolicy())
	f, ok := model.AsFault(err)
	if !ok || f.Stage != model.StageCompose || f.Track != 0 {
		t.Fatalf("expected compose fault on track 0, got %v", err)
	}
}

func TestComposeRejectsOutOfBoundsCrop(t *testing.T) {
	tracks, _ := loadTracks(t)
	aligned := alignAll(t, tracks)
	tracks[0].Crop = &parse.Crop{X: 0, Y: 0, W: tracks[0].Width + 1, H: 10}
	_, err := timeline.Compose(tracks, aligned, timeline.DefaultPolicy())
	f, ok := model.AsFault(err)
	if !ok || f.Stage != model.StageCompose {
		t.Fatalf("expected compose fault, got %v", err)
	}
}

func TestAlignTimestampOverflow(t *testing.T) {
	_, corrupt := loadTracks(t)
	tr, err := parse.ParseMXFFile(corrupt["overflow_ts"])
	if err != nil {
		t.Fatalf("overflow fixture must parse: %v", err)
	}
	_, err = timeline.AlignTrack(6, tr)
	f, ok := model.AsFault(err)
	if !ok {
		t.Fatalf("expected align fault, got %v", err)
	}
	if f.Stage != model.StageAlign || f.Track != 6 {
		t.Fatalf("fault must carry stage=align track=6, got %v", f)
	}
}

func TestAlignRejectsHugeScaleConversion(t *testing.T) {
	tr := &parse.Track{
		Source: "synthetic", TimeScale: 1, MediaOffset: 0,
		Samples: []parse.Sample{
			{Index: 0, DTS: math.MaxInt64 - 10, Duration: 1, Keyframe: true},
		},
	}
	_, err := timeline.AlignTrack(0, tr)
	f, ok := model.AsFault(err)
	if !ok || f.Stage != model.StageAlign || f.Time == nil {
		t.Fatalf("expected align fault with time context, got %v", err)
	}
}
