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
	// The first Window packets are held while stream-start probation checks
	// for an earlier in-window sequence; Drain settles it.
	all = append(all, r.Drain()...)
	if got := seqOf(all); len(got) != 5 || got[0] != 0 || got[4] != 4 {
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
	// Probation holds the opening packets so an earlier sequence can still
	// claim the origin; nothing may be released before it settles.
	if out := push(0); len(out) != 0 {
		t.Fatalf("probation must not release yet: %v", out)
	}
	if out := push(2); len(out) != 0 { // 1 missing, hold 2
		t.Fatalf("seq 2 must be held: %v", out)
	}
	// seq 4 is a full window past the origin, so probation settles here: the
	// lowest buffered sequence (0) becomes the origin and contiguous packets
	// release until the hole at seq 1.
	if out := push(4); len(out) != 1 || out[0].Seq != 0 {
		t.Fatalf("seq 4 should settle probation and release origin: %v", seqOf(out))
	}
	out := push(1) // fills the hole, flushes 1 and 2
	if got := seqOf(out); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("contiguous flush wrong: %v", got)
	}
	// seq 3 was genuinely missing and is still inside the window, so 4 waits
	// until it arrives; the reorder was fully repaired with no loss.
	out = push(3)
	if got := seqOf(out); len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Fatalf("late hole fill flush wrong: %v", got)
	}
	if r.Stats().Reordered < 2 {
		t.Fatalf("reordered not counted: %+v", r.Stats())
	}
	if r.Held() != 0 {
		t.Fatalf("buffer should be empty: %d", r.Held())
	}
	_ = r.Drain()
	if r.Stats().Lost != 0 {
		t.Fatalf("in-window reorder must not count loss: %+v", r.Stats())
	}
	if r.Drain() != nil {
		t.Fatal("nothing should remain buffered")
	}
}

func TestGapOutsideWindow(t *testing.T) {
	// Window 2: hold buffer may contain at most two packets ahead of a hole.
	r := NewReorderer(9, 2)
	r.Push(mkPacket(9, 10, 1000)) // released immediately, next=11
	r.Push(mkPacket(9, 11, 1100)) // released, next=12
	// Fill the window with 13,14 while 12 is missing.
	r.Push(mkPacket(9, 13, 1300))
	r.Push(mkPacket(9, 14, 1400))
	if r.Held() != 2 {
		t.Fatalf("window should hold two packets: %d", r.Held())
	}
	// seq 15 cannot be buffered without exceeding the window; the oldest
	// hole (seq 12) is evicted as lost, then 13..15 flush in sequence.
	out, dup, err := r.Push(mkPacket(9, 15, 1500))
	if err != nil || dup || len(out) != 3 {
		t.Fatalf("eviction push should flush 13..15: out=%v err=%v", out, err)
	}
	st := r.Stats()
	if st.Lost != 1 || len(st.Gaps) != 1 {
		t.Fatalf("eviction did not declare exactly one loss: %+v", st)
	}
	g := st.Gaps[0]
	if g.FromSeq != 12 || g.ToSeq != 12 || g.Count != 1 {
		t.Fatalf("evicted gap bounds wrong: %+v", g)
	}
	if g.StartTS != 1100 || g.EndTS != 1500 {
		t.Fatalf("gap timestamp span wrong: %+v", g)
	}
	// Re-delivering an evicted sequence is an idempotent duplicate.
	_, dup, _ = r.Push(mkPacket(9, 12, 1200))
	if !dup {
		t.Fatal("late evicted seq must be idempotent duplicate")
	}
	if r.Drain() != nil {
		t.Fatal("buffer must be empty after flush and duplicate")
	}
}

func TestGapClosedAtDrain(t *testing.T) {
	// Window 4: seq 0 then seq 2 (hold, seq 1 missing but within window).
	r := NewReorderer(4, 4)
	r.Push(mkPacket(4, 0, 0))
	r.Push(mkPacket(4, 2, 200))
	if r.Stats().Lost != 0 {
		t.Fatal("hole inside window must not count as lost early")
	}
	// Stream ends without seq 1: drain settles probation, declares the
	// terminal loss and releases every held packet in order.
	out := r.Drain()
	if len(out) != 2 || out[0].Seq != 0 || out[1].Seq != 2 {
		t.Fatalf("drain output wrong: %v", seqOf(out))
	}
	st := r.Stats()
	if st.Lost != 1 || len(st.Gaps) != 1 {
		t.Fatalf("drain did not close terminal gap: %+v", st)
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
	all = append(all, r.Drain()...)
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
	// Everything was still in probation (only two packets per stream), so
	// Drain is what finally releases them; nothing may be lost.
	rest := d.Drain()
	if len(rest[100]) != 2 || len(rest[200]) != 1 || len(rest[999]) != 1 {
		t.Fatalf("drain should release probation-held packets: %+v", rest)
	}
	if d.Stream(100).Stats().Lost != 0 || d.Stream(200).Stats().Lost != 0 {
		t.Fatalf("probation drain must not report loss: %+v", rest)
	}
	if again := d.Drain(); len(again) != 0 {
		t.Fatalf("second drain must be empty: %+v", again)
	}
	// With a hole still open, drain closes it and releases held packets.
	d2 := NewDemux(4)
	d2.Push(mkPacket(7, 0, 0))
	d2.Push(mkPacket(7, 2, 200)) // seq 1 missing, held inside window
	rest2 := d2.Drain()
	if len(rest2[7]) != 2 || rest2[7][0].Seq != 0 || rest2[7][1].Seq != 2 {
		t.Fatalf("drain did not release held packets: %+v", rest2)
	}
	if d2.Stream(7).Stats().Lost != 1 {
		t.Fatalf("drain did not close terminal gap: %+v", d2.Stream(7).Stats())
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

// TestStreamStartProbation covers the case a packetized frame can produce:
// the higher sequence of a split frame arrives first. The window must repair
// it once the earlier packet shows up instead of pinning the origin and
// declaring the frame truncated.
func TestStreamStartProbation(t *testing.T) {
	r := NewReorderer(55, 8)
	// seq 1 (second fragment) races ahead of seq 0 (first fragment).
	if out, dup, err := r.Push(mkPacket(55, 1, 100)); err != nil || dup || len(out) != 0 {
		t.Fatalf("first packet must be held for probation: out=%v dup=%v err=%v", out, dup, err)
	}
	out, dup, err := r.Push(mkPacket(55, 0, 100))
	if err != nil || dup {
		t.Fatalf("second packet: dup=%v err=%v", dup, err)
	}
	// Probation settles only once the window has been observed; drain forces
	// it here because the stream is only two packets long.
	out = append(out, r.Drain()...)
	if got := seqOf(out); len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Fatalf("start reorder not repaired: %v", got)
	}
	if st := r.Stats(); st.Lost != 0 {
		t.Fatalf("start reorder counted as loss: %+v", st)
	}
}

func TestProbationReplayIsDuplicate(t *testing.T) {
	r := NewReorderer(56, 8)
	r.Push(mkPacket(56, 5, 0))
	r.Push(mkPacket(56, 5, 0)) // replay while probation is open
	if r.Stats().Duplicates != 1 {
		t.Fatalf("replay during probation not deduplicated: %+v", r.Stats())
	}
	if r.Stats().Received != 1 {
		t.Fatalf("replay inflated accepted count: %+v", r.Stats())
	}
}
