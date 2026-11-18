package testkit

import (
	"reflect"
	"testing"

	"jitterlab/internal/packet"
)

func baseOptions() Options {
	return Options{
		Frames: 200, FrameMs: 20, Seed: 41,
		Loss: 0.1, Reorder: 0.1, Duplicate: 0.05,
		ReorderDelay: 3, DuplicateDelay: 3,
	}
}

func TestOptionsValidation(t *testing.T) {
	bad := []Options{
		{Frames: 0},
		{Frames: MaxFrames + 1},
		{Frames: 10, FrameMs: 0},
		{Frames: 10, FrameMs: 20, Loss: -0.1},
		{Frames: 10, FrameMs: 20, Reorder: 1.5},
		{Frames: 10, FrameMs: 20, BurstLen: 11},
		{Frames: 10, FrameMs: 20, DriftPPM: 200_000},
	}
	for i, opt := range bad {
		if err := opt.Validate(); err == nil {
			t.Errorf("case %d: accepted invalid options %+v", i, opt)
		}
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	opt := baseOptions()
	a, err := Generate(opt)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate(opt)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same options produced different plans")
	}
	c, _ := Generate(Options{Frames: 200, FrameMs: 20, Seed: 42, Loss: 0.1})
	if reflect.DeepEqual(a.Stats, c.Stats) {
		t.Fatal("different seeds produced identical stats")
	}
}

func TestGenerateInjectsEveryImpairment(t *testing.T) {
	opt := baseOptions()
	opt.Corrupt = 0.05
	opt.Truncate = 0.05
	opt.BadVersion = 0.05
	opt.BurstStart = 0.05
	opt.BurstLen = 3
	p, err := Generate(opt)
	if err != nil {
		t.Fatal(err)
	}
	if p.Stats.Lost == 0 || p.Stats.Reordered == 0 || p.Stats.Duplicated == 0 {
		t.Fatalf("stats = %+v, want loss, reorder and duplicates", p.Stats)
	}
	if p.Stats.Faults == 0 || p.Stats.Bursts == 0 {
		t.Fatalf("stats = %+v, want faults and bursts", p.Stats)
	}
	seen := map[Fault]bool{}
	for _, ev := range p.Events {
		seen[ev.Fault] = true
		if !ev.Dropped && len(ev.Wire) > 0 && ev.Fault == FaultNone {
			if _, err := packet.Decode(ev.Wire); err != nil {
				t.Fatalf("clean event fails to decode: %v", err)
			}
		}
	}
	for _, f := range []Fault{FaultChecksum, FaultTruncated, FaultVersion} {
		if !seen[f] {
			t.Errorf("fault %v never generated", f)
		}
	}
}

func TestGenerateWithoutLossIsClean(t *testing.T) {
	p, err := Generate(Options{Frames: 50, FrameMs: 10, Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	if p.Stats.Lost != 0 || p.Stats.Faults != 0 {
		t.Fatalf("stats = %+v", p.Stats)
	}
	if len(p.Events) != 50 {
		t.Fatalf("events = %d, want 50", len(p.Events))
	}
	for i, ev := range p.Events {
		if ev.AtMs != int64(i)*10 || ev.Seq != uint64(i) {
			t.Fatalf("event %d = %+v", i, ev)
		}
	}
}

func TestStretchMs(t *testing.T) {
	o := Options{DriftPPM: 10_000}
	if got := o.StretchMs(1000); got != 1010 {
		t.Fatalf("StretchMs(1000) = %d, want 1010", got)
	}
	o.DriftPPM = -10_000
	if got := o.StretchMs(1000); got != 990 {
		t.Fatalf("StretchMs(1000) = %d, want 990", got)
	}
}
