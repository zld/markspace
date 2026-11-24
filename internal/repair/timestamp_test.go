package repair

import (
	"errors"
	"testing"

	"mediarun/internal/fault"
	"mediarun/internal/parse"
)

func frag(samples ...parse.Sample) *parse.Fragment {
	return &parse.Fragment{Path: "f.m4s", MoofOffset: 128, SeqNumber: 1, Samples: samples}
}

func smp(dt uint64, dur uint32) parse.Sample {
	return parse.Sample{DecodeTime: dt, Duration: dur, Size: 10, Sync: true}
}

func TestMonotonicPassthrough(t *testing.T) {
	r := New("v1", 90000, false, 1<<31)
	rep := &Report{Track: "v1"}
	out, err := r.RepairFragment(frag(smp(0, 3000), smp(3000, 3000), smp(6000, 3000)), rep)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Count() != 0 {
		t.Fatalf("unexpected repairs: %+v", rep.Entries)
	}
	for i, want := range []uint64{0, 3000, 6000} {
		if out[i] != want {
			t.Fatalf("out[%d] = %d, want %d", i, out[i], want)
		}
	}
}

func TestDuplicateAdvancedByDuration(t *testing.T) {
	r := New("v1", 90000, false, 1<<31)
	rep := &Report{Track: "v1"}
	out, err := r.RepairFragment(frag(smp(0, 3000), smp(0, 3000), smp(3000, 3000)), rep)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Count() != 2 {
		t.Fatalf("repairs = %d", rep.Count())
	}
	want := []uint64{0, 3000, 6000}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("out[%d] = %d, want %d", i, out[i], want[i])
		}
	}
	if rep.Entries[0].Why != ReasonDuplicate {
		t.Fatalf("why = %s", rep.Entries[0].Why)
	}
}

func TestBackwardJumpRepaired(t *testing.T) {
	r := New("v1", 90000, false, 1<<31)
	rep := &Report{Track: "v1"}
	out, err := r.RepairFragment(frag(smp(9000, 3000), smp(6000, 3000), smp(15000, 3000)), rep)
	if err != nil {
		t.Fatal(err)
	}
	want := []uint64{9000, 12000, 15000}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("out[%d] = %d, want %d", i, out[i], want[i])
		}
	}
	if rep.Entries[0].Why != ReasonBackward {
		t.Fatalf("why = %s", rep.Entries[0].Why)
	}
}

func TestZeroDurationDuplicateStopsTrack(t *testing.T) {
	r := New("v1", 90000, false, 1<<31)
	rep := &Report{Track: "v1"}
	out, err := r.RepairFragment(frag(smp(5000, 0), smp(5000, 0), smp(9000, 3000)), rep)
	if err == nil {
		t.Fatal("expected stop")
	}
	var fe *fault.Error
	if !errors.As(err, &fe) {
		t.Fatalf("want fault.Error, got %T", err)
	}
	if fe.Stage != fault.StageRepair || fe.Offset != 128 || fe.Path != "f.m4s" {
		t.Fatalf("bad fault: %+v", fe)
	}
	if !rep.Stopped || rep.Completed != 1 {
		t.Fatalf("report: %+v", rep)
	}
	if len(out) != 1 || out[0] != 5000 {
		t.Fatalf("completed samples not preserved: %v", out)
	}
}

func TestHugeBackwardJumpStopsTrack(t *testing.T) {
	r := New("v1", 90000, false, 1000)
	rep := &Report{Track: "v1"}
	_, err := r.RepairFragment(frag(smp(1<<40, 3000), smp(100, 3000)), rep)
	if err == nil || !rep.Stopped {
		t.Fatalf("expected stop, rep=%+v err=%v", rep, err)
	}
}

func TestRTPWraparound(t *testing.T) {
	r := New("a1", 90000, true, 1<<31)
	rep := &Report{Track: "a1"}
	const max32 = 1<<32 - 1
	// Three samples straddling the 32-bit wrap point.
	f := frag(
		smp(max32-2999, 3000), // global 0
		smp(max32-2999+3000, 3000), // global 1: max32+1 -> wraps to 0
		smp(3000, 3000),        // global 2
	)
	f.Samples[1].DecodeTime = 0 // raw 32-bit value after wrap
	out, err := r.RepairFragment(f, rep)
	if err != nil {
		t.Fatal(err)
	}
	want := []uint64{max32 - 2999, 1 << 32, (1 << 32) + 3000}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("out[%d] = %d, want %d", i, out[i], want[i])
		}
	}
	if rep.Entries[0].Why != ReasonWrap {
		t.Fatalf("why = %s", rep.Entries[0].Why)
	}
	// Monotonic across the wrap.
	for i := 1; i < len(out); i++ {
		if out[i] <= out[i-1] {
			t.Fatal("not monotonic")
		}
	}
}

func TestRTPSequenceDisorderStopsTrack(t *testing.T) {
	r := New("a1", 90000, true, 1<<31)
	rep := &Report{Track: "a1"}
	if _, err := r.RepairFragment(frag(smp(100, 3000)), rep); err != nil {
		t.Fatal(err)
	}
	// Next fragment starts at global index 5: sequence jump 0 -> 5.
	r.global = 5
	_, err := r.RepairFragment(frag(smp(3100, 3000)), rep)
	if err == nil || !rep.Stopped {
		t.Fatalf("expected stop, rep=%+v err=%v", rep, err)
	}
	if rep.Completed != 1 {
		t.Fatalf("completed = %d", rep.Completed)
	}
}
