package sync

import (
	"errors"
	"testing"
)

func TestFitDrift(t *testing.T) {
	// Perfect 100ppm fast clock: shared = 1.0001*media, intercept 0.
	obs := make([]Observation, 0, 5)
	for i := int64(1); i <= 5; i++ {
		media := i * 1_000_000_000
		shared := int64(1.0001 * float64(media))
		obs = append(obs, Observation{MediaNS: media, SharedNS: shared, Weight: 1})
	}
	m, err := FitDrift(obs)
	if err != nil {
		t.Fatal(err)
	}
	if absFloat(m.DriftPPM-100) > 1 || m.ResidualNS > 1 {
		t.Fatalf("drift fit = %+v", m)
	}
	if got := m.Apply(10_000_000_000); abs64(got-10_001_000_000) > 1 {
		t.Errorf("apply = %d", got)
	}
	if _, err := FitDrift(obs[:1]); !errors.Is(err, ErrInsufficientPoints) {
		t.Fatalf("single point must fail: %v", err)
	}
}

func TestAudioVideoOffsetThreshold(t *testing.T) {
	th := DefaultThresholds()
	// Within-threshold: both tracks map media 0 to shared positions whose
	// (shared-media) offsets differ by only 5ms.
	a := NewAligner(th)
	a.AddAnchor(Anchor{Kind: TrackAudio, TrackID: "aud", MediaNS: 0, SharedNS: 1_000_000_000})
	a.AddAnchor(Anchor{Kind: TrackVideo, TrackID: "vid", MediaNS: 5_000_000, SharedNS: 1_000_000_000})
	if _, err := a.Align(); err != nil {
		t.Fatalf("5ms offset should pass: %v", err)
	}

	// Now make the two offsets differ by 100ms.
	a2 := NewAligner(th)
	a2.AddAnchor(Anchor{Kind: TrackAudio, TrackID: "aud", MediaNS: 0, SharedNS: 1_000_000_000})
	a2.AddAnchor(Anchor{Kind: TrackVideo, TrackID: "vid", MediaNS: 100_000_000, SharedNS: 1_000_000_000})
	_, err := a2.Align()
	var ae *AlignmentError
	if !errors.As(err, &ae) {
		t.Fatalf("expected AlignmentError, got %v", err)
	}
	if abs64(ae.DeltaNS) != 100_000_000 || ae.EarliestBadNS != 1_000_000_000 {
		t.Fatalf("error localization = %+v", ae)
	}
	if !errors.Is(err, ErrAlignment) {
		t.Fatal("error must wrap ErrAlignment sentinel")
	}
}

func TestSubtitleSilentShiftGuard(t *testing.T) {
	// Loosen the A/V threshold so the A/V check passes; the subtitle-specific
	// guard (still at its strict 40ms default) must refuse to paper over it.
	th := DefaultThresholds()
	th.MaxOffsetNS = 200_000_000
	a := NewAligner(th)
	// Audio offset = 1s; video offset = 1.08s => 80ms A/V disagreement,
	// inside the loosened 200ms A/V bound but real nonetheless.
	a.AddAnchor(Anchor{Kind: TrackAudio, TrackID: "aud", MediaNS: 0, SharedNS: 1_000_000_000})
	a.AddAnchor(Anchor{Kind: TrackVideo, TrackID: "vid", MediaNS: 80_000_000, SharedNS: 1_000_080_000})
	a.AddAnchor(Anchor{Kind: TrackSubtitle, TrackID: "sub", MediaNS: 0, SharedNS: 1_000_050_000})
	a.AddCues(Cue{Index: 1, StartNS: 50_000_000, EndNS: 80_000_000, Text: "hi"})
	_, err := a.Align()
	var ae *AlignmentError
	if !errors.As(err, &ae) || ae.TrackA != TrackSubtitle {
		t.Fatalf("expected subtitle guard error, got %v", err)
	}
	if ae.EarliestBadNS <= 0 {
		t.Fatal("guard error must locate the earliest bad time")
	}
}

func TestSubtitleShiftWithinBound(t *testing.T) {
	th := DefaultThresholds()
	a := NewAligner(th)
	a.AddAnchor(Anchor{Kind: TrackAudio, TrackID: "aud", MediaNS: 0, SharedNS: 1_000_000_000})
	a.AddAnchor(Anchor{Kind: TrackVideo, TrackID: "vid", MediaNS: 0, SharedNS: 1_000_005_000}) // 5ms, fine
	a.AddAnchor(Anchor{Kind: TrackSubtitle, TrackID: "sub", MediaNS: 0, SharedNS: 1_000_100_000})
	al, err := a.Align()
	if err != nil {
		t.Fatalf("bounded shift should pass: %v", err)
	}
	cues := al.ShiftCues([]Cue{{Index: 1, StartNS: 0, EndNS: 1_000_000, Text: "x"}})
	if cues[0].StartNS != al.SubShift {
		t.Fatalf("cue shift = %d, want %d", cues[0].StartNS, al.SubShift)
	}

	// Shift beyond 200ms must fail.
	a2 := NewAligner(th)
	a2.AddAnchor(Anchor{Kind: TrackAudio, TrackID: "aud", MediaNS: 0, SharedNS: 1_000_000_000})
	a2.AddAnchor(Anchor{Kind: TrackSubtitle, TrackID: "sub", MediaNS: 0, SharedNS: 1_300_000_000})
	if _, err := a2.Align(); err == nil {
		t.Fatal("300ms subtitle shift must be rejected")
	}
}

func TestDriftThresholdFailure(t *testing.T) {
	th := DefaultThresholds() // 50ppm
	a := NewAligner(th)
	obs := []Observation{
		{MediaNS: 0, SharedNS: 0},
		{MediaNS: 1_000_000_000, SharedNS: 1_000_200_000}, // 200ppm
	}
	for _, o := range obs {
		a.AddObservation("vid", o)
	}
	_, err := a.Align()
	var ae *AlignmentError
	if !errors.As(err, &ae) {
		t.Fatalf("expected drift AlignmentError, got %v", err)
	}
	if ae.EarliestBadNS != 0 {
		t.Fatalf("drift error should point at first observation, got %d", ae.EarliestBadNS)
	}
}
