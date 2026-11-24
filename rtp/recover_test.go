package rtp_test

import (
	"testing"

	pionrtp "github.com/pion/rtp"

	"mediacheck/rtp"
	"mediacheck/timeline"
)

func packet(seq uint16) []byte {
	p := &pionrtp.Packet{
		Header:  pionrtp.Header{SequenceNumber: seq, PayloadType: 96, Version: 2},
		Payload: []byte{byte(seq >> 8), byte(seq)},
	}
	raw, err := p.Marshal()
	if err != nil {
		panic(err)
	}
	return raw
}

func feed(t *testing.T, r *rtp.Receiver, seqs ...uint16) {
	t.Helper()
	for _, s := range seqs {
		if err := r.Handle(packet(s)); err != nil {
			t.Fatalf("handle seq %d: %v", s, err)
		}
	}
}

func TestInOrderNoLoss(t *testing.T) {
	rcv := rtp.NewReceiver(8, 1000)
	feed(t, rcv, 0, 1, 2, 3, 4)
	out := rcv.Drain()
	if len(out) != 5 {
		t.Fatalf("want 5 ordered packets, got %d", len(out))
	}
	s := rcv.Stats()
	if s.Lost != 0 || s.RecoveryRate() != 1 {
		t.Fatalf("clean stream must show no loss: %+v", s)
	}
}

func TestDuplicatesNotCounted(t *testing.T) {
	rcv := rtp.NewReceiver(8, 1000)
	feed(t, rcv, 0, 1, 1, 2, 0)
	s := rcv.Stats()
	if s.Received != 3 || s.Duplicates != 2 || s.Lost != 0 {
		t.Fatalf("duplicate accounting wrong: %+v", s)
	}
}

func TestReorderRecoveredInWindow(t *testing.T) {
	rcv := rtp.NewReceiver(8, 1000)
	// 0,1 then 4,3,2: the gap is filled before the window expires.
	feed(t, rcv, 0, 1, 4, 3, 2)
	s := rcv.Stats()
	if s.Recovered != 2 || s.Unrecoverable != 0 {
		t.Fatalf("in-window recovery wrong: %+v", s)
	}
	out := rcv.Drain()
	if len(out) != 5 {
		t.Fatalf("drain after recovery want 5, got %d", len(out))
	}
	for i, p := range out {
		if p.SequenceNumber != uint16(i) {
			t.Fatalf("drain order broken at %d: %d", i, p.SequenceNumber)
		}
	}
}

func TestGapHoldsDrainUntilFilled(t *testing.T) {
	rcv := rtp.NewReceiver(8, 1000)
	feed(t, rcv, 0, 2) // seq 1 missing
	if out := rcv.Drain(); len(out) != 1 || out[0].SequenceNumber != 0 {
		t.Fatalf("gap must hold back seq 2, got %d packets", len(out))
	}
	feed(t, rcv, 1)
	out := rcv.Drain()
	if len(out) != 2 || out[0].SequenceNumber != 1 || out[1].SequenceNumber != 2 {
		t.Fatalf("filled gap should release in order: %+v", out)
	}
}

func TestOutOfWindowUnrecoverable(t *testing.T) {
	rcv := rtp.NewReceiver(4, 1000)
	feed(t, rcv, 0, 1)
	// Jump 10 packets ahead: seq 2..9 gap exceeds the window of 4.
	feed(t, rcv, 11)
	s := rcv.Stats()
	if s.Unrecoverable == 0 {
		t.Fatalf("over-window gap must be unrecoverable: %+v", s)
	}
	if s.RecoveryRate() >= 1 {
		t.Fatalf("recovery rate must drop below 1: %f", s.RecoveryRate())
	}
	if len(s.LostIntervals) == 0 {
		t.Fatal("unrecoverable gap must carry a timeline interval")
	}
	iv := s.LostIntervals[0]
	if iv.StartNs == 0 && iv.EndNs == 0 {
		t.Fatal("lost interval must be bound to timeline nanoseconds")
	}
	if iv.SeqStart != 2 {
		t.Fatalf("interval start want 2, got %d", iv.SeqStart)
	}
}

func TestSequenceRollover(t *testing.T) {
	rcv := rtp.NewReceiver(16, 1000)
	feed(t, rcv, 65534, 65535, 0, 1)
	out := rcv.Drain()
	if len(out) != 4 {
		t.Fatalf("rollover stream want 4 packets, got %d", len(out))
	}
	want := []uint16{65534, 65535, 0, 1}
	for i, p := range out {
		if p.SequenceNumber != want[i] {
			t.Fatalf("rollover order broken at %d: %d want %d", i, p.SequenceNumber, want[i])
		}
	}
}

func TestFlushAgesRemainingGaps(t *testing.T) {
	rcv := rtp.NewReceiver(64, 1000)
	feed(t, rcv, 0, 1, 3) // seq 2 never arrives
	rcv.Flush()
	s := rcv.Stats()
	if s.Unrecoverable != 1 || s.Recovered != 0 {
		t.Fatalf("flush must age the open gap: %+v", s)
	}
}

func TestInvalidPacketRejected(t *testing.T) {
	rcv := rtp.NewReceiver(8, 1000)
	if err := rcv.Handle([]byte{0x00}); !timeline.IsCategory(err, timeline.CatCorrupt) {
		t.Fatalf("want corrupt error for bad packet, got %v", err)
	}
}

func TestRecoveryRateClean(t *testing.T) {
	rcv := rtp.NewReceiver(8, 1000)
	feed(t, rcv, 0, 1, 2)
	if rcv.Stats().RecoveryRate() != 1 {
		t.Fatal("clean feed recovery rate must be exactly 1")
	}
}
