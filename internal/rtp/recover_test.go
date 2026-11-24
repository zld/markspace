package rtp

import (
	"testing"

	"mediacheck/internal/damage"
	"mediacheck/internal/fixture"
)

func baseSpec() fixture.RTPSpec {
	return fixture.RTPSpec{
		SSRC: 42, PayloadType: 96, ClockRate: 90000,
		Count: 100, BaseSeq: 1000, BaseTimestamp: 0,
		TimestampStep: 3000, // ~33.3 ms packets
	}
}

func TestRecoverCleanStream(t *testing.T) {
	spec := baseSpec()
	res, err := Recover(fixture.BuildRTP(spec), spec.ClockRate, 4)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.Lost != 0 || res.Stats.Recovered != 0 || res.Stats.Duplicate != 0 {
		t.Fatalf("clean stream must have zero defects, got %+v", res.Stats)
	}
	if res.Stats.LossRecoveryRate != 1 {
		t.Fatal("no-loss recovery rate must be exactly 1")
	}
	if len(res.Ordered) != 100 {
		t.Fatalf("ordered = %d, want 100", len(res.Ordered))
	}
	// Timeline must be monotonic nanoseconds.
	for i := 1; i < len(res.Timeline); i++ {
		if res.Timeline[i] < res.Timeline[i-1] {
			t.Fatal("timeline must be non-decreasing")
		}
	}
}

func TestRecoverBurstLossAbove90Percent(t *testing.T) {
	spec := baseSpec()
	// 20-packet burst; 19 retransmitted -> 95% recovery, one unrecovered.
	spec.Bursts = []fixture.RTPBurst{
		{Start: 1020, Length: 20, Retransmit: 19, DelaySlots: 2},
	}
	res, err := Recover(fixture.BuildRTP(spec), spec.ClockRate, 8)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.Lost != 20 {
		t.Fatalf("lost = %d, want 20", res.Stats.Lost)
	}
	if res.Stats.Recovered != 19 || res.Stats.Unrecovered != 1 {
		t.Fatalf("recovered/unrecovered = %d/%d, want 19/1",
			res.Stats.Recovered, res.Stats.Unrecovered)
	}
	if res.Stats.LossRecoveryRate < 0.90 {
		t.Fatalf("recovery rate = %.3f, want >= 0.90", res.Stats.LossRecoveryRate)
	}
	hasUnrecovered := false
	for _, e := range res.Errors {
		if e.Code == damage.CodeRTPLost && e.Recoverable == damage.RecoverableNo {
			hasUnrecovered = true
		}
	}
	if !hasUnrecovered {
		t.Fatal("unrecovered gap must be an explicit no-recover error")
	}
}

func TestRecoverReorderAndDuplicate(t *testing.T) {
	spec := baseSpec()
	spec.ReorderSwaps = []int{5, 20}
	spec.Duplicates = []uint16{1050}
	res, err := Recover(fixture.BuildRTP(spec), spec.ClockRate, 4)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.Reordered < 2 {
		t.Errorf("reordered = %d, want >= 2", res.Stats.Reordered)
	}
	if res.Stats.Duplicate != 1 {
		t.Errorf("duplicate = %d, want 1", res.Stats.Duplicate)
	}
	// Ordering must still be perfectly sequential after reassembly.
	for i := 1; i < len(res.Ordered); i++ {
		if res.Ordered[i].Packet.SequenceNumber != res.Ordered[i-1].Packet.SequenceNumber+1 {
			t.Fatalf("order break at %d", i)
		}
	}
}

func TestRecoverTimestampWrapStaysMonotonic(t *testing.T) {
	spec := baseSpec()
	spec.BaseTimestamp = 0xFFFFFFFF - 5*3000
	spec.Count = 20
	res, err := Recover(fixture.BuildRTP(spec), spec.ClockRate, 4)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(res.Timeline); i++ {
		if res.Timeline[i] < res.Timeline[i-1] {
			t.Fatalf("wrap must expand to monotonic ns at index %d", i)
		}
	}
}

func TestRecoverDeterministic(t *testing.T) {
	spec := baseSpec()
	spec.Bursts = []fixture.RTPBurst{{Start: 1010, Length: 10, Retransmit: 8, DelaySlots: 2}}
	spec.Duplicates = []uint16{1090}
	a, _ := Recover(fixture.BuildRTP(spec), spec.ClockRate, 6)
	b, _ := Recover(fixture.BuildRTP(spec), spec.ClockRate, 6)
	if a.Stats != b.Stats {
		t.Fatalf("identical input gave different stats: %+v vs %+v", a.Stats, b.Stats)
	}
	for i := range a.Timeline {
		if a.Timeline[i] != b.Timeline[i] {
			t.Fatal("timelines diverged on identical input")
		}
	}
}

func TestRecoverRejectsZeroClock(t *testing.T) {
	spec := baseSpec()
	if _, err := Recover(fixture.BuildRTP(spec), 0, 4); err == nil {
		t.Fatal("zero clock rate must be rejected")
	}
}
