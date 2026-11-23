package timeline_test

import (
	"testing"

	"mediacore/internal/fixture"
	"mediacore/internal/ingest"
	"mediacore/internal/timeline"
)

func scanFragments(t *testing.T, data []byte) (*ingest.InitInfo, []ingest.Fragment) {
	t.Helper()
	r, f := ingest.NewReader("s.fmp4", data, ingest.DefaultLimits())
	if f != nil {
		t.Fatal(f)
	}
	info, f := ingest.ParseInit(r)
	if f != nil {
		t.Fatal(f)
	}
	idx, _ := ingest.ScanIndex(r, info.MoovOffset+info.MoovSize)
	res := r.ScanFragmentsBounded(info.MoovOffset+info.MoovSize, idx)
	return info, res.Fragments
}

func TestMappingUsesEachTrackTimescale(t *testing.T) {
	stream, err := fixture.Build(fixture.DefaultSpec(41, 2, 8))
	if err != nil {
		t.Fatal(err)
	}
	info, frags := scanFragments(t, stream.Data)
	tracks, errs := timeline.BuildTracks([]*ingest.InitInfo{info}, frags, 90000)
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(tracks) != 2 {
		t.Fatalf("tracks=%d", len(tracks))
	}
	for _, tr := range tracks {
		if tr.Mapping.SourceTimescale != tr.Timescale {
			t.Fatalf("track %d source timescale %d != declared %d", tr.ID, tr.Mapping.SourceTimescale, tr.Timescale)
		}
		for _, s := range tr.Samples {
			// Exact integer relation: timeline == source * 90000 / timescale
			want := s.SourceTime * 90000 / int64(tr.Timescale)
			if s.TimelineTime != want {
				t.Fatalf("track %d sample %d timeline=%d want %d", tr.ID, s.SampleIndex, s.TimelineTime, want)
			}
		}
	}
	// Audio at 48 kHz must land on the same shared grid as video at 90 kHz.
	audio := tracks[1]
	if audio.Timescale != 48000 {
		t.Fatalf("audio timescale=%d", audio.Timescale)
	}
	if second := audio.Samples[1]; second.TimelineTime != 1920 {
		t.Fatalf("audio sample 1 at %d want 1920", second.TimelineTime)
	}
}

func TestMappingPreservesNonZeroSourceStart(t *testing.T) {
	spec := fixture.DefaultSpec(9, 2, 4)
	spec.Tracks[0].SourceStart = 90000 // video starts one second in
	spec.Tracks[1].SourceStart = 48000
	stream, err := fixture.Build(spec)
	if err != nil {
		t.Fatal(err)
	}
	info, frags := scanFragments(t, stream.Data)
	tracks, errs := timeline.BuildTracks([]*ingest.InitInfo{info}, frags, 45000)
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	for _, tr := range tracks {
		if !tr.Mapping.NonZeroStart() {
			t.Fatalf("track %d lost its non-zero start", tr.ID)
		}
		if tr.Mapping.TimelineStart != 45000 {
			t.Fatalf("track %d mapped start %d want 45000", tr.ID, tr.Mapping.TimelineStart)
		}
		if tr.Samples[0].TimelineTime != 45000 {
			t.Fatalf("track %d first sample at %d", tr.ID, tr.Samples[0].TimelineTime)
		}
		if tr.Samples[0].SourceTime != tr.Mapping.SourceStart {
			t.Fatalf("track %d source start changed", tr.ID)
		}
	}
}

func TestMergeSortsAcrossTracks(t *testing.T) {
	stream, err := fixture.Build(fixture.DefaultSpec(41, 2, 12))
	if err != nil {
		t.Fatal(err)
	}
	info, frags := scanFragments(t, stream.Data)
	tracks, _ := timeline.BuildTracks([]*ingest.InitInfo{info}, frags, 90000)
	merged, errs := timeline.Merge(tracks, timeline.GapRecord)
	if len(errs) != 0 {
		t.Fatalf("merge errors: %v", errs)
	}
	if len(merged.Samples) != 24 {
		t.Fatalf("merged samples=%d", len(merged.Samples))
	}
	for i := 1; i < len(merged.Samples); i++ {
		if merged.Samples[i].TimelineTime < merged.Samples[i-1].TimelineTime {
			t.Fatalf("sample %d at %d precedes %d", i, merged.Samples[i].TimelineTime, merged.Samples[i-1].TimelineTime)
		}
	}
	if merged.Timescale != 90000 {
		t.Fatalf("timescale=%d", merged.Timescale)
	}
	// Both tracks must be represented at the same instant zero.
	if merged.Samples[0].TimelineTime != 0 || merged.Samples[1].TimelineTime != 0 {
		t.Fatalf("expected two track samples at time zero")
	}
	if merged.Samples[0].TrackID != 1 || merged.Samples[1].TrackID != 2 {
		t.Fatalf("ties not broken by track id")
	}
}

func TestMergeRecordsGapFromDroppedFragment(t *testing.T) {
	injected, err := fixture.Inject(41, 2, 24, []fixture.FaultKind{fixture.FaultSequenceGap})
	if err != nil {
		t.Fatal(err)
	}
	info, frags := scanFragments(t, injected.Data)
	tracks, _ := timeline.BuildTracks([]*ingest.InitInfo{info}, frags, 90000)
	merged, _ := timeline.Merge(tracks, timeline.GapRecord)
	found := false
	for _, g := range merged.Gaps {
		if g.Kind == "internal" && g.Duration > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("dropped fragment did not produce an internal gap: %+v", merged.Gaps)
	}
}
