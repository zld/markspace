package sim

import (
	"testing"
	"time"
)

func TestGenerateDeterministic(t *testing.T) {
	cfg := Config{Seed: 424242, Duration: 8 * time.Second, FramePeriod: 20 * time.Millisecond}
	a := Generate(cfg)
	b := Generate(cfg)
	if len(a) != len(b) {
		t.Fatalf("lengths %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].At != b[i].At || a[i].Pkt.Seq != b[i].Pkt.Seq {
			t.Fatalf("event %d differs", i)
		}
	}
}

func TestGenerateWrapsSequence(t *testing.T) {
	ev := Generate(Config{Seed: 1, Duration: 8 * time.Second, FramePeriod: 20 * time.Millisecond})
	seen := make(map[uint16]bool)
	wrapped := false
	var maxSeq uint16
	for _, e := range ev {
		seen[e.Pkt.Seq] = true
		if e.Pkt.Seq > maxSeq {
			maxSeq = e.Pkt.Seq
		}
	}
	if !seen[0] && !seen[1] {
		t.Fatal("sequence never wrapped past 65535")
	}
	if maxSeq < 65000 {
		t.Fatalf("max seq %d", maxSeq)
	}
	_ = wrapped
}

func TestGenerateHasImpairments(t *testing.T) {
	cfg := Config{Seed: 424242, Duration: 8 * time.Second, FramePeriod: 20 * time.Millisecond}
	ev := Generate(cfg)
	total := int(cfg.Duration / cfg.FramePeriod)
	counts := make(map[uint16]int)
	for _, e := range ev {
		counts[e.Pkt.Seq]++
	}
	dups, unique := 0, 0
	for _, n := range counts {
		unique++
		if n > 1 {
			dups++
		}
	}
	lost := total - unique
	if lost < 10 {
		t.Fatalf("too few losses: %d", lost)
	}
	if dups == 0 {
		t.Fatal("no duplicates generated")
	}
	// Sorted by send time.
	for i := 1; i < len(ev); i++ {
		if ev[i].At < ev[i-1].At {
			t.Fatal("events not sorted")
		}
	}
}

func TestToneContinuous(t *testing.T) {
	// Consecutive frames from the tone generator must have small
	// boundary jumps.
	phase := 0.0
	var prev []byte
	for i := 0; i < 5; i++ {
		pcm, ph := tonePCM(phase)
		phase = ph
		if prev != nil {
			ps := pcm[len(pcm)-2:]
			_ = ps
			last := int(int16(uint16(prev[len(prev)-2]) | uint16(prev[len(prev)-1])<<8))
			first := int(int16(uint16(pcm[0]) | uint16(pcm[1])<<8))
			d := last - first
			if d < 0 {
				d = -d
			}
			if d > 2000 {
				t.Fatalf("boundary jump %d", d)
			}
		}
		prev = pcm
	}
}
