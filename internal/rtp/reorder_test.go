package rtp

import (
	"testing"
	"time"
)

func collect(r *Reorderer, pkts ...Packet) []uint16 {
	var seq []uint16
	for _, p := range pkts {
		for _, out := range r.Push(p) {
			seq = append(seq, out.Sequence)
		}
	}
	for _, out := range r.Finish() {
		seq = append(seq, out.Sequence)
	}
	return seq
}

func TestReorderDeduplicateAndFinish(t *testing.T) {
	r := NewReorderer(16)
	got := collect(r,
		Packet{Sequence: 10, Payload: []byte("a")},
		Packet{Sequence: 12, Payload: []byte("c")},
		Packet{Sequence: 12, Payload: []byte("c-dup")},
		Packet{Sequence: 11, Payload: []byte("b")},
		Packet{Sequence: 13, Payload: []byte("d")},
	)
	want := []uint16{10, 11, 12, 13}
	if len(got) != len(want) {
		t.Fatalf("sequences=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sequences=%v want %v", got, want)
		}
	}
	if r.Stats().Duplicates != 1 {
		t.Fatalf("duplicates=%d want 1", r.Stats().Duplicates)
	}
}

func TestMissingSequenceIsGapWithoutFakePayload(t *testing.T) {
	r := NewReorderer(8)
	got := collect(r,
		Packet{Sequence: 100, Payload: []byte("a")},
		Packet{Sequence: 102, Payload: []byte("c")},
	)
	if len(got) != 2 || got[0] != 100 || got[1] != 102 {
		t.Fatalf("got %v, want real packets 100 and 102", got)
	}
	if r.Stats().Gaps != 1 || len(r.Events()) != 1 {
		t.Fatalf("gaps=%d events=%d", r.Stats().Gaps, len(r.Events()))
	}
}

func TestLatePacketAndBandwidthDoNotRewriteCommittedOrder(t *testing.T) {
	clock := &fakeClock{t: time.Unix(100, 0)}
	r := NewReorderer(4, WithClock(clock.now))
	first := r.Push(Packet{Sequence: 500})
	if len(first) != 1 {
		t.Fatal("first packet not committed")
	}
	r.SetBandwidth(10_000_000, 188)
	r.SetBandwidth(50_000, 188)
	late := r.Push(Packet{Sequence: 400})
	if late != nil {
		t.Fatal("late packet must not be inserted into committed stream")
	}
	if r.Stats().Late != 1 {
		t.Fatalf("late=%d want 1", r.Stats().Late)
	}
}

func TestRTPWireRoundTrip(t *testing.T) {
	p := Packet{Sequence: 7, Timestamp: 9000, SSRC: 42, Payload: []byte("payload")}
	wire, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Unmarshal(wire, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Sequence != 7 || parsed.SSRC != 42 || string(parsed.Payload) != "payload" {
		t.Fatalf("bad round trip: %+v", parsed)
	}
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }
