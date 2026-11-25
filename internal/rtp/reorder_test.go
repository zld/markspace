package rtp

import (
	"testing"

	"github.com/pion/rtp"

	"mediacore/internal/fault"
)

func pkt(ssrc uint32, seq uint16) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: ssrc, SequenceNumber: seq, Timestamp: uint32(seq) * 1000}, Payload: []byte{byte(seq)}}
}

func TestReorderAllDedupAndGap(t *testing.T) {
	// Arrival order: 3, 1, duplicate 3, 0. Sequence 2 is permanently missing.
	packets := []*rtp.Packet{pkt(7, 3), pkt(7, 1), pkt(7, 3), pkt(7, 0)}
	rep, err := ReorderAll(packets, 64)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Stats.DuplicatesRemoved != 1 {
		t.Fatalf("duplicates=%d want 1", rep.Stats.DuplicatesRemoved)
	}
	if rep.Stats.ReorderedPackets < 2 {
		t.Fatalf("reordered=%d want >=2", rep.Stats.ReorderedPackets)
	}
	if rep.Stats.MissingPackets != 1 || len(rep.Gaps) != 1 {
		t.Fatalf("missing=%d gaps=%+v want one missing sequence", rep.Stats.MissingPackets, rep.Gaps)
	}
	got := []uint16{rep.Packets[0].Packet.SequenceNumber, rep.Packets[1].Packet.SequenceNumber, rep.Packets[2].Packet.SequenceNumber}
	if got[0] != 0 || got[1] != 1 || got[2] != 3 {
		t.Fatalf("order=%v want [0 1 3]", got)
	}
}

func TestLatePacketDoesNotRewriteCommittedTimeline(t *testing.T) {
	r := NewReorderer(2)
	for _, seq := range []uint16{0, 3, 4} {
		if _, err := r.Push(Item{ArrivalIndex: int(seq), Packet: pkt(9, seq)}); err != nil {
			t.Fatal(err)
		}
	}
	if r.Stats.MissingPackets != 2 {
		t.Fatalf("missing after gap commit=%d want 2", r.Stats.MissingPackets)
	}
	_, err := r.Push(Item{ArrivalIndex: 99, Packet: pkt(9, 1)})
	if !fault.IsCode(err, fault.Conflict) {
		t.Fatalf("late packet err=%v want conflict", err)
	}
	if r.Stats.LateRejected != 1 {
		t.Fatalf("late_rejected=%d want 1", r.Stats.LateRejected)
	}
	ordered, err := r.Flush()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range ordered {
		if item.Packet.SequenceNumber == 1 {
			t.Fatal("late sequence 1 must not be inserted after timeline commit")
		}
	}
}
