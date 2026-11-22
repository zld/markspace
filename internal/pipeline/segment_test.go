package pipeline

import (
	"testing"

	"github.com/pairwise/mediad/internal/audio"
	"github.com/pairwise/mediad/internal/fault"
	"github.com/pairwise/mediad/internal/fixtures"
)

func TestMaxSegmentCapIsThirtyMinutes(t *testing.T) {
	if MaxSegmentNS != 1_800_000_000_000 {
		t.Fatalf("MaxSegmentNS=%d", MaxSegmentNS)
	}
}

func TestSegmentationAlignsToSyncFramesAndHashesSamples(t *testing.T) {
	sess, err := OpenSession(writeFile(t, t.TempDir(), "seg.mp4", fixtures.FileSpec{
		Timescale: 1000, Brands: []string{"isom"}, BreakStszAt: -1, BreakSizeOf: -1,
		Tracks: []fixtures.TrackSpec{{
			ID: 1, Codec: fixtures.CodecAVC, Timescale: 90000, SampleDur: 3000,
			Samples: 120, KeyEvery: 12, SampleSize: 40, SampleSeed: 3,
		}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	planner, err := sess.Planner()
	if err != nil {
		t.Fatal(err)
	}
	// First window starts mid-GOP: must pull back to sample 0.
	res, err := planner.Run([]Request{{TrackID: 1, StartNS: 0, EndNS: 1_200_000_000, MaxSpanNS: 600_000_000}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Segments) != 2 {
		t.Fatalf("segments=%d want 2", len(res.Segments))
	}
	first, second := res.Segments[0], res.Segments[1]
	if first.FirstSample != 0 || !first.KeyExact {
		t.Fatalf("first segment anchor = %+v", first)
	}
	if second.FirstSample != 12 {
		t.Fatalf("second segment should snap back to sample 12, got %d", second.FirstSample)
	}
	if first.SHA256 == "" || first.SHA256 != first.ContentHex {
		t.Fatalf("hash alias mismatch: %q %q", first.SHA256, first.ContentHex)
	}
	if first.SampleCount != 18 || second.SampleCount != 24 {
		t.Fatalf("sample counts %d+%d want 18+24", first.SampleCount, second.SampleCount)
	}
	if second.EndNS-second.StartNS != 600_000_000 {
		t.Fatalf("second window %d..%d", second.StartNS, second.EndNS)
	}
	// The second window starts at 600ms, 100ms past the sample-12 frame at
	// 500ms, so the cut snaps back and is not an exact hit.
	if res.KeyframeHits != 1 || res.Cuts != 2 {
		t.Fatalf("hits=%d cuts=%d", res.KeyframeHits, res.Cuts)
	}
}

func TestUnreachableRangeReportsNoKeyframe(t *testing.T) {
	sess, err := OpenSession(writeFile(t, t.TempDir(), "seg.mp4", fixtures.FileSpec{
		Timescale: 1000, Brands: []string{"isom"}, BreakStszAt: -1, BreakSizeOf: -1,
		Tracks: []fixtures.TrackSpec{{
			ID: 1, Codec: fixtures.CodecAVC, Timescale: 90000, SampleDur: 3000,
			Samples: 10, KeyEvery: 10, SampleSize: 16, SampleSeed: 1,
		}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	planner, _ := sess.Planner()
	// A window that starts past the last indexed sync frame cannot be
	// forward-aligned.
	idx := sess.Index.Tracks[0]
	end := idx.Entries[len(idx.Entries)-1].Presented
	// The single sync frame is sample 0, so any time after it still aligns
	// back to sample 0; a request for an unknown track is the located
	// failure path that must not be silently dropped.
	_, err = planner.Run([]Request{{TrackID: 99, StartNS: end, EndNS: end + 2_000_000}})
	if err == nil {
		t.Fatal("expected unknown-track error")
	}
	if _, ok := fault.As(err); !ok {
		t.Fatalf("error lacks location: %v", err)
	}
}

func TestSilenceCoverageSkipsUndetectableTrack(t *testing.T) {
	planner := &Planner{Silence: map[uint32]audio.Result{5: audio.Undetectable(5, "no audio")}}
	seg := Segment{TrackID: 5, StartNS: 0, EndNS: 1_000_000_000}
	planner.annotateSilence(&seg)
	if seg.SilenceKnown || seg.SilentNS != 0 {
		t.Fatalf("undetectable track annotated: %+v", seg)
	}
	planner.Silence[5] = audio.Result{TrackID: 5, Detectable: true, Silence: []audio.Range{{Start: 0, End: 400_000_000}}}
	planner.annotateSilence(&seg)
	if !seg.SilenceKnown || seg.SilentNS != 400_000_000 {
		t.Fatalf("silence not annotated: %+v", seg)
	}
}
