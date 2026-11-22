package rtp

import (
	"errors"
	"testing"
)

func testPacket(seq uint16, ts uint32) Packet {
	return Packet{Seq: seq, TS: ts, SSRC: 7, Payload: []byte{byte(seq)}}
}

// at pushes pkt at arrival time and returns packets released by a flush at now.
func at(t *testing.T, buf *ReorderBuffer, pkt Packet, now int64) []Packet {
	t.Helper()
	if _, err := buf.Push(pkt, now); err != nil {
		t.Fatalf("push %d: %v", pkt.Seq, err)
	}
	return buf.Flush(now).Ready
}

func TestReorderInOrderWithHold(t *testing.T) {
	buf, err := NewReorderBuffer(Config{Window: 8, HoldTicks: 100})
	if err != nil {
		t.Fatal(err)
	}
	var got []Packet
	for i, seq := range []uint16{10, 11, 12} {
		now := int64(i * 100)
		got = append(got, at(t, buf, testPacket(seq, uint32(i*100)), now)...)
	}
	// Each packet waits out the hold; the last needs another flush.
	got = append(got, buf.Flush(500).Ready...)
	if len(got) != 3 {
		t.Fatalf("emitted %d", len(got))
	}
	for i, p := range got {
		if p.Seq != uint16(10+i) {
			t.Fatalf("out[%d].Seq = %d", i, p.Seq)
		}
	}
	if st := buf.Stats(); st.Emitted != 3 || st.Buffered != 0 || st.Reordered != 0 {
		t.Fatalf("stats %+v", st)
	}
}

func TestReorderSortsInsideWindow(t *testing.T) {
	buf, _ := NewReorderBuffer(Config{Window: 4, HoldTicks: 100})
	if res, _ := buf.Push(testPacket(100, 1000), 0); res.Status != StatusAccepted {
		t.Fatalf("status %v", res.Status)
	}
	// 103,101,102 arrive during the hold, then everything flushes in order.
	at(t, buf, testPacket(103, 1300), 10)
	at(t, buf, testPacket(101, 1100), 20)
	got := at(t, buf, testPacket(102, 1200), 30)
	got = append(got, buf.Flush(1000).Ready...)
	if len(got) != 4 {
		t.Fatalf("emitted %d", len(got))
	}
	for i, p := range got {
		if p.Seq != uint16(100+i) {
			t.Fatalf("out[%d] = %d", i, p.Seq)
		}
	}
	if st := buf.Stats(); st.Emitted != 4 || st.Reordered == 0 || st.Duplicates != 0 {
		t.Fatalf("stats %+v", st)
	}
}

func TestReorderReportsDueGapUntilFilled(t *testing.T) {
	buf, _ := NewReorderBuffer(Config{Window: 6, HoldTicks: 50})
	buf.Push(testPacket(200, 2000), 0)
	buf.Push(testPacket(202, 2200), 10)
	res := buf.Flush(100)
	if len(res.Ready) != 1 || res.Ready[0].Seq != 200 {
		t.Fatalf("first release = %+v", res.Ready)
	}
	if !res.HasGap || res.DueGap.Start != 201 || res.DueGap.Count != 1 {
		t.Fatalf("due gap %+v", res)
	}
	// The same hole is reported only once per flush cycle.
	if again := buf.Flush(110); again.HasGap {
		t.Fatalf("gap re-reported: %+v", again)
	}
	// 201 arrives late but inside the hold: present it as a retransmission.
	if res, _ := buf.Push(testPacket(201, 2100), 120); res.Status != StatusAccepted {
		t.Fatalf("late retransmission rejected: %v", res.Status)
	}
	ready := buf.Flush(1000).Ready
	if len(ready) != 2 || ready[0].Seq != 201 || ready[1].Seq != 202 {
		t.Fatalf("ready = %+v", ready)
	}
}

func TestReorderRejectsDuplicatesCountsLate(t *testing.T) {
	buf, _ := NewReorderBuffer(Config{Window: 4, HoldTicks: 10})
	buf.Push(testPacket(21, 210), 0)
	if res, _ := buf.Push(testPacket(21, 210), 1); res.Status != StatusDuplicate {
		t.Fatalf("pending duplicate status %v", res.Status)
	}
	// 20 has never been emitted and the emission point is already past it.
	if res, _ := buf.Push(testPacket(20, 200), 2); res.Status != StatusLate {
		t.Fatalf("late status %v", res.Status)
	}
	if ready := buf.Flush(100).Ready; len(ready) != 1 || ready[0].Seq != 21 {
		t.Fatalf("ready %+v", ready)
	}
	if res, _ := buf.Push(testPacket(21, 210), 101); res.Status != StatusDuplicate {
		t.Fatalf("emitted duplicate status %v", res.Status)
	}
	st := buf.Stats()
	if st.Duplicates != 2 || st.LateDropped != 1 {
		t.Fatalf("stats %+v", st)
	}
}

func TestReorderOutOfWindow(t *testing.T) {
	buf, _ := NewReorderBuffer(Config{Window: 3, HoldTicks: 10})
	buf.Push(testPacket(1, 0), 0)
	if res, _ := buf.Push(testPacket(9, 90), 1); res.Status != StatusOutOfWindow {
		t.Fatalf("status %v", res.Status)
	}
	if res, _ := buf.Push(testPacket(5, 50), 2); res.Status != StatusOutOfWindow {
		t.Fatalf("beyond next+window status %v", res.Status)
	}
	buf.Flush(100)
	if st := buf.Stats(); st.OutOfWindow != 2 {
		t.Fatalf("stats %+v", st)
	}
}

func TestReorderAcrossSequenceWrap(t *testing.T) {
	buf, _ := NewReorderBuffer(Config{Window: 8, HoldTicks: 20})
	pkts := []Packet{
		testPacket(0xFFFE, 100),
		testPacket(0xFFFF, 200),
		testPacket(0x0001, 400),
		testPacket(0x0000, 300),
		testPacket(0x0002, 500),
	}
	var got []Packet
	for i, p := range pkts {
		res, err := buf.Push(p, int64(i*5))
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != StatusAccepted {
			t.Fatalf("status %v", res.Status)
		}
	}
	got = buf.Flush(100).Ready
	if len(got) != 5 {
		t.Fatalf("emitted %d, want 5", len(got))
	}
	for i, want := range []uint16{0xFFFE, 0xFFFF, 0x0000, 0x0001, 0x0002} {
		if got[i].Seq != want {
			t.Fatalf("out[%d] = %#04x want %#04x", i, got[i].Seq, want)
		}
		if i > 0 && got[i].TS <= got[i-1].TS {
			t.Fatalf("timestamps not ordered: %d then %d", got[i-1].TS, got[i].TS)
		}
	}
	if rest, gaps := buf.Drain(); len(rest) != 0 || len(gaps) != 0 {
		t.Fatalf("unexpected drain output %v %v", rest, gaps)
	}
}

func TestReorderSkipGap(t *testing.T) {
	buf, _ := NewReorderBuffer(Config{Window: 6, HoldTicks: 10})
	buf.Push(testPacket(200, 2000), 0)
	buf.Push(testPacket(203, 2300), 1)
	buf.Push(testPacket(201, 2100), 2)
	buf.Push(testPacket(202, 2200), 3)
	buf.Flush(1000)
	buf.Push(testPacket(205, 2500), 2000)
	rng, ok := buf.PendingGap()
	if !ok || rng.Start != 204 || rng.End != 204 || rng.Count != 1 || rng.Wrapped {
		t.Fatalf("gap %+v ok=%v", rng, ok)
	}
	ready, skipped, ok := buf.SkipGap(3000)
	if !ok || skipped.Count != 1 || len(ready) != 1 {
		t.Fatalf("skip ready=%v gap=%v", ready, skipped)
	}
	if ready[0].Seq != 205 {
		t.Fatalf("tail = %d", ready[0].Seq)
	}
	if st := buf.Stats(); st.GapsDeclared != 1 || st.GapPackets != 1 {
		t.Fatalf("stats %+v", st)
	}
}

func TestReorderDrainReportsGapsWithoutFabricating(t *testing.T) {
	buf, _ := NewReorderBuffer(Config{Window: 8, HoldTicks: 10})
	for i, seq := range []uint16{300, 301, 304, 305} {
		buf.Push(testPacket(seq, uint32(seq)*10), int64(i))
	}
	got, gaps := buf.Drain()
	if len(got) != 4 {
		t.Fatalf("drained %d", len(got))
	}
	for i, want := range []uint16{300, 301, 304, 305} {
		if got[i].Seq != want {
			t.Fatalf("drain[%d] = %d", i, got[i].Seq)
		}
	}
	if len(gaps) != 1 || gaps[0].Start != 302 || gaps[0].Count != 2 || gaps[0].Wrapped {
		t.Fatalf("gaps %+v", gaps)
	}
}

func TestReorderWindowAndHoldValidation(t *testing.T) {
	for _, w := range []int{0, -1, MaxWindow + 1} {
		if _, err := NewReorderBuffer(Config{Window: w}); !errors.Is(err, ErrInvalidWindow) {
			t.Fatalf("window %d accepted", w)
		}
	}
	for _, h := range []int64{-1, MaxHoldTicks + 1} {
		if _, err := NewReorderBuffer(Config{Window: 4, HoldTicks: h}); !errors.Is(err, ErrInvalidHold) {
			t.Fatalf("hold %d accepted", h)
		}
	}
}
