package fixture

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateDeterministic(t *testing.T) {
	d1, d2 := t.TempDir(), t.TempDir()
	p1, err := Generate(d1, 17, "all")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := Generate(d2, 17, "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Inputs) != len(p2.Inputs) {
		t.Fatal("input count differs")
	}
	for _, name := range p1.Inputs {
		b1, err := os.ReadFile(filepath.Join(d1, name))
		if err != nil {
			t.Fatal(err)
		}
		b2, err := os.ReadFile(filepath.Join(d2, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(b1) != string(b2) {
			t.Fatalf("fixture %s differs across runs", name)
		}
	}
}

func TestGeneratePlanAll(t *testing.T) {
	p, err := Generate(t.TempDir(), 17, "all")
	if err != nil {
		t.Fatal(err)
	}
	if p.CorruptSegment != 4 || p.CorruptOffset <= 0 {
		t.Fatalf("corrupt plan: %+v", p)
	}
	if p.NormalGOPMaxMs != 2000 || p.InjectedGOPMaxMs != 4000 {
		t.Fatalf("gop plan: %+v", p)
	}
	if p.Duplicates != 1 || p.Reordered != 2 || p.BurstLost != 3 {
		t.Fatalf("rtp plan: %+v", p)
	}
	if p.Discontinuities != 1 || p.TimingJumps != 2 {
		t.Fatalf("timeline plan: %+v", p)
	}
}

func TestGeneratePlanNone(t *testing.T) {
	p, err := Generate(t.TempDir(), 17, "none")
	if err != nil {
		t.Fatal(err)
	}
	if p.CorruptSegment != -1 || p.CorruptOffset != -1 || p.InjectedGOPMaxMs != 2000 {
		t.Fatalf("clean plan: %+v", p)
	}
	if p.Duplicates != 0 || p.Reordered != 0 || p.BurstLost != 0 || p.TimingJumps != 0 {
		t.Fatalf("clean plan faults: %+v", p)
	}
}

func TestParseFaultSet(t *testing.T) {
	if _, err := ParseFaultSet("bogus"); err == nil {
		t.Fatal("unknown fault must fail")
	}
	f, err := ParseFaultSet("rtp,corrupt-nal")
	if err != nil {
		t.Fatal(err)
	}
	if !f.RTP || !f.CorruptNAL || f.TimingJump {
		t.Fatalf("faults: %+v", f)
	}
}

func TestLoadPlanRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p, err := Generate(dir, 99, "all")
	if err != nil {
		t.Fatal(err)
	}
	got, err := LoadPlan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Seed != p.Seed || got.CorruptOffset != p.CorruptOffset || got.BurstLost != p.BurstLost {
		t.Fatalf("plan round trip: %+v vs %+v", got, p)
	}
}
