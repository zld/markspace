package media

import (
	"math"
	"testing"
)

func TestSortEventsTrackThenSequenceAtEqualPTS(t *testing.T) {
	in := []Event{
		{TrackID: 2, Sequence: 1, PTSNS: 100, GlobalSequence: 3},
		{TrackID: 1, Sequence: 1, PTSNS: 100, GlobalSequence: 2},
		{TrackID: 1, Sequence: 0, PTSNS: 100, GlobalSequence: 0},
		{TrackID: 2, Sequence: 0, PTSNS: 100, GlobalSequence: 1},
		{TrackID: 1, Sequence: 0, PTSNS: 80, GlobalSequence: 4},
	}
	got := SortEvents(in)
	wantTrack := []uint32{1, 1, 1, 2, 2}
	wantSeq := []int{0, 0, 1, 0, 1}
	for i := range wantTrack {
		if got[i].TrackID != wantTrack[i] || got[i].Sequence != wantSeq[i] {
			t.Fatalf("position %d got track=%d seq=%d; want track=%d seq=%d\nfull=%+v",
				i, got[i].TrackID, got[i].Sequence, wantTrack[i], wantSeq[i], got)
		}
	}
}

func TestAnalyzeCoverageAndVideoAVSkew(t *testing.T) {
	events := []Event{
		{Kind: KindVideo, TrackID: 1, PTSNS: 0, DurationNS: 40_000_000, EndNS: 40_000_000},
		{Kind: KindAudio, TrackID: 2, PTSNS: 0, DurationNS: 20_000_000, EndNS: 20_000_000},
		{Kind: KindAudio, TrackID: 2, PTSNS: 20_000_000, DurationNS: 20_000_000, EndNS: 40_000_000},
		{Kind: KindVideo, TrackID: 1, PTSNS: 40_000_000, DurationNS: 40_000_000, EndNS: 80_000_000},
		{Kind: KindAudio, TrackID: 2, PTSNS: 40_000_000, DurationNS: 20_000_000, EndNS: 60_000_000},
		{Kind: KindAudio, TrackID: 2, PTSNS: 60_000_000, DurationNS: 20_000_000, EndNS: 80_000_000},
	}
	r := AnalyzeTimeline(events)
	if r.CoveragePPM != 1_000_000 {
		t.Fatalf("coverage=%d", r.CoveragePPM)
	}
	if r.MaxAVSkewNS != 0 {
		t.Fatalf("skew=%d ns, want 0 because every video PTS has aligned audio", r.MaxAVSkewNS)
	}

	events[3].PTSNS = 45_000_000
	events[3].EndNS = 85_000_000
	r = AnalyzeTimeline(events)
	if r.MaxAVSkewNS != 5_000_000 {
		t.Fatalf("skew=%d, want 5ms", r.MaxAVSkewNS)
	}
}

func TestAnalyzeExplicitLossGapNotCovered(t *testing.T) {
	events := []Event{
		{Kind: KindVideo, TrackID: 1, PTSNS: 0, DurationNS: 40_000_000, EndNS: 40_000_000},
		{Kind: KindVideo, TrackID: 1, PTSNS: 80_000_000, DurationNS: 40_000_000, EndNS: 120_000_000},
		{Kind: KindCue, Lost: true, MissingCount: 1, PTSNS: 40_000_000, EndNS: 80_000_000, Detail: "missing"},
	}
	r := AnalyzeTimeline(events)
	if r.LostEvents != 1 || len(r.Gaps) == 0 {
		t.Fatalf("loss not reported: %+v", r)
	}
	if r.CoveragePPM >= 1_000_000 {
		t.Fatalf("coverage should reflect gap, got %d", r.CoveragePPM)
	}
}

func TestCheckedAddExtremes(t *testing.T) {
	if _, err := CheckedAdd(math.MaxInt64, 1); err == nil {
		t.Fatal("expected overflow")
	}
}
