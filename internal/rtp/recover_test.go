package rtp

import (
	"testing"

	"github.com/pion/rtp"

	"regress/internal/media"
)

// mkPacket builds a minimal RTP packet with a deterministic payload.
func mkPacket(ssrc uint32, seq uint16, ts uint32) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           ssrc,
		},
		Payload: []byte{byte(seq >> 8), byte(seq), 0xAA},
	}
}

func TestCleanStream(t *testing.T) {
	var pkts []*rtp.Packet
	for s := uint16(0); s < 10; s++ {
		pkts = append(pkts, mkPacket(1, s, uint32(s)*160))
	}
	states, errs := Recover(pkts, map[uint32]uint32{1: 8000})
	if len(errs) != 0 || len(states) != 1 {
		t.Fatalf("errs=%v states=%d", errs, len(states))
	}
	st := states[0]
	if len(st.Ordered) != 10 || len(st.Gaps) != 0 {
		t.Fatalf("clean stream: ordered=%d gaps=%v", len(st.Ordered), st.Gaps)
	}
	if st.Duplicates != 0 || st.Reordered != 0 || st.BurstLost != 0 {
		t.Fatalf("counters wrong: %+v", st)
	}
	if st.FirstSeq != 0 || st.LastSeq != 9 {
		t.Fatalf("bounds wrong: %d..%d", st.FirstSeq, st.LastSeq)
	}
}

func TestDuplicatesAndReorder(t *testing.T) {
	// 0,1,2,3,2(dup),5,4(reorder),6,7
	pkts := []*rtp.Packet{
		mkPacket(1, 0, 0), mkPacket(1, 1, 160), mkPacket(1, 2, 320),
		mkPacket(1, 3, 480), mkPacket(1, 2, 320), mkPacket(1, 5, 800),
		mkPacket(1, 4, 640), mkPacket(1, 6, 960), mkPacket(1, 7, 1120),
	}
	states, _ := Recover(pkts, map[uint32]uint32{1: 8000})
	st := states[0]
	if st.Duplicates != 1 {
		t.Fatalf("duplicates=%d, want 1", st.Duplicates)
	}
	if st.Reordered != 1 {
		t.Fatalf("reordered=%d, want 1", st.Reordered)
	}
	for i, p := range st.Ordered {
		if p.SequenceNumber != uint16(i) {
			t.Fatalf("order broken at %d: seq=%d", i, p.SequenceNumber)
		}
	}
}

func TestBurstLossBecomesGap(t *testing.T) {
	// Packets 4,5,6,7 are lost; high-water advances past the window so the
	// loss must be retained as one 4-packet gap, never fabricated.
	var pkts []*rtp.Packet
	for s := uint16(0); s < 4; s++ {
		pkts = append(pkts, mkPacket(7, s, 0))
	}
	for s := uint16(8); s < 80; s++ {
		pkts = append(pkts, mkPacket(7, s, 0))
	}
	states, _ := Recover(pkts, map[uint32]uint32{7: 90000})
	st := states[0]
	if len(st.Gaps) != 1 {
		t.Fatalf("gaps=%v, want exactly one", st.Gaps)
	}
	g := st.Gaps[0]
	if g.FirstSeq != 4 || g.LastSeq != 7 || g.Count != 4 {
		t.Fatalf("gap=%+v, want 4..7 count 4", g)
	}
	if st.BurstLost != 4 {
		t.Fatalf("burstLost=%d, want 4", st.BurstLost)
	}
	// 76 packets were actually delivered (0..3 and 8..79); the four missing
	// sequence numbers must appear as a gap but never as fabricated output.
	if len(st.Ordered) != 76 {
		t.Fatalf("ordered=%d, want 76 (no fabricated packets)", len(st.Ordered))
	}
	if st.Ordered[0].SequenceNumber != 0 || st.Ordered[len(st.Ordered)-1].SequenceNumber != 79 {
		t.Fatalf("ordered bounds wrong: %d..%d",
			st.Ordered[0].SequenceNumber, st.Ordered[len(st.Ordered)-1].SequenceNumber)
	}
}

func TestTrailingLoss(t *testing.T) {
	// The stream ends with packets 20,21 missing while packet 22 was observed
	// (e.g. a late anchor). At finish() the gap must still be retained rather
	// than silently dropped from the timeline.
	var pkts []*rtp.Packet
	for s := uint16(0); s < 20; s++ {
		pkts = append(pkts, mkPacket(3, s, 0))
	}
	pkts = append(pkts, mkPacket(3, 22, 0))
	states, _ := Recover(pkts, map[uint32]uint32{3: 90000})
	st := states[0]
	var found bool
	for _, g := range st.Gaps {
		if g.FirstSeq == 20 && g.LastSeq == 21 && g.Count == 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("trailing gap 20..21 not retained: %+v", st.Gaps)
	}
	if len(st.Ordered) != 21 { // 0..19 and 22; no fabricated 20,21
		t.Fatalf("ordered=%d, want 21 (no fabricated packets)", len(st.Ordered))
	}
}

func TestSequenceWrap(t *testing.T) {
	pkts := []*rtp.Packet{
		mkPacket(9, 65534, 1), mkPacket(9, 65535, 2),
		mkPacket(9, 0, 3), mkPacket(9, 1, 4),
	}
	states, _ := Recover(pkts, map[uint32]uint32{9: 8000})
	st := states[0]
	if len(st.Ordered) != 4 || st.Ordered[3].SequenceNumber != 1 {
		t.Fatalf("wrap ordering wrong: %d packets", len(st.Ordered))
	}
}

func TestDigestsAndMultipleSSRC(t *testing.T) {
	a := []*rtp.Packet{mkPacket(1, 0, 0), mkPacket(1, 1, 0)}
	b := []*rtp.Packet{mkPacket(2, 0, 0), mkPacket(2, 1, 0)}
	states, _ := Recover(append(a, b...), map[uint32]uint32{1: 8000, 2: 8000})
	if len(states) != 2 {
		t.Fatalf("states=%d, want 2 SSRC tracks", len(states))
	}
	// Same payload content repeated yields equal payload digests regardless
	// of SSRC; container digests carry framing and differ by SSRC stream.
	r1, _ := Recover(a, map[uint32]uint32{1: 8000})
	r2, _ := Recover(a, map[uint32]uint32{1: 8000})
	if r1[0].PayloadDigest != r2[0].PayloadDigest {
		t.Fatal("identical input must reuse the same digest")
	}
}

func TestNilPacketReported(t *testing.T) {
	_, errs := Recover([]*rtp.Packet{nil}, nil)
	if len(errs) != 1 {
		t.Fatalf("errs=%v, want one structured error", errs)
	}
	if me, ok := media.AsError(errs[0]); !ok || me.Code != media.CodeInvalidInput {
		t.Fatalf("err=%v, want CodeInvalidInput", errs[0])
	}
}

func TestMediaTimeRejectsZeroClock(t *testing.T) {
	if _, err := MediaTime(100, 0); err == nil {
		t.Fatal("zero clock rate accepted")
	}
	if r, err := MediaTime(4000, 8000); err != nil || r.String() != "1/2" {
		t.Fatalf("rtp media time wrong: %s err=%v", r, err)
	}
}
