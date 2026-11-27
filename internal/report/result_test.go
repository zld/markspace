package report

import (
	"encoding/json"
	"testing"
	"time"

	"mediarc/internal/model"
)

func healthyRep(id string) model.RecoveredRepresentation {
	return model.RecoveredRepresentation{
		ID: id, Format: "dash", OK: true,
		Tracks: []model.RecoveredTrack{{
			Trusted: true, DriftPPM: 10, WrapCount: 0,
			Track: model.Track{ID: 1, Codec: model.CodecAVC1, Timescale: 90000,
				Samples: []model.Sample{{Dts: 0, Duration: 3000, Payload: []byte{1}}}},
		}},
	}
}

func TestBuildSummaryCountsAndBudget(t *testing.T) {
	in := Input{
		Seed:       17,
		Thresholds: Thresholds{BoundaryMs: 20, DriftMsPerSec: 5, BudgetMs: 1000},
		Elapsed:    420 * time.Millisecond,
		Items: []ItemInput{
			{Rep: healthyRep("r1"), FingerprintMatch: true, Mapped: true},
			{Rep: healthyRep("r2"), FingerprintMatch: true, Mapped: true},
		},
		FanIn: NewFanInView(nil, nil),
	}
	s := Build(in)
	if s.RepresentationCount != 2 || s.RecoveredCount != 2 ||
		s.FingerprintMatches != 2 || s.MappingSuccessCount != 2 {
		t.Fatalf("counts wrong: %+v", s)
	}
	if s.BudgetExceeded || s.BudgetDeviationMs != -580 {
		t.Fatalf("budget math wrong: %+v", s)
	}
	if s.Conclusions[0].Conclusion != "ok" {
		t.Fatalf("want ok, got %s", s.Conclusions[0].Conclusion)
	}
}

func TestBudgetOverrunAndBoundaryAnomaly(t *testing.T) {
	anom := model.AnomalyInterval{
		StartMs: 0, EndMs: 100, ThresholdMs: 1, MaxDeltaMs: 12,
		Participants: []string{"r2", "r1"}, Kind: model.KindBoundaryMismatch, Detail: "diverge",
	}
	in := Input{
		Seed:       17,
		Thresholds: Thresholds{BoundaryMs: 1, DriftMsPerSec: 5, BudgetMs: 1},
		Elapsed:    5 * time.Millisecond,
		Items:      []ItemInput{{Rep: healthyRep("r1"), FingerprintMatch: true, Mapped: true}},
		FanIn:      NewFanInView([]model.AnomalyInterval{anom}, []string{"r3"}),
	}
	s := Build(in)
	if !s.BudgetExceeded || s.BudgetDeviationMs != 4 {
		t.Fatalf("budget overrun must be flagged: %+v", s)
	}
	if s.AnomalyCount != 1 || s.Anomalies[0].Participants[0] != "r1" {
		t.Fatalf("anomaly must list sorted participants: %+v", s.Anomalies)
	}
	if len(s.ReviewItems) != 1 || s.ReviewItems[0] != "r3" {
		t.Fatalf("untrusted rep must enter review: %+v", s.ReviewItems)
	}
}

func TestCorruptRepresentationIsolated(t *testing.T) {
	bad := healthyRep("bad")
	bad.OK = false
	bad.Errors = []*model.MediaError{{Kind: model.KindSegmentCorrupt, File: "x.ts"}}
	bad.Tracks[0].Trusted = false
	in := Input{
		Thresholds: Thresholds{BudgetMs: 1000},
		Elapsed:    time.Millisecond,
		Items: []ItemInput{
			{Rep: healthyRep("good"), FingerprintMatch: true, Mapped: true},
			{Rep: bad, ReviewRequired: true},
		},
	}
	s := Build(in)
	if s.RecoveredCount != 1 {
		t.Fatal("corrupt rep must not reduce the healthy recovery count")
	}
	var badConclusion string
	for _, c := range s.Conclusions {
		if c.RepID == "bad" {
			badConclusion = c.Conclusion
		}
	}
	if badConclusion != "review_required" {
		t.Fatalf("bad rep conclusion=%s want review_required", badConclusion)
	}
}

func TestDeterministicJSON(t *testing.T) {
	anom := model.AnomalyInterval{Participants: []string{"b", "a"}, Kind: model.KindBoundaryMismatch}
	build := func() Summary {
		return Build(Input{
			Thresholds: Thresholds{BudgetMs: 1000},
			Elapsed:    2 * time.Millisecond,
			Items: []ItemInput{
				{Rep: healthyRep("z"), FingerprintMatch: true},
				{Rep: healthyRep("a"), FingerprintMatch: true},
			},
			FanIn: NewFanInView([]model.AnomalyInterval{anom}, nil),
		})
	}
	b1, _ := json.Marshal(build())
	b2, _ := json.Marshal(build())
	if string(b1) != string(b2) {
		t.Fatalf("repeated builds must serialize identically:\n%s\n%s", b1, b2)
	}
}

func TestDriftExceededConclusion(t *testing.T) {
	rep := healthyRep("drift")
	rep.Tracks[0].DriftPPM = 9000
	c := conclude(ItemInput{Rep: rep, FingerprintMatch: true, Mapped: true},
		Thresholds{DriftMsPerSec: 5})
	if !c.DriftExceeded || c.Conclusion != "drift_threshold_exceeded" {
		t.Fatalf("drift over threshold misclassified: %+v", c)
	}
}
