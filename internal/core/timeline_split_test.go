package core_test

import (
	"testing"

	"mediad/internal/core"
	"mediad/internal/model"
)

func mkTrack(id string, kind model.TrackKind, pts ...model.PlayTime) *model.MediaTrack {
	tr := &model.MediaTrack{ID: id, Kind: kind, TimeScale: 1000}
	for _, p := range pts {
		tr.Frames = append(tr.Frames, model.Frame{Kind: kind, PTS: p, Dur: 1_000_000_000})
	}
	return tr
}

func TestTimelineAnchorsAndMonotonicity(t *testing.T) {
	segs := []core.SegmentInput{
		{Track: mkTrack("v", model.TrackVideo, 0, 1_000_000_000), Anchor: 0},
		{Track: mkTrack("v", model.TrackVideo, 0, 1_000_000_000),
			Anchor: 4_000_000_000, Discontinuity: true},
	}
	rep, err := core.BuildTimeline("v", model.TrackVideo, false, segs)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Frames) != 4 {
		t.Fatalf("frames = %d", len(rep.Frames))
	}
	want := []model.PlayTime{0, 1_000_000_000, 4_000_000_000, 5_000_000_000}
	for i, f := range rep.Frames {
		if f.PTS != want[i] {
			t.Fatalf("frame %d pts = %d want %d", i, f.PTS, want[i])
		}
	}
	var sawDiscont bool
	for _, e := range rep.Events {
		if e.Kind == core.EventDiscont {
			sawDiscont = true
		}
	}
	if !sawDiscont {
		t.Fatal("discontinuity event missing")
	}
}

func TestTimelineRejectsFallback(t *testing.T) {
	// Second segment anchor overlaps the first: absolute PTS goes backwards.
	segs := []core.SegmentInput{
		{Track: mkTrack("v", model.TrackVideo, 0, 2_000_000_000), Anchor: 0},
		{Track: mkTrack("v", model.TrackVideo, 0), Anchor: 1_000_000_000},
	}
	_, err := core.BuildTimeline("v", model.TrackVideo, false, segs)
	se, ok := model.AsSource(err)
	if !ok || se.Kind != model.KindTimestampFallback {
		t.Fatalf("got %v", err)
	}
}

func TestTimelineDropsDuplicate(t *testing.T) {
	tr := mkTrack("v", model.TrackVideo, 0, 0, 1_000_000_000)
	rep, err := core.BuildTimeline("v", model.TrackVideo, false,
		[]core.SegmentInput{{Track: tr, Anchor: 0}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Frames) != 2 {
		t.Fatalf("duplicate frame must be dropped, frames = %d", len(rep.Frames))
	}
	var dup int
	for _, e := range rep.Events {
		if e.Kind == core.EventDuplicate {
			dup++
		}
	}
	if dup != 1 {
		t.Fatalf("duplicate events = %d", dup)
	}
}

func TestSplitKeyframeAlignment(t *testing.T) {
	// 6 frames at 1 s; keyframes exactly on the 2 s target boundaries.
	frames := make([]model.Frame, 6)
	for i := range frames {
		frames[i] = model.Frame{
			Kind: model.TrackVideo, PTS: model.PlayTime(i) * 1_000_000_000,
			Dur: 1_000_000_000, KeyFrame: i%2 == 0,
		}
	}
	res, err := core.Split("v", frames, core.SplitOptions{TargetDuration: 2_000_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Chunks) != 3 {
		t.Fatalf("chunks = %d", len(res.Chunks))
	}
	if res.HitRate() != 1.0 || res.KeyframeHits != res.BoundaryTotal {
		t.Fatalf("hit rate = %f (%d/%d)", res.HitRate(), res.KeyframeHits, res.BoundaryTotal)
	}
	for i, c := range res.Chunks {
		if c.Start != model.PlayTime(i*2)*1_000_000_000 {
			t.Fatalf("chunk %d start = %d", i, c.Start)
		}
		// Internal chunks (a following boundary exists) must be exact hits;
		// the final tail chunk has no following boundary by construction.
		if i > 0 && i < len(res.Chunks)-1 && !c.Boundary.Hit {
			t.Fatalf("internal chunk %d boundary not an exact hit: %+v", i, c.Boundary)
		}
	}
}

func TestSplitSnapsBackToKeyframe(t *testing.T) {
	// Keyframes at 0, 3, 5; the 2 s ideal edge has no keyframe before it
	// inside the first chunk, so the boundary must report an honest miss.
	// Once a keyframe (frame 3) falls inside a target span, the next
	// boundary snaps back to it.
	frames := make([]model.Frame, 7)
	for i := range frames {
		frames[i] = model.Frame{
			Kind: model.TrackVideo, PTS: model.PlayTime(i) * 1_000_000_000,
			Dur: 1_000_000_000, KeyFrame: i == 0 || i == 3 || i == 5,
		}
	}
	res, err := core.Split("v", frames, core.SplitOptions{TargetDuration: 2_000_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if res.BoundaryTotal == 0 {
		t.Fatal("expected internal boundaries")
	}
	if res.KeyframeHits == res.BoundaryTotal {
		t.Fatal("missing-keyframe span must not report all boundaries as hits")
	}
	sawSnap := false
	for _, c := range res.Chunks[1:] {
		// A boundary that claims an exact hit must genuinely start on a
		// keyframe positioned at the ideal time.
		if c.Boundary.Hit && !c.Frames[0].KeyFrame {
			t.Fatalf("chunk %s claims hit but starts on non-keyframe", c.ID)
		}
		if c.Boundary.Hit && c.Boundary.KeyFramePTS != c.Boundary.IdealAt {
			t.Fatalf("hit boundary must sit on the ideal time: %+v", c.Boundary)
		}
		// A snapped boundary sits on a real keyframe before the ideal edge.
		if c.Boundary.KeyFramePTS >= 0 && c.Frames[0].KeyFrame && !c.Boundary.Hit {
			sawSnap = true
		}
	}
	if !sawSnap {
		t.Fatal("expected at least one boundary snapped onto a real keyframe")
	}
}

func TestSplitRejectsBadOptions(t *testing.T) {
	if _, err := core.Split("v", nil, core.SplitOptions{TargetDuration: 0}); err == nil {
		t.Fatal("zero target duration must fail")
	}
	if _, err := core.Split("v", []model.Frame{},
		core.SplitOptions{TargetDuration: 1}); err == nil {
		t.Fatal("empty stream must fail")
	}
}
