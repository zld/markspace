package transport

import (
	"testing"

	"mediaroute/internal/clock"
)

func mkPacket(ssrc uint32, seq uint16, ts uint32) *Packet {
	return &Packet{SSRC: ssrc, Seq: seq, Timestamp: ts, Payload: []byte{byte(seq)}}
}

func seqOf(out []*Packet) []uint16 {
	got := make([]uint16, len(out))
	for i, p := range out {
		got[i] = p.Seq
	}
	return got
}

func TestInOrderAndDuplicate(t *testing.T) {
	r := NewReorderer(1, 8)
	var all []*Packet
	for seq := uint16(0); seq < 5; seq++ {
		out, dup, err := r.Push(mkPacket(1, seq, uint32(seq)*100))
		if err != nil || dup {
			t.Fatalf("seq %d: dup=%v err=%v", seq, dup, err)
		}
		all = append(all, out...)
	}
	if got := seqOf(all); len(got) != 5 || got[4] != 4 {
		t.Fatalf("in-order release broken: %v", got)
	}
	// Repeated deliveries must be idempotent.
	for seq := uint16(0); seq < 5; seq++ {
		_, dup, err := r.Push(mkPacket(1, seq, uint32(seq)*100))
		if err != nil || !dup {
			t.Fatalf("seq %d not treated as duplicate", seq)
		}
	}
	st := r.Stats()
	if st.Duplicates != 5 || st.Received != 5 {
		t.Fatalf("stats wrong: %+v", st)
	}
}

func TestReorderWithinWindow(t *testing.T) {
	r := NewReorderer(7, 4)
	push := func(seq uint16) []*Packet {
		out, dup, err := r.Push(mkPacket(7, seq, uint32(seq)*90))
		if err != nil || dup {
			t.Fatalf("seq %d: dup=%v err=%v", seq, dup, err)
		}
		return out
	}
	if out := push(0); len(out) != 1 || out[0].Seq != 0 {
		t.Fatalf("first packet should flush: %v", out)
	}
	if out := push(2); len(out) != 0 { // 1 missing, hold 2
		t.Fatalf("seq 2 must be held: %v", out)
	}
	if out := push(3); len(out) != 0 {
		t.Fatalf("seq 3 must be held: %v", out)
	}
	out := push(1) // fills the hole, flushes 1,2,3
	if got := seqOf(out); len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("contiguous flush wrong: %v", got)
	}
	if r.Stats().Reordered < 2 {
		t.Fatalf("reordered not counted: %+v", r.Stats())
	}
	if r.Held() != 0 {
		t.Fatalf("buffer should be empty: %d", r.Held())
	}
}

func TestGapOutsideWindow(t *testing.T) {
	r := NewReorderer(9, 3)
	r.Push(mkPacket(9, 10, 1000))
	// next is now 11; seq 15 has distance 4 > window 3: burst gap 11..14,
	// skip ahead rather than buffering without bound.
	out, dup, err := r.Push(mkPacket(9, 15, 1500))
	if err != nil || dup {
		t.Fatalf("gap push: %v", err)
	}
	if len(out) != 1 || out[0].Seq != 15 {
		t.Fatalf("post-gap packet should flush: %v", out)
	}
	st := r.Stats()
	if st.Lost != 4 || len(st.Gaps) != 1 {
		t.Fatalf("gap not recorded: %+v", st)
	}
	g := st.Gaps[0]
	if g.FromSeq != 11 || g.ToSeq != 14 || g.Recovered {
		t.Fatalf("gap bounds wrong: %+v", g)
	}
	if g.StartTS != 1000 || g.EndTS != 1500 {
		t.Fatalf("affected time span not preserved: %+v", g)
	}
	// A late packet inside the closed gap is a duplicate, never silently merged.
	_, dup, _ = r.Push(mkPacket(9, 12, 1200))
	if !dup {
		t.Fatal("late gap packet must be idempotent duplicate")
	}
}

func TestSequenceWraparound(t *testing.T) {
	r := NewReorderer(3, 8)
	var all []*Packet
	for _, seq := range []uint16{65534, 65535, 0, 1, 2} {
		out, _, err := r.Push(mkPacket(3, seq, uint32(seq)))
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, out...)
	}
	got := seqOf(all)
	want := []uint16{65534, 65535, 0, 1, 2}
	if len(got) != len(want) {
		t.Fatalf("wrap ordering: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("wrap ordering: got %v want %v", got, want)
		}
	}
	// Duplicate after wrap must stay a duplicate.
	_, dup, _ := r.Push(mkPacket(3, 65535, 0))
	if !dup {
		t.Fatal("post-wrap duplicate not recognized")
	}
}

func TestDemux(t *testing.T) {
	d := NewDemux(4)
	pkts := []*Packet{mkPacket(100, 0, 0), mkPacket(200, 0, 0), mkPacket(100, 1, 90)}
	for _, p := range pkts {
		if _, _, err := d.Push(p); err != nil {
			t.Fatal(err)
		}
	}
	if d.Stream(100).Stats().Received != 2 || d.Stream(200).Stats().Received != 1 {
		t.Fatal("demux routing failed")
	}
	if _, _, err := d.Push(mkPacket(999, 0, 0)); err != nil {
		t.Fatalf("lazy stream creation failed: %v", err)
	}
	if d.Stream(999) == nil {
		t.Fatal("demux should lazily create streams")
	}
}

func TestParseValidation(t *testing.T) {
	if _, err := ParsePacket([]byte{0x00}); err == nil {
		t.Fatal("trash accepted as RTP")
	}
	pe, ok := clock.AsError(ParsePacketMustFail(t))
	if !ok || pe.Phase != clock.PhaseTransport {
		t.Fatalf("error model missing on bad RTP: %v", pe)
	}
}

func ParsePacketMustFail(t *testing.T) error {
	t.Helper()
	_, err := ParsePacket([]byte{0x80, 0x60, 0x00, 0x01})
	return err
}
