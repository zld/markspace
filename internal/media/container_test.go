package media

import (
	"testing"

	"mediaverify/internal/errs"
)

func TestSynthesizeAndProbeRoundTrip(t *testing.T) {
	raw, err := Synthesize(90000, 9_000_000)
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	summary, err := Probe(raw)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if summary.Timescale != 90000 {
		t.Fatalf("timescale = %d, want 90000", summary.Timescale)
	}
	if summary.Duration != 9_000_000 {
		t.Fatalf("duration = %d, want 9000000", summary.Duration)
	}
	if summary.Brand != "isom" {
		t.Fatalf("brand = %q, want isom", summary.Brand)
	}
	if len(summary.SHA256) != 64 {
		t.Fatalf("digest wrong length: %q", summary.SHA256)
	}
	// Deterministic serialization: same inputs, same bytes, same digest.
	raw2, _ := Synthesize(90000, 9_000_000)
	s2, _ := Probe(raw2)
	if s2.SHA256 != summary.SHA256 {
		t.Fatal("MP4 synthesis is not deterministic")
	}
}

func TestSynthesizeRejectsZeroTimescale(t *testing.T) {
	if _, err := Synthesize(0, 100); errs.Class(err) != errs.KindInvalidInput {
		t.Fatalf("want invalid_input, got %v", err)
	}
}

func TestProbeRejectsGarbage(t *testing.T) {
	if _, err := Probe([]byte("not an mp4")); errs.Class(err) != errs.KindInvalidInput {
		t.Fatalf("want invalid_input for garbage, got %v", err)
	}
}
