package syncer

import (
	"testing"

	"medcheck/internal/clock"
	"medcheck/internal/recovery"
)

func frame(stream clock.StreamID, pts int64) recovery.MediaEvent {
	return recovery.MediaEvent{Stream: stream, PTSNS: pts, DurationNS: 20_000_000}
}

func TestNoCorrectionAtOrBelow80ms(t *testing.T) {
	s := New()
	s.Push(frame(clock.StreamAudio, 1_000_000_000))
	s.Push(frame(clock.StreamVideo, 1_040_000_000))
	if s.Stats.Corrections != 0 {
		t.Fatalf("unexpected correction: %+v", s.Stats)
	}
}

func TestDriftOver80msRecordsCorrection(t *testing.T) {
	s := New()
	s.Push(frame(clock.StreamAudio, 2_000_000_000))
	s.Push(frame(clock.StreamVideo, 2_120_000_000))
	if s.Stats.Corrections != 1 {
		t.Fatalf("corrections=%d stats=%+v", s.Stats.Corrections, s.Stats)
	}
	if len(s.Drifts) != 1 || abs64(s.Drifts[0].DriftNS) != 120_000_000 {
		t.Fatalf("drifts=%+v", s.Drifts)
	}
	if s.Stats.AvDriftMS != 120 {
		t.Fatalf("av drift ms=%v", s.Stats.AvDriftMS)
	}
}

func TestCorrectedTimestampNeverGoesBackward(t *testing.T) {
	s := New()
	// Video establishes adjusted time 220ms before the late audio reference
	// reveals that video is 120ms early.
	v1 := s.Push(frame(clock.StreamVideo, 220_000_000))
	if v1.AdjustedPTSNS != 220_000_000 {
		t.Fatalf("v1=%d", v1.AdjustedPTSNS)
	}
	s.Push(frame(clock.StreamAudio, 100_000_000))
	if s.Stats.Corrections != 1 {
		t.Fatalf("expected correction, stats=%+v", s.Stats)
	}
	v2 := s.Push(frame(clock.StreamVideo, 240_000_000))
	if v2.AdjustedPTSNS < v1.AdjustedPTSNS {
		t.Fatalf("video went backwards: %d < %d", v2.AdjustedPTSNS, v1.AdjustedPTSNS)
	}
	if v2.AdjustedPTSNS != v1.AdjustedPTSNS {
		t.Fatalf("expected clamp to last output, got %d", v2.AdjustedPTSNS)
	}
	v3 := s.Push(frame(clock.StreamVideo, 500_000_000))
	if v3.AdjustedPTSNS < v2.AdjustedPTSNS {
		t.Fatalf("later video went backwards")
	}
}
