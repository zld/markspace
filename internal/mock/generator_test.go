package mock

import (
	"testing"

	"rtspcheck/internal/rtp"
)

func TestGenerateStreamDeterministic(t *testing.T) {
	cfg := FaultConfig{
		Seed:           41,
		DropEvery:      19,
		DuplicateEvery: 23,
		Reorder:        0.3,
		Delay:          5,
	}
	first := GenerateStream(120, cfg)
	second := GenerateStream(120, cfg)
	if len(first) != len(second) {
		t.Fatalf("length differs: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Seq != second[i].Seq || first[i].DelayJitter != second[i].DelayJitter {
			t.Fatalf("spec %d differs across runs", i)
		}
		if string(first[i].Data) != string(second[i].Data) {
			t.Fatalf("packet data %d differs across runs", i)
		}
	}
}

func TestGenerateStreamNoFaults(t *testing.T) {
	specs := GenerateStream(120, FaultConfig{Seed: 1})
	if want := 120 * packetsPerFrame; len(specs) != want {
		t.Fatalf("got %d packets, want %d", len(specs), want)
	}
	// Every generated datagram must parse with our own strict parser.
	for i, spec := range specs {
		p, err := rtp.Parse(spec.Data)
		if err != nil {
			t.Fatalf("packet %d failed own parser: %v", i, err)
		}
		wantMarker := i%packetsPerFrame == packetsPerFrame-1
		if p.Marker != wantMarker {
			t.Fatalf("packet %d marker = %v, want %v", i, p.Marker, wantMarker)
		}
	}
}

func TestGenerateStreamDropsAndDuplicates(t *testing.T) {
	const frames = 100
	dropped := GenerateStream(frames, FaultConfig{Seed: 7, DropEvery: 19})
	base := frames * packetsPerFrame
	// seq values divisible by 19 are dropped (across the 16-bit space).
	wantDrops := 0
	for seq := 0; seq < base; seq++ {
		if seq%19 == 0 {
			wantDrops++
		}
	}
	if len(dropped) != base-wantDrops {
		t.Fatalf("drop schedule = %d packets, want %d", len(dropped), base-wantDrops)
	}

	dup := GenerateStream(frames, FaultConfig{Seed: 7, DuplicateEvery: 23})
	wantDup := 0
	for seq := 0; seq < base; seq++ {
		if seq%23 == 0 {
			wantDup++
		}
	}
	if len(dup) != base+wantDup {
		t.Fatalf("duplicate schedule = %d packets, want %d", len(dup), base+wantDup)
	}
}

func TestGenerateStreamMarkerTimestamps(t *testing.T) {
	specs := GenerateStream(3, FaultConfig{Seed: 2})
	parsed := make([]*rtp.Packet, len(specs))
	for i, s := range specs {
		p, err := rtp.Parse(s.Data)
		if err != nil {
			t.Fatal(err)
		}
		parsed[i] = p
	}
	// Three frames -> three distinct timestamps, three markers.
	tsSeen := map[uint32]bool{}
	markers := 0
	for _, p := range parsed {
		tsSeen[p.Timestamp] = true
		if p.Marker {
			markers++
		}
	}
	if len(tsSeen) != 3 || markers != 3 {
		t.Fatalf("timestamps=%d markers=%d, want 3/3", len(tsSeen), markers)
	}
}
