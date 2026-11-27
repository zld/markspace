package merge

import (
	"errors"
	"testing"

	"mediapipe/internal/model"
	"mediapipe/internal/timeline"
)

const ms = model.Nanos(1_000_000)

func track(id, kind string, segs ...timeline.MappedSegment) timeline.MappedTrack {
	return timeline.MappedTrack{Asset: "a", Version: "v", TrackID: id, Kind: kind, Timescale: 1000, Segments: segs}
}

func seg(startMS, endMS int64, summary string) timeline.MappedSegment {
	return timeline.MappedSegment{Timeline: model.Span{Start: model.Nanos(startMS) * ms, End: model.Nanos(endMS) * ms}, Summary: summary}
}

func prio(n int) *int { return &n }

func TestAgreementProducesNoConflict(t *testing.T) {
	v1 := Version{ID: "v1", Tracks: []timeline.MappedTrack{
		track("1", "video", seg(0, 1000, "A"), seg(1000, 2000, "B")),
	}}
	v2 := Version{ID: "v2", Tracks: []timeline.MappedTrack{
		track("1", "video", seg(0, 1000, "A"), seg(1000, 2000, "B")),
	}}
	res, err := Merge("a", []Version{v1, v2})
	if err != nil {
		t.Fatal(err)
	}
	if res.ConflictCount() != 0 || res.MissingCount() != 0 {
		t.Fatalf("conflicts=%d missing=%d", res.ConflictCount(), res.MissingCount())
	}
	if err := res.Err(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tr := res.Tracks[0]
	if len(tr.Intervals) != 2 || tr.Intervals[0].Reason != "agreement" {
		t.Fatalf("intervals = %+v", tr.Intervals)
	}
	if !res.HasCommon || res.CommonSpan.Start != 0 || res.CommonSpan.End != 2000*ms {
		t.Fatalf("common = %v has=%v", res.CommonSpan, res.HasCommon)
	}
}

func TestSummaryConflictUnresolvedWithoutPriority(t *testing.T) {
	v1 := Version{ID: "v1", Tracks: []timeline.MappedTrack{track("1", "video", seg(0, 1000, "A"))}}
	v2 := Version{ID: "v2", Tracks: []timeline.MappedTrack{track("1", "video", seg(0, 1000, "B"))}}
	res, err := Merge("a", []Version{v1, v2})
	if err != nil {
		t.Fatal(err)
	}
	if res.ConflictCount() != 1 || res.UnresolvedCount() != 1 {
		t.Fatalf("conflicts=%d unresolved=%d", res.ConflictCount(), res.UnresolvedCount())
	}
	c := res.Conflicts[0]
	if c.Kind != "summary_mismatch" || c.Resolved || c.Chosen != "" {
		t.Fatalf("conflict = %+v", c)
	}
	if len(c.Details) != 2 || c.Details[0].Version != "v1" || c.Details[1].Summary != "B" {
		t.Fatalf("details = %+v", c.Details)
	}
	err = res.Err()
	var me *model.MultiError
	if !errors.As(err, &me) || me.Kind != model.KindConflict {
		t.Fatalf("err = %v", err)
	}
	if model.ExitCodeOf(err) != 2 {
		t.Fatalf("exit = %d", model.ExitCodeOf(err))
	}
}

func TestPriorityResolvesAndRecordsChoice(t *testing.T) {
	v1 := Version{ID: "v1", Priority: prio(10), Tracks: []timeline.MappedTrack{track("1", "video", seg(0, 1000, "A"))}}
	v2 := Version{ID: "v2", Priority: prio(1), Tracks: []timeline.MappedTrack{track("1", "video", seg(0, 1000, "B"))}}
	res, err := Merge("a", []Version{v1, v2})
	if err != nil {
		t.Fatal(err)
	}
	if res.UnresolvedCount() != 0 {
		t.Fatalf("unresolved = %d", res.UnresolvedCount())
	}
	c := res.Conflicts[0]
	if !c.Resolved || c.Chosen != "v1" || c.Reason != "explicit priority" {
		t.Fatalf("conflict = %+v", c)
	}
	iv := res.Tracks[0].Intervals[0]
	if iv.Selected != "v1" || iv.Summary != "A" || iv.Reason != "priority" {
		t.Fatalf("interval = %+v", iv)
	}
	if err := res.Err(); err != nil {
		t.Fatalf("resolved merge should not error: %v", err)
	}
}

func TestPartialPriorityDoesNotResolve(t *testing.T) {
	v1 := Version{ID: "v1", Priority: prio(10), Tracks: []timeline.MappedTrack{track("1", "video", seg(0, 1000, "A"))}}
	v2 := Version{ID: "v2", Tracks: []timeline.MappedTrack{track("1", "video", seg(0, 1000, "B"))}}
	res, _ := Merge("a", []Version{v1, v2})
	if res.UnresolvedCount() != 1 {
		t.Fatalf("partially ranked merge must stay open, unresolved=%d", res.UnresolvedCount())
	}
}

func TestEqualPriorityTiesStayOpen(t *testing.T) {
	v1 := Version{ID: "v1", Priority: prio(5), Tracks: []timeline.MappedTrack{track("1", "video", seg(0, 1000, "A"))}}
	v2 := Version{ID: "v2", Priority: prio(5), Tracks: []timeline.MappedTrack{track("1", "video", seg(0, 1000, "B"))}}
	res, _ := Merge("a", []Version{v1, v2})
	if res.UnresolvedCount() != 1 {
		t.Fatalf("tied priority must stay open, unresolved=%d", res.UnresolvedCount())
	}
}

func TestMissingIntervalItemisedPerVersion(t *testing.T) {
	v1 := Version{ID: "v1", Tracks: []timeline.MappedTrack{
		track("1", "video", seg(0, 1000, "A"), seg(1000, 2000, "B")),
	}}
	v2 := Version{ID: "v2", Tracks: []timeline.MappedTrack{
		track("1", "video", seg(0, 1000, "A")),
	}}
	res, _ := Merge("a", []Version{v1, v2})
	if res.MissingCount() != 1 {
		t.Fatalf("missing = %+v", res.Missing)
	}
	m := res.Missing[0]
	if m.Version != "v2" || m.Reason != "interval_absent" ||
		m.Span.Start != 1000*ms || m.Span.End != 2000*ms {
		t.Fatalf("missing = %+v", m)
	}
	if res.ConflictCount() != 0 {
		t.Fatalf("missing coverage is not a conflict: %+v", res.Conflicts)
	}
}

func TestMissingTrackItemisedOnce(t *testing.T) {
	v1 := Version{ID: "v1", Tracks: []timeline.MappedTrack{
		track("1", "video", seg(0, 1000, "A"), seg(1000, 2000, "B")),
	}}
	v2 := Version{ID: "v2", Tracks: []timeline.MappedTrack{
		track("2", "audio", seg(0, 2000, "aud")),
	}}
	res, _ := Merge("a", []Version{v1, v2})
	var trackMissing int
	for _, m := range res.Missing {
		if m.Reason == "track_absent" && m.TrackID == "1" && m.Version == "v2" {
			trackMissing++
			if m.Span.Start != 0 || m.Span.End != 2000*ms {
				t.Fatalf("track-absent span = %v", m.Span)
			}
		}
	}
	if trackMissing != 1 {
		t.Fatalf("expected one track_absent item, got %d (%+v)", trackMissing, res.Missing)
	}
}

func TestDifferentSliceBoundariesAreNotFalseConflicts(t *testing.T) {
	// v1 publishes two 1 s ranges, v2 one 2 s range with the same content.
	v1 := Version{ID: "v1", Tracks: []timeline.MappedTrack{
		track("1", "video", seg(0, 1000, "A"), seg(1000, 2000, "A")),
	}}
	v2 := Version{ID: "v2", Tracks: []timeline.MappedTrack{
		track("1", "video", seg(0, 2000, "A")),
	}}
	res, _ := Merge("a", []Version{v1, v2})
	if res.ConflictCount() != 0 {
		t.Fatalf("boundary difference alone is not a conflict: %+v", res.Conflicts)
	}
	if len(res.Divergence) == 0 {
		t.Fatal("boundary divergence should still be recorded")
	}
	tr := res.Tracks[0]
	if len(tr.Intervals) != 1 || tr.Intervals[0].Summary != "A" || !tr.Intervals[0].MixedBoundaries {
		t.Fatalf("intervals = %+v", tr.Intervals)
	}
}

func TestBoundarySplitWithSameSummaryCoalesces(t *testing.T) {
	v1 := Version{ID: "v1", Tracks: []timeline.MappedTrack{
		track("1", "video", seg(0, 1000, "A"), seg(1000, 2000, "B")),
	}}
	v2 := Version{ID: "v2", Tracks: []timeline.MappedTrack{
		track("1", "video", seg(0, 500, "A"), seg(500, 1000, "A"),
			seg(1000, 1500, "B"), seg(1500, 2000, "B")),
	}}
	res, _ := Merge("a", []Version{v1, v2})
	if res.ConflictCount() != 0 {
		t.Fatalf("summaries agree, conflicts = %+v", res.Conflicts)
	}
	tr := res.Tracks[0]
	if len(tr.Intervals) != 2 {
		t.Fatalf("expected coalesced intervals, got %+v", tr.Intervals)
	}
	if tr.Intervals[0].Span.End != 1000*ms {
		t.Fatalf("first interval = %v", tr.Intervals[0].Span)
	}
}

func TestTrackKindMismatchIsConflict(t *testing.T) {
	v1 := Version{ID: "v1", Tracks: []timeline.MappedTrack{track("1", "video", seg(0, 1000, "A"))}}
	v2 := Version{ID: "v2", Tracks: []timeline.MappedTrack{track("1", "audio", seg(0, 1000, "A"))}}
	_, err := Merge("a", []Version{v1, v2})
	if err == nil {
		t.Fatal("expected kind mismatch error")
	}
	if model.ExitCodeOf(err) != 2 {
		t.Fatalf("exit = %d", model.ExitCodeOf(err))
	}
}

func TestCommonSpanIntersectsTracks(t *testing.T) {
	v := Version{ID: "v1", Tracks: []timeline.MappedTrack{
		track("1", "video", seg(0, 3000, "V")),
		track("2", "audio", seg(1000, 2500, "A")),
	}}
	res, _ := Merge("a", []Version{v})
	if !res.HasCommon {
		t.Fatal("expected common span")
	}
	if res.CommonSpan.Start != 1000*ms || res.CommonSpan.End != 2500*ms {
		t.Fatalf("common = %v", res.CommonSpan)
	}
}

func TestNoOverlapMeansNoCommon(t *testing.T) {
	v := Version{ID: "v1", Tracks: []timeline.MappedTrack{
		track("1", "video", seg(0, 1000, "V")),
		track("2", "audio", seg(2000, 3000, "A")),
	}}
	res, _ := Merge("a", []Version{v})
	if res.HasCommon {
		t.Fatalf("common should be empty, got %v", res.CommonSpan)
	}
}

func TestDeterministicOutput(t *testing.T) {
	v1 := Version{ID: "v1", Tracks: []timeline.MappedTrack{track("1", "video", seg(0, 1000, "A"), seg(1000, 2000, "B"))}}
	v2 := Version{ID: "v2", Tracks: []timeline.MappedTrack{track("1", "video", seg(0, 1000, "A"))}}
	first, _ := Merge("a", []Version{v1, v2})
	second, _ := Merge("a", []Version{v2, v1})
	if first.MissingCount() != second.MissingCount() || first.ConflictCount() != second.ConflictCount() {
		t.Fatal("merge result depends on version order")
	}
	if first.Missing[0] != second.Missing[0] {
		t.Fatalf("missing differs: %+v vs %+v", first.Missing[0], second.Missing[0])
	}
}

func TestDuplicateVersionRejected(t *testing.T) {
	v := Version{ID: "v1", Tracks: []timeline.MappedTrack{track("1", "video", seg(0, 1000, "A"))}}
	if _, err := Merge("a", []Version{v, v}); err == nil {
		t.Fatal("expected duplicate version error")
	}
}
