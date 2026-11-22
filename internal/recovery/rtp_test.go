package recovery

import (
	"testing"

	"github.com/pion/rtp"
)

func marshalPkt(t *testing.T, seq uint16, ts uint32, payload byte) []byte {
	t.Helper()
	p := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           42,
		},
		Payload: []byte{payload},
	}
	b, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReorderDuplicateAndRTXConflict(t *testing.T) {
	rcv, err := NewReceiver(8)
	if err != nil {
		t.Fatal(err)
	}
	seqs := []uint16{0, 1, 3, 2} // 2 fills the hole via late arrival
	for i, s := range seqs {
		if err := rcv.Handle(marshalPkt(t, s, 1000+uint32(i), byte(s))); err != nil {
			t.Fatal(err)
		}
	}
	want := []Disposition{DispAccepted, DispAccepted, DispAccepted, DispRetransmit}
	for i, ev := range rcv.report.Events {
		if ev.Disposition != want[i] {
			t.Fatalf("event %d = %s, want %s", i, ev.Disposition, want[i])
		}
	}
	// Identical resend of a known packet -> duplicate; changed payload on
	// an already filled slot -> retransmission conflict.
	if err := rcv.Handle(marshalPkt(t, 2, 1003, 2)); err != nil {
		t.Fatal(err)
	}
	if rcv.report.Events[4].Disposition != DispDuplicate {
		t.Fatalf("got %s, want duplicate", rcv.report.Events[4].Disposition)
	}
	if err := rcv.Handle(marshalPkt(t, 2, 9999, 99)); err != nil {
		t.Fatal(err)
	}
	if rcv.report.Events[5].Disposition != DispRTXConflict {
		t.Fatalf("got %s, want rtx_conflict", rcv.report.Events[5].Disposition)
	}
	// A second copy of an earlier in-order packet is a plain reorder dup.
	if err := rcv.Handle(marshalPkt(t, 1, 1001, 1)); err != nil {
		t.Fatal(err)
	}
	if rcv.report.Events[6].Disposition != DispDuplicate {
		t.Fatalf("got %s, want duplicate", rcv.report.Events[6].Disposition)
	}
}

func TestBurstLossAcrossBoundary(t *testing.T) {
	rcv, _ := NewReceiver(4)
	// 0..20 with 8,9,10,11 missing (a burst crossing a segment edge in
	// the fixture scenario).
	for s := 0; s <= 20; s++ {
		if s >= 8 && s <= 11 {
			continue
		}
		if err := rcv.Handle(marshalPkt(t, uint16(s), uint32(s*3000),
			byte(s))); err != nil {
			t.Fatal(err)
		}
	}
	rep := rcv.Finalize()
	if len(rep.Losses) != 1 || rep.Losses[0].Count != 4 {
		t.Fatalf("loss windows wrong: %+v", rep.Losses)
	}
	if rep.Losses[0].Recoverable {
		t.Fatal("aged-out burst must be unrecoverable")
	}
	if rep.Unrecoverable != 4 {
		t.Fatalf("unrecoverable = %d, want 4", rep.Unrecoverable)
	}
	if rep.RecoveryRate() != 0 {
		t.Fatalf("rate = %v, want 0 with no recovered packets", rep.RecoveryRate())
	}
}

func TestRetransmissionRecoversHole(t *testing.T) {
	rcv, _ := NewReceiver(4)
	_ = rcv.Handle(marshalPkt(t, 0, 0, 0))
	_ = rcv.Handle(marshalPkt(t, 1, 1, 1))
	// seq 2 missing initially
	_ = rcv.Handle(marshalPkt(t, 3, 3, 3))
	_ = rcv.Handle(marshalPkt(t, 4, 4, 4))
	// Retransmitted 2 arrives before the window ages out.
	_ = rcv.Handle(marshalPkt(t, 2, 2, 2))
	rep := rcv.Finalize()
	var found bool
	for _, ev := range rep.Events {
		if ev.Seq == 2 && ev.Disposition == DispRetransmit {
			found = true
		}
	}
	if !found {
		t.Fatalf("retransmit disposition missing: %+v", rep.Events)
	}
	if rep.Recovered != 1 || rep.RecoveryRate() != 1 {
		t.Fatalf("recovery stats wrong: %+v", rep)
	}
}

func TestSequenceWraparound(t *testing.T) {
	rcv, _ := NewReceiver(4)
	// Walk 65534..65535..0..3 to exercise 16-bit wrap.
	for _, s := range []uint16{65534, 65535, 0, 1, 2, 3} {
		if err := rcv.Handle(marshalPkt(t, s, uint32(s), byte(s))); err != nil {
			t.Fatal(err)
		}
	}
	rep := rcv.Finalize()
	if rep.Wraps != 1 {
		t.Fatalf("wraps = %d, want 1", rep.Wraps)
	}
	if rep.UniqueSeqs != 6 || rep.Unrecoverable != 0 {
		t.Fatalf("wrap session stats wrong: %+v", rep)
	}
	// Determinism: dispositions of the wrapped packets are all accepted.
	for i, ev := range rep.Events {
		if ev.Disposition != DispAccepted {
			t.Fatalf("event %d disposition %s", i, ev.Disposition)
		}
	}
}

func TestDeterministicDispositionsAcrossRuns(t *testing.T) {
	build := func() Report {
		rcv, _ := NewReceiver(6)
		for _, s := range []uint16{10, 11, 14, 12, 13, 18, 19, 20, 21, 22} {
			_ = rcv.Handle(marshalPkt(t, s, uint32(s), byte(s)))
		}
		_ = rcv.Handle(marshalPkt(t, 12, 12, 12)) // duplicate
		return rcv.Finalize()
	}
	a, b := build(), build()
	if len(a.Events) != len(b.Events) {
		t.Fatal("run length differs")
	}
	for i := range a.Events {
		if a.Events[i] != b.Events[i] {
			t.Fatalf("nondeterministic event at %d: %+v vs %+v",
				i, a.Events[i], b.Events[i])
		}
	}
}

func TestNewReceiverRejectsBadWindow(t *testing.T) {
	if _, err := NewReceiver(0); err == nil {
		t.Fatal("zero window must fail")
	}
	if _, err := NewReceiver(40000); err == nil {
		t.Fatal("oversized window must fail")
	}
}

func TestInvalidPacketRejected(t *testing.T) {
	rcv, _ := NewReceiver(4)
	if err := rcv.Handle([]byte{0x00}); err == nil {
		t.Fatal("truncated RTP packet must fail")
	}
}
