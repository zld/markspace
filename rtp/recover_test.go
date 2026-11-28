package rtp

import (
	"fmt"
	"testing"
)

func TestRecoveryFillsGaps(t *testing.T) {
	rec, err := NewRecovery(RecoveryConfig{Silent: true, SampleRate: 48000, Channels: 2})
	if err != nil {
		t.Fatal(err)
	}
	rel := Release{
		Frames: []Frame{
			{ExtendedSeq: 0, StartNS: 0, EndNS: 20_000_000, DurationNS: 20_000_000, Payload: []byte{1}},
			{ExtendedSeq: 3, StartNS: 60_000_000, EndNS: 80_000_000, DurationNS: 20_000_000, Payload: []byte{4}},
		},
		Gaps: []Gap{{StartSeq: 1, Count: 2, DurationNS: 40_000_000}},
	}
	segments := rec.Apply(rel)
	if len(segments) != 3 {
		t.Fatalf("segments = %+v", segments)
	}
	if segments[1].Kind != KindSilence {
		t.Fatalf("middle segment = %+v", segments[1])
	}
	if segments[1].StartNS != 20_000_000 || segments[1].EndNS != 60_000_000 {
		t.Fatalf("silence covers %d..%d", segments[1].StartNS, segments[1].EndNS)
	}
	if segments[2].Kind != KindFrame || segments[2].StartNS != 60_000_000 {
		t.Fatalf("third segment = %+v", segments[2])
	}
	stats := rec.Stats()
	if stats.PacketsLost != 2 || stats.Recovered != 1 || stats.RecoveredNS != 40_000_000 {
		t.Fatalf("stats = %+v", stats)
	}
	samples := rec.SilenceSample(segments[1])
	if len(samples) != 40_000_000*48000/1_000_000_000*2*2 {
		t.Fatalf("silence sample bytes = %d", len(samples))
	}
}

func TestRecoveryWithoutSilence(t *testing.T) {
	rec, err := NewRecovery(RecoveryConfig{Silent: false})
	if err != nil {
		t.Fatal(err)
	}
	segments := rec.Apply(Release{Gaps: []Gap{{StartSeq: 1, Count: 1, DurationNS: 20_000_000}}})
	if len(segments) != 0 {
		t.Fatalf("segments = %+v", segments)
	}
	if rec.Stats().PacketsLost != 1 || rec.Stats().Recovered != 0 {
		t.Fatalf("stats = %+v", rec.Stats())
	}
}

func TestRecoveryDeterministic(t *testing.T) {
	rel := Release{
		Frames: []Frame{
			{ExtendedSeq: 0, StartNS: 0, EndNS: 20_000_000, DurationNS: 20_000_000, Payload: []byte{1, 2}},
			{ExtendedSeq: 2, StartNS: 40_000_000, EndNS: 60_000_000, DurationNS: 20_000_000, Payload: []byte{3}},
		},
		Gaps: []Gap{{StartSeq: 1, Count: 1, DurationNS: 20_000_000}},
	}
	first, _ := NewRecovery(RecoveryConfig{Silent: true})
	second, _ := NewRecovery(RecoveryConfig{Silent: true})
	a, b := first.Apply(rel), second.Apply(rel)
	if fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatalf("recovery is not deterministic:\n%v\n%v", a, b)
	}
}
