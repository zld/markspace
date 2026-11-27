package clock

import (
	"testing"

	"mediarc/internal/model"
)

func recoveredWithBoundaries(id string, bounds []model.Boundary, trusted bool) model.RecoveredRepresentation {
	return model.RecoveredRepresentation{
		ID:     id,
		Format: "dash",
		OK:     trusted,
		Tracks: []model.RecoveredTrack{{
			Track:      model.Track{ID: 1, Timescale: 1000},
			Boundaries: bounds,
			Trusted:    trusted,
		}},
	}
}

func TestFanInBoundaryMatch(t *testing.T) {
	bounds := []model.Boundary{
		{SegmentIndex: 0, StartMs: 0, EndMs: 950},
		{SegmentIndex: 1, StartMs: 950, EndMs: 1900},
	}
	// Within a 20 ms threshold.
	b2 := []model.Boundary{
		{SegmentIndex: 0, StartMs: 5, EndMs: 958},
		{SegmentIndex: 1, StartMs: 960, EndMs: 1912},
	}
	res := FanIn(
		[]model.RecoveredRepresentation{
			recoveredWithBoundaries("video-low", bounds, true),
			recoveredWithBoundaries("video-high", b2, true),
		},
		FanInConfig{BoundaryThresholdMs: 20},
	)
	if len(res.Anomalies) != 0 {
		t.Fatalf("unexpected anomalies: %+v", res.Anomalies)
	}
	if res.ReferenceID != "video-low" || len(res.CommonBoundaries) != 2 {
		t.Fatalf("common timeline wrong: %+v", res)
	}
}

func TestFanInBoundaryMismatch(t *testing.T) {
	b1 := []model.Boundary{{SegmentIndex: 0, StartMs: 0, EndMs: 1000}, {SegmentIndex: 1, StartMs: 1000, EndMs: 2000}}
	b2 := []model.Boundary{{SegmentIndex: 0, StartMs: 0, EndMs: 1000}, {SegmentIndex: 1, StartMs: 1040, EndMs: 2045}}
	res := FanIn(
		[]model.RecoveredRepresentation{
			recoveredWithBoundaries("a", b1, true),
			recoveredWithBoundaries("b", b2, true),
		},
		FanInConfig{BoundaryThresholdMs: 20},
	)
	if len(res.Anomalies) != 1 {
		t.Fatalf("want 1 anomaly, got %d", len(res.Anomalies))
	}
	a := res.Anomalies[0]
	if a.MaxDeltaMs != 40 || a.Kind != model.KindBoundaryMismatch {
		t.Fatalf("anomaly content wrong: %+v", a)
	}
	if len(a.Participants) != 2 || a.Participants[0] != "a" || a.Participants[1] != "b" {
		t.Fatalf("participants wrong: %v", a.Participants)
	}
}

func TestFanInUntrustedExcluded(t *testing.T) {
	b1 := []model.Boundary{{SegmentIndex: 0, StartMs: 0, EndMs: 1000}}
	b2 := []model.Boundary{{SegmentIndex: 0, StartMs: 500, EndMs: 1500}}
	res := FanIn(
		[]model.RecoveredRepresentation{
			recoveredWithBoundaries("good", b1, true),
			recoveredWithBoundaries("bad", b2, false),
		},
		FanInConfig{BoundaryThresholdMs: 20},
	)
	if len(res.Anomalies) != 0 {
		t.Fatalf("untrusted rep must not participate in cross-check: %+v", res.Anomalies)
	}
	if len(res.Untrusted) != 1 || res.Untrusted[0] != "bad" {
		t.Fatalf("untrusted list wrong: %v", res.Untrusted)
	}
	if res.ReferenceID != "good" {
		t.Fatalf("reference must be the only trusted rep, got %q", res.ReferenceID)
	}
}
