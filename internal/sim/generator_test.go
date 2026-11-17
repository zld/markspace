package sim

import (
	"testing"

	"jbcheck/internal/packet"
)

func testConfig() Config {
	return Config{
		Seed: 73, Frames: 1000,
		Loss: 0.06, Burst: 0.02, Reorder: 0.10, Duplicate: 0.03,
		JitterMs: 20,
	}
}

func TestGenerateDeterministic(t *testing.T) {
	a := Generate(testConfig())
	b := Generate(testConfig())
	if len(a) != len(b) {
		t.Fatalf("length %d != %d", len(a), len(b))
	}
	for i := range a {
		if a[i].ArrivalMs != b[i].ArrivalMs || string(a[i].Data) != string(b[i].Data) {
			t.Fatalf("event %d differs", i)
		}
	}
}

func TestGenerateLossApprox(t *testing.T) {
	events := Generate(testConfig())
	seen := make(map[uint16]int)
	dups := 0
	maxArrival := int64(-1)
	sorted := true
	prev := int64(-1 << 60)
	for _, ev := range events {
		p, err := packet.Decode(ev.Data)
		if err != nil {
			t.Fatalf("generated packet invalid: %v", err)
		}
		seen[p.Seq]++
		if seen[p.Seq] == 2 {
			dups++
		}
		if ev.ArrivalMs < prev {
			sorted = false
		}
		prev = ev.ArrivalMs
		if ev.ArrivalMs > maxArrival {
			maxArrival = ev.ArrivalMs
		}
	}
	if !sorted {
		t.Fatal("events not sorted by arrival")
	}
	lost := 1000 - len(seen)
	if lost < 40 || lost > 90 {
		t.Fatalf("lost=%d, want roughly 60", lost)
	}
	if dups < 15 || dups > 50 {
		t.Fatalf("dups=%d, want roughly 30", dups)
	}
	if maxArrival < 9000 || maxArrival > 11000 {
		t.Fatalf("max arrival %d ms out of range", maxArrival)
	}
}

func TestPCMFrameContinuous(t *testing.T) {
	a := PCMFrame(0)
	b := PCMFrame(1)
	// Phase-continuous tone: boundary step must be small.
	d := int(b[0]) - int(a[FrameSamples-1])
	if d < -400 || d > 400 {
		t.Fatalf("boundary step %d too large", d)
	}
}
