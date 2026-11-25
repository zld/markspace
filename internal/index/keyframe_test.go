package index

import (
	"testing"

	"medpipe/internal/clock"
	"medpipe/internal/ingest"
)

func idxFrame(track uint16, pts int64, tb clock.Timebase, key bool, offset int64) *ingest.Frame {
	return &ingest.Frame{TrackID: track, PTS: pts, Timebase: tb, KeyFrame: key, Offset: offset}
}

func TestBuildConvertsTimebasesAndQueriesWindow(t *testing.T) {
	frames := []*ingest.Frame{
		idxFrame(1, 0, clock.RTP90K, true, 10),
		idxFrame(2, 48_000, clock.PTS48K, true, 90), // 1s
		idxFrame(1, 90_000, clock.RTP90K, false, 200),
		idxFrame(1, 180_000, clock.RTP90K, true, 300), // 2s
	}
	idx, err := Build(frames)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Keyframes) != 3 {
		t.Fatalf("keyframes=%d want 3: %+v", len(idx.Keyframes), idx.Keyframes)
	}
	if idx.Keyframes[0].TimestampNS != 0 || idx.Keyframes[1].TimestampNS != 1_000_000_000 {
		t.Fatalf("timebase conversion/sort wrong: %+v", idx.Keyframes)
	}
	if got := idx.Query(1, 1_500_000_000, 2_500_000_000); len(got) != 1 || got[0].Offset != 300 {
		t.Fatalf("window query got %+v", got)
	}
	if got := idx.Query(1, 2_000_000_000, 2_000_000_001); len(got) != 1 {
		t.Fatalf("point-width window should include start: %+v", got)
	}
	if got := idx.Query(1, 2_000_000_001, 3_000_000_000); len(got) != 0 {
		t.Fatalf("end-exclusive boundary wrong: %+v", got)
	}
}

func TestIndexWithoutKeyframesAndEmptyTracks(t *testing.T) {
	idx, err := Build([]*ingest.Frame{idxFrame(1, 1, clock.RTP90K, false, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Keyframes) != 0 {
		t.Fatalf("expected no keyframes, got %+v", idx.Keyframes)
	}
	if got := idx.Query(1, 0, 1_000); got != nil {
		t.Fatalf("query on empty index = %+v, want nil", got)
	}
	hit := idx.HitRate(nil)
	if hit.Total != 0 || hit.Rate() != 0 {
		t.Fatalf("empty hit result %+v", hit)
	}

	emptyIdx, err := Build(nil)
	if err != nil || len(emptyIdx.Keyframes) != 0 {
		t.Fatalf("nil frames should build empty index: idx=%+v err=%v", emptyIdx, err)
	}
}

func TestLegalSampleWindowHitRateIsFull(t *testing.T) {
	frames := []*ingest.Frame{
		{TrackID: 7, PTS: 0, Timebase: clock.RTP90K, KeyFrame: true, Offset: 0},
		{TrackID: 7, PTS: 45_000, Timebase: clock.RTP90K, KeyFrame: false, Offset: 72},
		{TrackID: 7, PTS: 90_000, Timebase: clock.RTP90K, KeyFrame: true, Offset: 144},
	}
	idx, err := Build(frames)
	if err != nil {
		t.Fatal(err)
	}
	windows := make([]Window, 0, len(idx.Keyframes))
	for _, k := range idx.Keyframes {
		windows = append(windows, Window{TrackID: k.TrackID, StartNS: k.TimestampNS, EndNS: k.TimestampNS + 1})
	}
	hit := idx.HitRate(windows)
	if hit.Hits != hit.Total || hit.Rate() != 100 {
		t.Fatalf("hit result %+v rate=%v", hit, hit.Rate())
	}
}
