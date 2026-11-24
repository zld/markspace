package segment

import (
	"testing"

	"mediamerge/internal/model"
)

func seqSamples(track uint32, startNs int64, count int, durNs int64) []model.Sample {
	out := make([]model.Sample, count)
	for i := range out {
		out[i] = model.Sample{
			TrackID:    track,
			SeqNum:     uint32(i + 1),
			SourceFile: "a.mp4",
			Start:      model.NsTime(startNs + int64(i)*durNs),
			Dur:        model.NsDuration(durNs),
			Size:       10,
			Index:      uint32(i),
		}
	}
	return out
}

func TestSplitSnapsBackToLegalBoundary(t *testing.T) {
	// 5 x 1s samples, 2.5s limit: target 2.5s is inside sample 3, so the
	// legal cut is 2s. Then 4.5s -> 4s, then the final 1s segment.
	samples := seqSamples(2, 0, 5, 1_000_000_000)
	res := Split(samples, Options{MaxDuration: 2_500_000_000})
	if len(res.Segments) != 3 {
		t.Fatalf("segments=%d", len(res.Segments))
	}
	want := []model.Interval{
		{Start: 0, End: 2_000_000_000},
		{Start: 2_000_000_000, End: 4_000_000_000},
		{Start: 4_000_000_000, End: 5_000_000_000},
	}
	for i, w := range want {
		if res.Segments[i].Time != w {
			t.Fatalf("seg %d = %+v want %+v", i, res.Segments[i].Time, w)
		}
	}
	if res.Segments[0].BoundaryShift != -500_000_000 {
		t.Fatalf("shift=%d", res.Segments[0].BoundaryShift)
	}
	if res.MaxShiftNs != 500_000_000 {
		t.Fatalf("max shift=%d", res.MaxShiftNs)
	}
	// No sample straddles a boundary and none is lost.
	total := 0
	for _, s := range res.Segments {
		total += len(s.Samples)
		for _, smp := range s.Samples {
			if smp.Start < s.Time.Start || smp.End() > s.Time.End {
				t.Fatalf("sample %v outside %+v", smp, s.Time)
			}
		}
	}
	if total != 5 {
		t.Fatalf("samples=%d", total)
	}
}

func TestSplitExactBoundaryNoShift(t *testing.T) {
	samples := seqSamples(2, 0, 4, 1_000_000_000)
	res := Split(samples, Options{MaxDuration: 2_000_000_000})
	if len(res.Segments) != 2 {
		t.Fatalf("segments=%d", len(res.Segments))
	}
	for _, s := range res.Segments {
		if s.BoundaryShift != 0 {
			t.Fatalf("unexpected shift %d", s.BoundaryShift)
		}
	}
}

func TestSplitKeepsSourcesAndTracks(t *testing.T) {
	a := seqSamples(2, 0, 2, 1_000_000_000)
	v := seqSamples(1, 0, 2, 1_000_000_000)
	for i := range v {
		v[i].SourceFile = "v.mp4"
		v[i].SeqNum = uint32(i + 1)
	}
	res := Split(append(a, v...), Options{MaxDuration: 4_000_000_000})
	if len(res.Segments) != 1 {
		t.Fatalf("segments=%d", len(res.Segments))
	}
	seg := res.Segments[0]
	if len(seg.Tracks) != 2 || seg.Tracks[0] != 1 || seg.Tracks[1] != 2 {
		t.Fatalf("tracks=%v", seg.Tracks)
	}
	if len(seg.Sources) != 4 { // two seqs x two files
		t.Fatalf("sources=%v", seg.Sources)
	}
	for i := 1; i < len(seg.Sources); i++ {
		if seg.Sources[i-1].File > seg.Sources[i].File {
			t.Fatalf("sources unsorted: %v", seg.Sources)
		}
	}
}

func TestSplitOversizedSampleMakesProgress(t *testing.T) {
	samples := []model.Sample{
		{TrackID: 2, Start: 0, Dur: 5_000_000_000},
		{TrackID: 2, Start: 5_000_000_000, Dur: 1_000_000_000},
	}
	res := Split(samples, Options{MaxDuration: 2_000_000_000})
	if len(res.Segments) != 2 {
		t.Fatalf("segments=%d", len(res.Segments))
	}
	if res.Segments[0].Time.End != 5_000_000_000 {
		t.Fatalf("first end=%d", res.Segments[0].Time.End)
	}
}

func TestSplitEmptyAndDeterministic(t *testing.T) {
	if got := Split(nil, Options{MaxDuration: 1_000_000_000}); len(got.Segments) != 0 {
		t.Fatal("empty input produced segments")
	}
	samples := seqSamples(2, 0, 7, 700_000_000)
	a := Split(samples, Options{MaxDuration: 2_000_000_000})
	b := Split(samples, Options{MaxDuration: 2_000_000_000})
	if len(a.Segments) != len(b.Segments) {
		t.Fatal("segment count differs")
	}
	for i := range a.Segments {
		if a.Segments[i].Time != b.Segments[i].Time || len(a.Segments[i].Samples) != len(b.Segments[i].Samples) {
			t.Fatalf("segment %d differs", i)
		}
	}
}
