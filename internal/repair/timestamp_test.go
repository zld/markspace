package repair

import (
	"testing"

	"replayproof/internal/media"
	"replayproof/internal/parse"
)

func vTrack(samples []parse.Sample) *parse.Track {
	return &parse.Track{ID: 1, Kind: parse.KindVideo, Timescale: 90000, Samples: samples}
}

func smp(n int, ticks int64, durTicks int64) parse.Sample {
	d := media.FromTicks(ticks, 90000)
	dur := media.FromTicks(durTicks, 90000)
	return parse.Sample{Number: n, DTS: d, PTS: d, Duration: dur, Offset: int64(n * 100), Keyframe: n == 1}
}

func TestRepairLeavesLegalIntervals(t *testing.T) {
	tr := vTrack([]parse.Sample{smp(1, 0, 3000), smp(2, 3000, 3000), smp(3, 9000, 3000)})
	res, err := RepairTrack(tr, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fixes) != 0 {
		t.Fatalf("unexpected fixes: %#v", res.Fixes)
	}
	if res.Samples[2].DTS.Micros() != 100000 {
		t.Fatalf("gap not preserved: %d", res.Samples[2].DTS.Micros())
	}
	if err := ValidateMonotonic(res.Samples); err != nil {
		t.Fatal(err)
	}
}

func TestRepairMissingTimestampInheritsPreviousDuration(t *testing.T) {
	tr := vTrack([]parse.Sample{smp(1, 0, 3000), smp(2, 3000, 0), smp(3, 6000, 3000)})
	res, err := RepairTrack(tr, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Samples[1].Duration.Micros() != 33333 {
		t.Fatalf("duration=%d", res.Samples[1].Duration.Micros())
	}
	f := res.Fixes[0]
	if f.Kind != FixMissing || f.BeforeDur.Cmp(media.Zero) != 0 || f.AfterDur.Micros() != 33333 {
		t.Fatalf("fix=%#v", f)
	}
	if f.BeforeDTS.Cmp(f.AfterDTS) != 0 {
		t.Fatalf("missing-duration fix must not move DTS: %#v", f)
	}
}

func TestRepairDuplicateMovesToPreviousEnd(t *testing.T) {
	// Samples 2 and 3 share DTS=3000 (zero delta produced 3 -> 3).
	tr := vTrack([]parse.Sample{smp(1, 0, 3000), smp(2, 3000, 3000), smp(3, 3000, 3000)})
	res, err := RepairTrack(tr, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fixes) != 1 || res.Fixes[0].Kind != FixDuplicate || res.Fixes[0].SampleNumber != 3 {
		t.Fatalf("fixes=%#v", res.Fixes)
	}
	if res.Samples[2].DTS.Micros() != 66667 {
		t.Fatalf("dts=%d us", res.Samples[2].DTS.Micros())
	}
	if res.Fixes[0].BeforeDTS.Micros() != 33333 || res.Fixes[0].AfterDTS.Micros() != 66667 {
		t.Fatalf("fix before/after: %#v", res.Fixes[0])
	}
	if err := ValidateMonotonic(res.Samples); err != nil {
		t.Fatal(err)
	}
}

func TestRepairBackwardsClamps(t *testing.T) {
	tr := vTrack([]parse.Sample{smp(1, 3000, 3000), smp(2, 0, 3000)})
	res, err := RepairTrack(tr, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fixes) != 1 || res.Fixes[0].Kind != FixBackwards {
		t.Fatalf("fixes=%#v", res.Fixes)
	}
	if res.Samples[1].DTS.Micros() != 66667 {
		t.Fatalf("dts=%d", res.Samples[1].DTS.Micros())
	}
}

func TestRepairFirstSampleZeroDurationUsesPolicy(t *testing.T) {
	tr := vTrack([]parse.Sample{smp(1, 0, 0)})
	if _, err := RepairTrack(tr, Policy{}); err == nil {
		t.Fatal("expected error without default duration")
	}
	res, err := RepairTrack(tr, Policy{DefaultDuration: media.FromTicks(3000, 90000)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Samples[0].Duration.Micros() != 33333 {
		t.Fatalf("duration=%d", res.Samples[0].Duration.Micros())
	}
	if res.Fixes[0].Kind != FixMissing {
		t.Fatalf("fix=%#v", res.Fixes[0])
	}
}

func TestRepairPTSFollowsDTSShift(t *testing.T) {
	s2 := smp(2, 0, 3000)
	tr := vTrack([]parse.Sample{smp(1, 3000, 3000), s2})
	res, err := RepairTrack(tr, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Samples[1].PTS.Cmp(res.Samples[1].DTS) != 0 {
		t.Fatalf("pts=%v dts=%v", res.Samples[1].PTS, res.Samples[1].DTS)
	}
}

func TestRepairNilTrack(t *testing.T) {
	if _, err := RepairTrack(nil, Policy{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateMonotonicDetectsOverlap(t *testing.T) {
	samples := []parse.Sample{smp(1, 0, 3000), smp(2, 1000, 3000)}
	if err := ValidateMonotonic(samples); err == nil {
		t.Fatal("expected overlap error")
	}
}

func TestCounts(t *testing.T) {
	res := &Result{Fixes: []TimestampFix{{Kind: FixMissing}, {Kind: FixMissing}, {Kind: FixDuplicate}}}
	c := res.Counts()
	if c[FixMissing] != 2 || c[FixDuplicate] != 1 {
		t.Fatalf("counts=%#v", c)
	}
}
