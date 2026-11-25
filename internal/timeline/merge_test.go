package timeline

import (
	"testing"

	"medpipe/internal/clock"
	"medpipe/internal/ingest"
)

func tlFrame(track uint16, pts int64, tb clock.Timebase, offset int64) *ingest.Frame {
	return &ingest.Frame{TrackID: track, PTS: pts, Timebase: tb, Offset: offset}
}

func TestMergeConvertsSortsAndDeduplicatesByOffset(t *testing.T) {
	frames := []*ingest.Frame{
		tlFrame(1, 180_000, clock.RTP90K, 300), // 2s
		tlFrame(2, 48_000, clock.PTS48K, 90),   // 1s
		tlFrame(1, 90_000, clock.RTP90K, 100),  // 1s offset 100
		tlFrame(1, 90_000, clock.RTP90K, 40),   // duplicate, lower offset
		tlFrame(1, 0, clock.RTP90K, 0),
	}
	res, err := Merge(frames)
	if err != nil {
		t.Fatal(err)
	}
	if res.Duplicates != 1 {
		t.Fatalf("duplicates=%d want 1", res.Duplicates)
	}
	if len(res.Frames) != 4 {
		t.Fatalf("frames=%d want 4", len(res.Frames))
	}
	wantNS := []int64{0, 1_000_000_000, 1_000_000_000, 2_000_000_000}
	for i, f := range res.Frames {
		if f.TimestampNS != wantNS[i] {
			t.Fatalf("frame %d ns=%d want %d", i, f.TimestampNS, wantNS[i])
		}
	}
	// The two 1s frames retain stable input order; the track-1 winner has min offset.
	if res.Frames[1].TrackID != 2 || res.Frames[2].TrackID != 1 || res.Frames[2].Offset != 40 {
		t.Fatalf("stable order or duplicate winner wrong: %+v", res.Frames[1:3])
	}
}

func TestMergeEmptyAndNil(t *testing.T) {
	res, err := Merge(nil)
	if err != nil || len(res.Frames) != 0 || res.Duplicates != 0 {
		t.Fatalf("nil merge result=%+v err=%v", res, err)
	}
	res, err = Merge([]*ingest.Frame{nil})
	if err != nil || len(res.Frames) != 0 || res.Duplicates != 0 {
		t.Fatalf("nil-element merge result=%+v err=%v", res, err)
	}
}

func TestMergeTracks(t *testing.T) {
	res, err := Merge([]*ingest.Frame{
		tlFrame(3, 0, clock.RTP90K, 0),
		tlFrame(1, 0, clock.RTP90K, 1),
		tlFrame(3, 1, clock.RTP90K, 2),
	})
	if err != nil {
		t.Fatal(err)
	}
	tracks := res.Tracks()
	if len(tracks) != 2 || tracks[0] != 1 || tracks[1] != 3 {
		t.Fatalf("tracks=%v", tracks)
	}
}
