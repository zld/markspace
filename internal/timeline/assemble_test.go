package timeline_test

import (
	"testing"

	"mediapack/internal/clock"
	"mediapack/internal/demux"
	"mediapack/internal/fault"
	"mediapack/internal/maptrack"
	"mediapack/internal/timeline"
)

func testTable() *maptrack.Table {
	tb := clock.TickRate(24000)
	samples := make([]*demux.Sample, 0, 12)
	for i := 0; i < 12; i++ {
		samples = append(samples, &demux.Sample{
			Index: i, TrackID: 1, DTS: int64(i * 1000), PTS: int64(i * 1000),
			Duration: 1000, Hash: "h", Payload: []byte{byte(i)},
		})
	}
	return &maptrack.Table{
		CommonTimebase: clock.TickRate(48000),
		Tracks: []*maptrack.MappedTrack{{
			TargetID: 1, Timebase: tb, Track: &demux.Track{ID: 1, Timebase: tb, Samples: samples},
		}},
	}
}

func TestHalfOpenIntervalSelectsBoundaryCorrectly(t *testing.T) {
	spec := timeline.Spec{Clips: []timeline.ClipSpec{{
		ID: "c1", TargetID: 1, Start: clock.New(0, 1),
		In: clock.New(3000, 24000), Out: clock.New(6000, 24000),
	}}}
	tl, err := timeline.Assemble(testTable(), spec)
	if err != nil {
		t.Fatal(err)
	}
	p := tl.Tracks[0]
	if len(p.Samples) != 3 {
		t.Fatalf("samples=%d, want indices 3,4,5", len(p.Samples))
	}
	if p.Samples[0].SourceIndex != 3 || p.Samples[2].SourceIndex != 5 {
		t.Fatalf("indices=%d..%d", p.Samples[0].SourceIndex, p.Samples[2].SourceIndex)
	}
	if p.Samples[0].DTS != 0 || p.Samples[1].DTS != 2000 { // 1000/24000 -> 2000/48000
		t.Fatalf("converted DTS=%d,%d", p.Samples[0].DTS, p.Samples[1].DTS)
	}
}

func TestRejectNegativeDurationAndBackwardDTS(t *testing.T) {
	table := testTable()
	bad := timeline.Spec{Clips: []timeline.ClipSpec{{
		ID: "bad", TargetID: 1, In: clock.New(5, 24000), Out: clock.New(4, 24000),
	}}}
	if _, err := timeline.Assemble(table, bad); err == nil {
		t.Fatal("expected negative duration error")
	}

	back := timeline.Spec{Clips: []timeline.ClipSpec{
		{ID: "a", TargetID: 1, Start: clock.New(0, 1), In: clock.New(0, 24000), Out: clock.New(4000, 24000)},
		{ID: "b", TargetID: 1, Start: clock.New(1, 24000), In: clock.New(4000, 24000), Out: clock.New(8000, 24000)},
	}}
	_, err := timeline.Assemble(table, back)
	if err == nil {
		t.Fatal("expected backward DTS/overlap error")
	}
}

func TestUndeclaredOverlapIsRejectedButDeclaredTransitionAccepted(t *testing.T) {
	table := testTable()
	table.Tracks = append(table.Tracks, &maptrack.MappedTrack{TargetID: 2, Timebase: table.CommonTimebase})
	// Give target two four samples so its clip is non-empty.
	for i := 0; i < 4; i++ {
		table.Tracks[1].Track = &demux.Track{ID: 2, Timebase: table.CommonTimebase}
	}
	for i := 0; i < 4; i++ {
		table.Tracks[1].Track.Samples = append(table.Tracks[1].Track.Samples, &demux.Sample{
			Index: i, DTS: int64(i * 2000), PTS: int64(i * 2000), Duration: 2000, Hash: "x",
		})
	}
	clips := []timeline.ClipSpec{
		{ID: "a", TargetID: 1, Group: "vision", Start: clock.New(0, 1), In: clock.New(0, 24000), Out: clock.New(6000, 24000)},
		{ID: "b", TargetID: 2, Group: "vision", Start: clock.New(1, 8), In: clock.New(0, 24000), Out: clock.New(4000, 24000)},
	}
	if _, err := timeline.Assemble(table, timeline.Spec{Clips: clips}); err == nil {
		t.Fatal("expected undeclared overlap")
	} else if fe, ok := fault.As(err); !ok || fe.Kind != fault.KindTimeline {
		t.Fatalf("error=%v", err)
	}
	spec := timeline.Spec{Clips: clips, Transitions: []timeline.TransitionSpec{{
		ID: "tr", Kind: "dissolve", FromClipID: "a", ToClipID: "b", Overlap: clock.New(1, 8),
	}}}
	tl, err := timeline.Assemble(table, spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Transitions) != 1 || tl.Transitions[0].Overlap.Cmp(clock.New(1, 8)) != 0 {
		t.Fatalf("transitions=%+v", tl.Transitions)
	}
}
