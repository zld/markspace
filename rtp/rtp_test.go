package rtp

import (
	"testing"

	pionrtp "github.com/pion/rtp"

	"mediacore/media"
)

func mkPackets(t *testing.T, n, size int) []*pionrtp.Packet {
	t.Helper()
	p := NewPacketizer(0x1234, 96, media.VideoScale)
	var out []*pionrtp.Packet
	for i := 0; i < n; i++ {
		payload := make([]byte, size)
		for j := range payload {
			payload[j] = byte(i)
		}
		pkts, err := p.Packetize(payload, media.Ticks(int64(i)*3000))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, pkts...)
	}
	return out
}

func drainAll(r *Recover, pkts []*pionrtp.Packet) []Recovered {
	var out []Recovered
	for _, p := range pkts {
		out = append(out, r.Push(p)...)
	}
	return append(out, r.Flush()...)
}

func TestPacketizeRoundTrip(t *testing.T) {
	pkts := mkPackets(t, 3, MaxPayload*2+100) // forces fragmentation
	if len(pkts) != 9 {
		t.Fatalf("got %d packets, want 9", len(pkts))
	}
	if !pkts[2].Marker || pkts[0].Marker {
		t.Fatal("marker must be set only on last fragment")
	}
	r := NewRecover(8, 4)
	out := drainAll(r, pkts)
	if len(out) != 9 {
		t.Fatalf("recovered %d, want 9", len(out))
	}
	for i, rec := range out {
		if rec.Seq != int64(i) || rec.Concealed {
			t.Fatalf("item %d: seq=%d concealed=%v", i, rec.Seq, rec.Concealed)
		}
		if rec.TS != int64(i/3)*3000 {
			t.Fatalf("item %d: ts=%d", i, rec.TS)
		}
	}
	if r.Stats.Lost != 0 || r.Stats.Recovered != 0 {
		t.Fatalf("clean stream shows loss: %+v", r.Stats)
	}
}

func TestReorderAndDuplicate(t *testing.T) {
	pkts := mkPackets(t, 4, 100)
	// swap 1<->2, duplicate 3
	wire := []*pionrtp.Packet{pkts[0], pkts[2], pkts[1], pkts[3], pkts[3]}
	r := NewRecover(8, 4)
	out := drainAll(r, wire)
	if len(out) != 4 {
		t.Fatalf("got %d items, want 4", len(out))
	}
	for i, rec := range out {
		if rec.Seq != int64(i) {
			t.Fatalf("order broken at %d", i)
		}
	}
	if r.Stats.Duplicates != 1 {
		t.Fatalf("duplicates=%d, want 1", r.Stats.Duplicates)
	}
	if r.Stats.Reordered != 1 {
		t.Fatalf("reordered=%d, want 1", r.Stats.Reordered)
	}
}

func TestBurstLossConcealed(t *testing.T) {
	pkts := mkPackets(t, 10, 100)
	wire := append([]*pionrtp.Packet{}, pkts[:3]...)
	wire = append(wire, pkts[6:]...) // drop 3,4,5 (burst of 3)
	r := NewRecover(8, 4)
	out := drainAll(r, wire)
	if len(out) != 10 {
		t.Fatalf("got %d items, want 10", len(out))
	}
	concealed := 0
	for _, rec := range out {
		if rec.Concealed {
			concealed++
		}
	}
	if concealed != 3 || r.Stats.Recovered != 3 || r.Stats.Lost != 0 {
		t.Fatalf("concealed=%d stats=%+v", concealed, r.Stats)
	}
	if r.Stats.MaxBurst != 3 {
		t.Fatalf("max burst=%d, want 3", r.Stats.MaxBurst)
	}
}

func TestLossBeyondConcealLimit(t *testing.T) {
	pkts := mkPackets(t, 12, 100)
	wire := append([]*pionrtp.Packet{}, pkts[:2]...)
	wire = append(wire, pkts[8:]...) // burst of 6 > maxConceal 4
	r := NewRecover(8, 4)
	drainAll(r, wire)
	if r.Stats.Lost != 6 || r.Stats.Recovered != 0 {
		t.Fatalf("stats=%+v, want 6 lost", r.Stats)
	}
}

func TestSequenceWrap(t *testing.T) {
	p := NewPacketizer(1, 96, media.VideoScale)
	p.seq = 65534
	var pkts []*pionrtp.Packet
	for i := 0; i < 4; i++ {
		one, err := p.Packetize([]byte{byte(i)}, media.Ticks(i))
		if err != nil {
			t.Fatal(err)
		}
		pkts = append(pkts, one...)
	}
	r := NewRecover(8, 4)
	out := drainAll(r, pkts)
	for i, want := range []int64{65534, 65535, 65536, 65537} {
		if out[i].Seq != want {
			t.Fatalf("item %d: seq=%d, want %d", i, out[i].Seq, want)
		}
	}
}

func TestTimestampWrap(t *testing.T) {
	p := NewPacketizer(1, 96, media.VideoScale)
	base := int64(1<<32) - 3000
	var pkts []*pionrtp.Packet
	for i := 0; i < 3; i++ {
		one, err := p.Packetize([]byte{byte(i)}, media.Ticks(base+int64(i)*3000))
		if err != nil {
			t.Fatal(err)
		}
		pkts = append(pkts, one...)
	}
	r := NewRecover(8, 4)
	out := drainAll(r, pkts)
	for i := 0; i < 3; i++ {
		want := base + int64(i)*3000
		if out[i].TS != want {
			t.Fatalf("item %d: ts=%d, want %d (wrap not unwrapped)", i, out[i].TS, want)
		}
	}
}

func TestOversizeUnitRejected(t *testing.T) {
	p := NewPacketizer(1, 96, media.VideoScale)
	_, err := p.Packetize(make([]byte, MaxUnitBytes+1), 0)
	if media.CodeOf(err) != media.CodeLimitExceeded {
		t.Fatalf("got %v", err)
	}
}
