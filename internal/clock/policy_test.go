package clock

import (
	"errors"
	"testing"
)

func sample(ticks int64, ts uint32, present bool, durTicks int64) Sample {
	s := Sample{Present: present}
	if present {
		s.Declared = MustMake(ticks, int64(ts))
	}
	if durTicks > 0 {
		s.Duration = MustMake(durTicks, int64(ts))
	}
	return s
}

func TestRepairTrackMonotonic(t *testing.T) {
	// 25 fps track; sample 2 goes backwards, sample 3 repeats.
	in := []Sample{
		sample(0, 25, true, 1),
		sample(1, 25, true, 1),
		sample(0, 25, true, 1), // backward
		sample(2, 25, true, 1), // would be duplicate of repaired sample 2
		sample(3, 25, true, 1),
	}
	out, err := RepairTrack(DefaultPolicy(), in)
	if err != nil {
		t.Fatal(err)
	}
	want := []Rational{
		MustMake(0, 25), MustMake(1, 25), MustMake(2, 25),
		MustMake(3, 25), MustMake(4, 25),
	}
	for i := range out {
		if out[i].Value.Cmp(want[i]) != 0 {
			t.Fatalf("sample %d: got %s want %s", i, out[i].Value, want[i])
		}
	}
	if out[2].Reason != ReasonBackward {
		t.Fatalf("sample 2 reason = %q", out[2].Reason)
	}
	if out[3].Reason != ReasonDuplicate {
		t.Fatalf("sample 3 reason = %q", out[3].Reason)
	}
	if out[0].Reason != ReasonNone || out[1].Reason != ReasonNone {
		t.Fatal("clean samples must be marked ReasonNone")
	}
	// Original value is retained for audit.
	if out[2].Original.Cmp(MustMake(0, 25)) != 0 {
		t.Fatalf("original lost: %s", out[2].Original)
	}
}

func TestRepairTrackMissingContinues(t *testing.T) {
	in := []Sample{
		sample(10, 25, true, 1),
		sample(11, 25, true, 1),
		{}, // missing entirely
		sample(13, 25, true, 1),
	}
	out, err := RepairTrack(DefaultPolicy(), in)
	if err != nil {
		t.Fatal(err)
	}
	if out[2].Reason != ReasonMissing {
		t.Fatalf("reason = %q", out[2].Reason)
	}
	if out[2].Value.Cmp(MustMake(12, 25)) != 0 {
		t.Fatalf("missing sample placed at %s", out[2].Value)
	}
}

func TestRepairTrackStrictRejectsBackward(t *testing.T) {
	p := DefaultPolicy()
	p.Mode = ModeStrict
	in := []Sample{sample(1, 25, true, 1), sample(0, 25, true, 1)}
	_, err := RepairTrack(p, in)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("want ErrCorrupt, got %v", err)
	}
}

func TestRepairTrackAppliesOffset(t *testing.T) {
	p := DefaultPolicy()
	// Backup source stamps restart at zero; shift it one second forward.
	p.Offset = Shift{N: 1, D: 1}
	in := []Sample{
		sample(0, 25, true, 1),
		sample(1, 25, true, 1),
	}
	out, err := RepairTrack(p, in)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Value.Cmp(MustMake(1, 1)) != 0 {
		t.Fatalf("offset not applied: %s", out[0].Value)
	}
	if out[1].Value.Cmp(MustMake(26, 25)) != 0 {
		t.Fatalf("second sample: %s", out[1].Value)
	}
	if out[0].Reason != ReasonOffset && out[0].Reason != ReasonNone {
		// Offset alone on an otherwise good sample is not a defect.
	}
}

func TestRepairTrackDoesNotMutateInput(t *testing.T) {
	in := []Sample{sample(5, 25, true, 1)}
	snapshot := in[0]
	if _, err := RepairTrack(DefaultPolicy(), in); err != nil {
		t.Fatal(err)
	}
	if in[0].Declared.Cmp(snapshot.Declared) != 0 || in[0].Present != snapshot.Present {
		t.Fatal("input slice was mutated")
	}
}

func TestRepairTrackFirstMissingStartsAtZero(t *testing.T) {
	out, err := RepairTrack(DefaultPolicy(), []Sample{{}, sample(1, 25, true, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Value.Cmp(Zero()) != 0 {
		t.Fatalf("first missing sample at %s, want zero", out[0].Value)
	}
}
