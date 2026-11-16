package ingress

import (
	"testing"

	"jitter/internal/wire"
)

func params() TraceParams {
	return TraceParams{Seed: 41, Frames: 600, Loss: 0.04, Reorder: 0.08, Duplicate: 0.02}
}

func TestGenerateTraceDeterministic(t *testing.T) {
	a := GenerateTrace(params())
	b := GenerateTrace(params())
	if len(a) != len(b) {
		t.Fatalf("lengths differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].At != b[i].At || a[i].Pkt.Seq != b[i].Pkt.Seq || a[i].Pkt.Timestamp != b[i].Pkt.Timestamp {
			t.Fatalf("event %d differs between runs", i)
		}
	}
}

func TestGenerateTraceEventMix(t *testing.T) {
	evs := GenerateTrace(params())
	seen := map[uint16]int{}
	var dups, outOfOrder int
	prev := -1
	for _, ev := range evs {
		seen[ev.Pkt.Seq]++
		if int(ev.Pkt.Seq) < prev {
			outOfOrder++
		}
		prev = int(ev.Pkt.Seq)
	}
	for _, c := range seen {
		if c > 1 {
			dups++
		}
	}
	if len(seen) == 0 || len(seen) >= 600 {
		t.Fatalf("unique seqs = %d, want within (0, 600)", len(seen))
	}
	if dups == 0 {
		t.Fatal("expected duplicated frames")
	}
	if outOfOrder == 0 {
		t.Fatal("expected reordered arrivals")
	}
	lost := 600 - len(seen)
	if lost < 5 || lost > 60 {
		t.Fatalf("lost = %d, want roughly 24 (4%% of 600)", lost)
	}
}

func TestGenerateTracePayloadDecodes(t *testing.T) {
	evs := GenerateTrace(TraceParams{Seed: 1, Frames: 10})
	if len(evs) != 10 {
		t.Fatalf("events = %d, want 10 (no impairments)", len(evs))
	}
	for i, ev := range evs {
		b, err := wire.Encode(ev.Pkt)
		if err != nil {
			t.Fatalf("Encode event %d: %v", i, err)
		}
		if _, err := wire.Decode(b); err != nil {
			t.Fatalf("Decode event %d: %v", i, err)
		}
	}
}

func TestExchangeUDPPreservesEvents(t *testing.T) {
	in := GenerateTrace(TraceParams{Seed: 7, Frames: 50, Loss: 0.1, Reorder: 0.2, Duplicate: 0.1})
	out, malformed, err := ExchangeUDP(in)
	if err != nil {
		t.Fatalf("ExchangeUDP: %v", err)
	}
	if malformed != 0 {
		t.Fatalf("malformed = %d, want 0", malformed)
	}
	if len(out) != len(in) {
		t.Fatalf("received %d events, sent %d", len(out), len(in))
	}
	for i := range in {
		if out[i].Pkt.Seq != in[i].Pkt.Seq || out[i].At != in[i].At {
			t.Fatalf("event %d changed in transit", i)
		}
		if string(out[i].Pkt.Payload) != string(in[i].Pkt.Payload) {
			t.Fatalf("event %d payload corrupted", i)
		}
	}
}
