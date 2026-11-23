package rtp

import (
	"fmt"
	"sort"
	"testing"
)

func TestWindowBoundPreventsUnboundedBuffering(t *testing.T) {
	c := NewCursor(8)
	var out []Packet
	// Feed 1..3 then a far-ahead packet; once it is 8 ahead of next, the hole
	// must be declared and delivery must resume.
	for _, s := range []uint16{1, 2, 3, 20} {
		out = c.Push(Packet{Sequence: s}, out)
		if c.Pending() > 8 {
			t.Fatalf("pending %d exceeds window 8", c.Pending())
		}
	}
	if c.Stats().Lost == 0 {
		t.Fatal("expected holes beyond the window to be declared lost")
	}
	if len(out) == 0 {
		t.Fatal("expected delivery to resume after the gap")
	}
}

func TestSequenceWrap(t *testing.T) {
	// Cross the 16-bit wrap inside a reorder group.
	seqs := []uint16{65533, 65534, 65535, 1, 0, 2}
	got, stats, err := Recover(wire(t, seqs), Window)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Lost != 0 {
		t.Fatalf("lost = %d on wrap: %s", stats.Lost, stats)
	}
	want := []uint16{65533, 65534, 65535, 0, 1, 2}
	if len(got) != len(want) {
		t.Fatalf("delivered %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Sequence != want[i] {
			t.Fatalf("delivery[%d] = %d want %d", i, got[i].Sequence, want[i])
		}
	}
}

func TestDuplicatesCounted(t *testing.T) {
	seqs := []uint16{1, 2, 2, 3, 3, 4}
	_, stats, err := Recover(wire(t, seqs), Window)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Duplicate != 2 {
		t.Fatalf("duplicates = %d want 2 (%s)", stats.Duplicate, stats)
	}
	if stats.Lost != 0 {
		t.Fatalf("lost = %d", stats.Lost)
	}
}

func TestFlushDeliversTrailingBuffer(t *testing.T) {
	c := NewCursor(Window)
	var out []Packet
	out = c.Push(Packet{Sequence: 10}, out)
	out = c.Push(Packet{Sequence: 11}, out)
	out = c.Flush(out)
	if len(out) != 2 {
		t.Fatalf("flushed %d want 2", len(out))
	}
	if c.Stats().Lost != 0 {
		t.Fatalf("lost = %d, want 0", c.Stats().Lost)
	}
}

func TestMalformedPacketRejected(t *testing.T) {
	_, _, err := Recover([][]byte{{0x80}}, Window)
	if err == nil {
		t.Fatal("expected parse error for truncated RTP header")
	}
}

func TestPionRoundTrip(t *testing.T) {
	payload := make([]byte, 1200)
	for i := range payload {
		payload[i] = byte(i)
	}
	b, err := BuildPacket(4242, payload, true)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePacket(b)
	if err != nil {
		t.Fatal(err)
	}
	if p.Sequence != 4242 || p.PayloadLen != 1200 || !p.Marker {
		t.Fatalf("round trip lost fields: %+v", p)
	}
	if len(b) <= 1200 {
		t.Fatalf("wire packet %d bytes, expected RTP header overhead", len(b))
	}
}

func TestDeterministicReplay(t *testing.T) {
	// A seeded arrival order must reproduce identical deliveries and stats.
	src := make([]Packet, 0, 500)
	for i := 1; i <= 500; i++ {
		if i%17 == 0 {
			continue
		}
		src = append(src, Packet{Sequence: uint16(i)})
	}
	jitter := func(seed int64) []Packet {
		// Deterministic local shuffle of a small neighbourhood.
		out := append([]Packet(nil), src...)
		x := uint64(seed)
		for i := range out {
			x = x*6364136223846793005 + 1442695040888963407
			j := i + int(x%3)
			if j < len(out) {
				out[i], out[j] = out[j], out[i]
			}
		}
		return out
	}
	a, sa := RecoverPackets(jitter(1), Window)
	b, sb := RecoverPackets(jitter(1), Window)
	if fmt.Sprint(sa) != fmt.Sprint(sb) || len(a) != len(b) {
		t.Fatal("replay not deterministic")
	}
	for i := range a {
		if a[i].Sequence != b[i].Sequence {
			t.Fatal("delivery differs across identical replays")
		}
	}
	_ = sort.Ints
}
