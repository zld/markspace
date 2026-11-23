package recovery

import (
	"testing"

	"archivecheck/internal/index"
	"archivecheck/internal/media"
)

func clock() media.Clock { return media.MustClock(1000) }

// linearIndex builds three verified keyframe anchors at 0, 250 and 500 ms.
func linearInput() Input {
	return Input{
		Layer: "low", Clock: clock(), StreamLen: 3000, DurationMs: 1000,
		Units: []Unit{
			{Offset: 0, End: 300, Keyframe: true},
			{Offset: 300, End: 500},
			{Offset: 700, End: 1000, Keyframe: true},
			{Offset: 1000, End: 1300},
			{Offset: 1500, End: 1800, Keyframe: true},
			{Offset: 1800, End: 3000},
		},
		Index: index.Validation{Aligned: []index.Aligned{
			{Entry: index.Entry{PTS: 0, Offset: 0}, Boundary: 0},
			{Entry: index.Entry{PTS: 250, Offset: 700}, Boundary: 700},
			{Entry: index.Entry{PTS: 500, Offset: 1500}, Boundary: 1500},
		}},
	}
}

func TestRecoverHealthy(t *testing.T) {
	r := Recover(linearInput())
	if len(r.Segments) != 3 || len(r.Gaps) != 0 {
		t.Fatalf("segments=%+v gaps=%+v", r.Segments, r.Gaps)
	}
	if r.ParseRatio != 1 {
		t.Fatalf("parse ratio=%f", r.ParseRatio)
	}
	if r.MeanKFMs != 250 || r.MaxKFMs != 250 {
		t.Fatalf("mean=%f max=%d", r.MeanKFMs, r.MaxKFMs)
	}
	if r.StartMs != 0 || r.EndMs != 1000 {
		t.Fatalf("window=%d-%d", r.StartMs, r.EndMs)
	}
	if r.Segments[2].ByteEnd != 3000 {
		t.Fatalf("last segment=%+v", r.Segments[2])
	}
	if r.RecoveredBytes != 3000 {
		t.Fatalf("recovered bytes=%d", r.RecoveredBytes)
	}
}

func TestRecoverTruncationKeepsGap(t *testing.T) {
	in := linearInput()
	in.Truncation = &Truncation{Offset: 1600, Kind: media.KindOBUTruncated, Detail: "cut"}
	r := Recover(in)
	if len(r.Segments) != 2 {
		t.Fatalf("segments=%+v", r.Segments)
	}
	if len(r.Gaps) != 1 {
		t.Fatalf("gaps=%+v", r.Gaps)
	}
	g := r.Gaps[0]
	if g.Reason != media.KindOBUTruncated || g.Class != media.Unrecoverable {
		t.Fatalf("gap=%+v", g)
	}
	if g.StartMs != 500 || g.EndMs != 1000 {
		t.Fatalf("gap window=%d-%d", g.StartMs, g.EndMs)
	}
	// No content may be invented across the gap.
	for _, s := range r.Segments {
		if s.StartMs >= g.StartMs && s.EndMs <= g.EndMs {
			t.Fatalf("segment inside gap: %+v", s)
		}
	}
}

func TestRecoverUnverifiedAnchorSplitsCoverage(t *testing.T) {
	in := linearInput()
	// The middle index entry claims a boundary that is not a keyframe.
	in.Index.Aligned[1].Boundary = 300
	r := Recover(in)
	if len(r.RejectedFaults()) == 0 {
		t.Fatalf("unverified claim not reported: %+v", r.Faults)
	}
	if len(r.Anchors) != 2 {
		t.Fatalf("anchors=%+v", r.Anchors)
	}
	// The keyframe at byte 700 lost its only index claim, so the whole span up
	// to the next verified anchor at 500 ms is retained as a gap.
	if len(r.Gaps) != 1 || r.Gaps[0].StartMs != 0 || r.Gaps[0].EndMs != 500 {
		t.Fatalf("gaps=%+v", r.Gaps)
	}
	// The intraft keyframe at byte 700 now has no index entry at all, so the
	// gap is attributed to missing index coverage.
	if r.Gaps[0].Reason != media.KindIndexOutOfRange {
		t.Fatalf("reason=%s", r.Gaps[0].Reason)
	}
	if len(r.Segments) != 1 || r.Segments[0].StartMs != 500 {
		t.Fatalf("segments=%+v", r.Segments)
	}
}

func TestRecoverUnindexedKeyframeCreatesGap(t *testing.T) {
	in := linearInput()
	// A keyframe exists in the stream with no index entry tying it to time.
	in.Units = append(in.Units, Unit{Offset: 2200, End: 2400, Keyframe: true})
	in.StreamLen = 3000
	in.Index.Aligned = in.Index.Aligned[:2] // only 0 and 250 ms anchors
	r := Recover(in)
	if len(r.Gaps) != 1 {
		t.Fatalf("gaps=%+v", r.Gaps)
	}
	if r.Gaps[0].Reason != media.KindIndexOutOfRange {
		t.Fatalf("gap=%+v", r.Gaps[0])
	}
	if r.Gaps[0].StartMs != 250 || r.Gaps[0].EndMs != 1000 {
		t.Fatalf("gap window=%d-%d", r.Gaps[0].StartMs, r.Gaps[0].EndMs)
	}
}

func TestRecoverNoAnchors(t *testing.T) {
	in := linearInput()
	in.Index = index.Validation{}
	r := Recover(in)
	if len(r.Segments) != 0 {
		t.Fatalf("segments=%+v", r.Segments)
	}
	if len(r.Gaps) != 1 || r.Gaps[0].Reason != media.KindLayerUnusable {
		t.Fatalf("gaps=%+v", r.Gaps)
	}
	if r.Gaps[0].EndMs != in.DurationMs {
		t.Fatalf("gap must cover the whole duration: %+v", r.Gaps[0])
	}
}

func TestRecoverParseRatioCountsTruncatedUnits(t *testing.T) {
	in := linearInput()
	in.Units = append(in.Units, Unit{Offset: 2900, End: 3000, Truncated: true})
	r := Recover(in)
	if r.Frames != 7 || r.Parseable != 6 {
		t.Fatalf("frames=%d parseable=%d", r.Frames, r.Parseable)
	}
	if r.ParseRatio <= 0.85 || r.ParseRatio >= 0.86 {
		t.Fatalf("ratio=%f", r.ParseRatio)
	}
}

func TestRecoverCadenceLimit(t *testing.T) {
	in := linearInput()
	in.MaxKeyframeIntervalMs = 200
	r := Recover(in)
	found := false
	for _, f := range r.Faults {
		if f.Kind == media.KindIntervalExceeded {
			found = true
			if f.Class != media.Recoverable {
				t.Fatalf("class=%v", f.Class)
			}
			if !f.HasSpan || f.Span.StartMillis() != 0 || f.Span.EndMillis() != 500 {
				t.Fatalf("span=%+v", f.Span)
			}
		}
	}
	if !found {
		t.Fatalf("cadence limit not enforced: %+v", r.Faults)
	}

	// A limit at or above the observed maximum is silent.
	in = linearInput()
	in.MaxKeyframeIntervalMs = 250
	for _, f := range Recover(in).Faults {
		if f.Kind == media.KindIntervalExceeded {
			t.Fatalf("limit at the observed maximum fired: %+v", f)
		}
	}
}
