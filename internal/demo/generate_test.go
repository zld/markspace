package demo

import "testing"

func TestGeneratePlanCounters(t *testing.T) {
	arrival := Generate(17, DefaultPlan())
	plan := DefaultPlan()
	// One duplicate adds one extra datagram; two packets are dropped.
	wantArrival := PacketCount - len(plan.DropSeqs) + 1
	if len(arrival) != wantArrival {
		t.Fatalf("arrival=%d want %d", len(arrival), wantArrival)
	}
	blocks, stats, align, err := Rebuild(arrival)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Duplicates != 1 {
		t.Fatalf("duplicates=%d want 1", stats.Duplicates)
	}
	if stats.Reordered <= 0 {
		t.Fatalf("reordered=%d want >0", stats.Reordered)
	}
	if stats.Gaps != 1 {
		t.Fatalf("gaps=%d want 1 (burst merged)", stats.Gaps)
	}
	if align > 1 {
		t.Fatalf("alignment error %d exceeds one sample", align)
	}
	if len(blocks) == 0 {
		t.Fatal("no blocks reconstructed")
	}
	// The dropped packets must appear as gap samples, not fabricated PCM.
	var gapSamples int64
	for _, b := range blocks {
		gapSamples += b.GapSamples
	}
	if gapSamples != int64(len(plan.DropSeqs))*SamplesPerPacket {
		t.Fatalf("gap samples=%d want %d", gapSamples, int64(len(plan.DropSeqs))*SamplesPerPacket)
	}
}

func TestGenerateDeterministic(t *testing.T) {
	a := Generate(17, DefaultPlan())
	b := Generate(17, DefaultPlan())
	if len(a) != len(b) {
		t.Fatal("non-deterministic arrival length")
	}
	for i := range a {
		if a[i].Seq != b[i].Seq {
			t.Fatalf("arrival order differs at %d", i)
		}
	}
	ba, _, _, _ := Rebuild(a)
	bb, _, _, _ := Rebuild(b)
	if len(ba) != len(bb) {
		t.Fatal("block count differs across runs")
	}
}

func TestPCMIsStereo16Bit(t *testing.T) {
	pcm := PCM(17)
	want := PacketCount * SamplesPerPacket * Channels * (Bits / 8)
	if len(pcm) != want {
		t.Fatalf("pcm length=%d want %d", len(pcm), want)
	}
}
